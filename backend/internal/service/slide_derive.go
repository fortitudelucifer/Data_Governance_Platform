package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// WSI derivation (C1.1b): parse the slide header → persist `slide_meta`.
//
// # Why this never reads the whole file
//
// A real whole-slide image is 1–10 GB. `deriveVolume` can afford io.ReadAll
// (a NIfTI volume is tens of MB); doing the same here would blow the worker's
// memory on the first real slide — and it would do so only in production, on
// the biggest customer file, long after tests passed on a 1.9 MB sample.
//
// So the probe takes an io.ReadSeeker and walks the TIFF IFD chain, reading a
// few KB of headers. Both object-store drivers return seekable readers
// (local → *os.File, MinIO → *minio.Object over HTTP Range), so this is a real
// seek, not a download-then-seek. When a driver ever returns a non-seekable
// stream we fall back to buffering **with an explicit cap** rather than
// silently pulling gigabytes.
const slideProbeFallbackCap = 64 << 20 // 64 MiB — enough for any IFD chain

// deriveSlide produces the slide_meta derivative for a WSI asset.
func (w *MediaWorker) deriveSlide(ctx context.Context, a *dbmodel.Asset) error {
	rc, err := w.store.Get(ctx, a.StorageURI)
	if err != nil {
		return fmt.Errorf("fetch slide: %w", err)
	}
	defer rc.Close()

	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		// Non-seekable driver: buffer a bounded prefix. The IFD chain of every
		// real slide lives far below this cap; if a file needs more, that's a
		// signal worth failing on rather than an excuse to read 10 GB.
		buf, rerr := io.ReadAll(io.LimitReader(rc, slideProbeFallbackCap))
		if rerr != nil {
			return fmt.Errorf("read slide head: %w", rerr)
		}
		rs = bytes.NewReader(buf)
	}

	meta, err := ParseSlideMeta(rs)
	if err != nil {
		// A file that isn't a pyramidal slide is a terminal failure, not a retry
		// — retrying will not make it parse (same rule as deriveVolume).
		return terminalf("无法解析为病理全切片（金字塔 TIFF）：%v", err)
	}

	// ⚠️ mpp 缺失是**合法但危险**的状态：没有它就无法把像素换算成 µm。
	// 不在这里编一个默认值（C1.1a 的核心纪律），而是原样落库并记一条日志——
	// 前端据此隐藏物理单位，而不是显示一个错的数。
	if meta.MPP <= 0 {
		slog.Warn("slide has no MPP; physical units must stay hidden downstream",
			"asset_id", a.ID, "vendor", meta.Vendor)
	}

	blob, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal slide_meta: %w", err)
	}
	if err := w.putDerivative(ctx, a, dbmodel.DerivativeSlideMeta, paramsHash("sm-v1"), "application/json", blob); err != nil {
		return err
	}

	// Level-0 pixel dimensions go on the asset row: that is the coordinate space
	// every annotation is stored in (C1.2 renders scaled, geometry stays level 0).
	// Without them the viewer has no canvas size and geometry cannot be placed.
	wpx, hpx := meta.Width, meta.Height
	if err := w.db.UpdateAssetMediaMeta(ctx, a.ID, nil, nil, nil, &wpx, &hpx); err != nil {
		return fmt.Errorf("update slide dimensions: %w", err)
	}
	return nil
}
