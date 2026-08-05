package service

// volume_derive.go — 执行方案-04 · C3.1b:3D 体数据的派生。把一卷 NIfTI 变成
//   ① volume_meta(单文件 JSON:几何 + 窗位,viewer 的坐标/渲染依据);
//   ② volume_slices(目录前缀,逐 z 切片 16-bit 灰度 PNG,viewer 逐片加载)。
//
// 切片一律存**体素索引顺序**(i 最快、j 次之,不翻转)——几何存体素空间,
// 世界朝向由 volume_meta.direction 决定,渲染时才应用(C-H3)。在派生里翻转
// 会和 viewer 的方向处理叠加,重演"左右翻转不报错"的事故。
//
// 位深 16-bit(C0.5 决策):有符号数据整体上移到无符号装进 PNG16,上移量折进
// SliceScl 线性映射——viewer 对原始像素做一次仿射即得真值(HU),不丢精度、
// 不需要单独的 offset 字段。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// deriveVolume produces volume_meta + per-slice PNG16 for a volume asset.
func (w *MediaWorker) deriveVolume(ctx context.Context, a *dbmodel.Asset) error {
	rc, err := w.store.Get(ctx, a.StorageURI)
	if err != nil {
		return fmt.Errorf("fetch volume: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read volume: %w", err)
	}
	raw, err = gunzipIfNeeded(raw)
	if err != nil {
		return err
	}

	meta, err := ParseNIfTIMeta(raw)
	if err != nil {
		// A malformed medical file is a terminal QC-style failure, not a retry.
		return terminalf("无法解析为 NIfTI 体数据：%v", err)
	}

	nx, ny, nz := meta.Dims[0], meta.Dims[1], meta.Dims[2]
	dt, ok := datatypeByName(meta.Datatype)
	if !ok {
		return terminalf("不支持的体数据位深：%s", meta.Datatype)
	}
	sSlope, sInter, encode, ok := sliceEncoder(dt, meta.SclSlope, meta.SclInter)
	if !ok {
		return terminalf("体数据位深 %s 暂不支持切片派生（整数 CT/MRI 已支持；float/int32 待实现）", meta.Datatype)
	}
	meta.SliceSclSlope, meta.SliceSclInter = sSlope, sInter

	// Voxel data starts at vox_offset. ParseNIfTIMeta 已校验它在 [352, len] 内(#6);
	// 这里再核一次"体素数据整段在文件内"再切片,防越界 panic。
	voxOffset := int(float64FromF32(raw, 108))
	sliceVox := nx * ny
	sliceBytes := sliceVox * dt.bytes
	if voxOffset < 0 || int64(voxOffset)+int64(nz)*int64(sliceBytes) > int64(len(raw)) {
		return terminalf("体素数据超出文件范围(vox_offset=%d, 需要 %d 字节, 文件 %d)", voxOffset, int64(nz)*int64(sliceBytes), len(raw))
	}
	data := raw[voxOffset:]

	// ① volume_meta (single-file derivative).
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := w.putDerivative(ctx, a, dbmodel.DerivativeVolumeMeta, paramsHash("vm-v1"), "application/json", metaJSON); err != nil {
		return err
	}

	// ② per-slice PNG16 under the volume_slices prefix.
	prefix := fmt.Sprintf("derived/%s/%s/v1/", a.SHA256, dbmodel.DerivativeVolumeSlices)
	var totalBytes int64
	for k := 0; k < nz; k++ {
		off := k * sliceBytes
		slice := data[off : off+sliceBytes]
		png16, err := encodeSlicePNG16(slice, nx, ny, dt, encode)
		if err != nil {
			return fmt.Errorf("encode slice %d: %w", k, err)
		}
		key := fmt.Sprintf("%s%04d.png", prefix, k)
		if _, err := w.store.PutAt(ctx, key, bytes.NewReader(png16), int64(len(png16)), "image/png"); err != nil {
			return fmt.Errorf("put slice %d: %w", k, err)
		}
		totalBytes += int64(len(png16))
	}
	// One derivative row for the whole slice set: storage_uri is the PREFIX
	// (trailing slash) per C0.2; individual slices are served by /slice/:z.
	return w.db.UpsertDerivative(ctx, &dbmodel.AssetDerivative{
		AssetID:    a.ID,
		Kind:       dbmodel.DerivativeVolumeSlices,
		Version:    1,
		ParamsHash: paramsHash("vs-png16-v1"),
		// #1 用 store.URIForKey 反推 URI(而非硬编码 local://)——MinIO 生产下 PutAt 返回
		// minio://,写死 local:// 会让切片端点/ SAM2 全都读不到(§5f 的 key≠URI 事故类)。
		StorageURI: w.store.URIForKey(prefix),
		Status:     "ready",
		SizeBytes:  totalBytes,
	})
}

// gunzipIfNeeded transparently decompresses a gzip-wrapped .nii.gz.
func gunzipIfNeeded(b []byte) ([]byte, error) {
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("gunzip volume: %w", err)
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, maxVolumeBytes+1))
		if err != nil {
			return nil, fmt.Errorf("gunzip read: %w", err)
		}
		if int64(len(out)) > maxVolumeBytes {
			return nil, fmt.Errorf("gunzip: 解压超过 %d GB 上限(疑似 gzip bomb)", maxVolumeBytes>>30)
		}
		return out, nil
	}
	return b, nil
}

type niftiDT struct {
	code  int16
	bytes int
}

func datatypeByName(name string) (niftiDT, bool) {
	for code, v := range niftiDatatypeNames {
		if v.name == name {
			return niftiDT{code: code, bytes: v.bytes}, true
		}
	}
	return niftiDT{}, false
}

// sliceEncoder returns the PNG16 pixel↔real mapping and a per-voxel encoder that
// shifts signed data into unsigned uint16. Only lossless integer types are
// supported; float/int32 return ok=false (handled as a terminal failure above).
func sliceEncoder(dt niftiDT, slope, inter float64) (sSlope, sInter float64, encode func([]byte, int) uint16, ok bool) {
	switch dt.code {
	case 2: // uint8
		return slope, inter, func(b []byte, i int) uint16 { return uint16(b[i]) }, true
	case 256: // int8: shift +128
		return slope, inter - 128*slope, func(b []byte, i int) uint16 { return uint16(int16(int8(b[i])) + 128) }, true
	case 512: // uint16
		return slope, inter, func(b []byte, i int) uint16 { return binary.LittleEndian.Uint16(b[i*2:]) }, true
	case 4: // int16: shift +32768 so [-32768,32767] → [0,65535]
		return slope, inter - 32768*slope, func(b []byte, i int) uint16 {
			return uint16(int32(int16(binary.LittleEndian.Uint16(b[i*2:]))) + 32768)
		}, true
	}
	return 0, 0, nil, false
}

// encodeSlicePNG16 extracts slice k (voxel-index order, i fastest) and encodes a
// 16-bit grayscale PNG. Pixel (x,y) = voxel (i=x, j=y): the slice block is
// i-fastest then j, matching PNG's row-major with x-fastest — no transpose.
func encodeSlicePNG16(slice []byte, nx, ny int, dt niftiDT, encode func([]byte, int) uint16) ([]byte, error) {
	img := image.NewGray16(image.Rect(0, 0, nx, ny))
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			img.SetGray16(x, y, color.Gray16{Y: encode(slice, x+y*nx)})
		}
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// float64FromF32 reads a little-endian float32 at off and widens it.
func float64FromF32(raw []byte, off int) float64 {
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4])))
}
