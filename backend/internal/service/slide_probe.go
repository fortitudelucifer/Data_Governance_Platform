package service

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WSI (whole-slide image) probe — C1.1a. Mirrors volume_probe.go's role for
// NIfTI: read geometry + scale out of the file itself, in pure Go, no cgo and
// no external binary.
//
// # Why parse TIFF ourselves instead of binding OpenSlide
//
// Aperio .svs (and Hamamatsu .ndpi, Leica .scn) are **pyramidal TIFFs**: the
// downsampled levels are just further IFDs, and the vendor metadata lives in
// ImageDescription. Everything this probe needs is plain TIFF structure, so a
// cgo dependency on the OpenSlide native library buys nothing here — and cgo
// would make the build need a C toolchain + native lib on every dev machine
// and CI runner. Tile *decoding* is a different story (JPEG/JPEG2000 per tile);
// that's C1.2's problem, not the probe's.
//
// # Two things that would silently corrupt downstream work
//
//  1. **BigTIFF.** Real slides are gigabytes and are usually BigTIFF (magic 43,
//     8-byte offsets), while small samples are classic TIFF (magic 42, 4-byte).
//     Reading a BigTIFF with classic offsets yields garbage IFDs — and garbage
//     that often *parses*, producing plausible-looking wrong dimensions. Both
//     are handled here and both are covered by tests.
//  2. **Missing mpp.** MPP (microns per pixel) is what turns pixels into µm for
//     the C1.35 ruler. Not every slide carries it. It is therefore reported as
//     **0 = unknown** and never defaulted to 1.0: a silent 1.0 would make every
//     measurement wrong by the scan factor while looking completely normal —
//     the exact failure mode C3.25 fought in the 3D measurement tool.
type SlideMeta struct {
	// Width/Height of level 0 (full resolution), in pixels.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Levels is the resolution pyramid, level 0 first.
	Levels []SlideLevel `json:"levels"`
	// MPP is microns per pixel at level 0. **0 means unknown** — callers must
	// refuse to show physical units rather than assume a value.
	MPP float64 `json:"mpp"`
	// Magnification is the scanner's stated objective power (e.g. 20, 40).
	// 0 = unknown.
	Magnification float64 `json:"magnification"`
	Vendor        string  `json:"vendor"`
	// Associated images (thumbnail / label / macro) that are NOT pyramid levels.
	Associated []string `json:"associated,omitempty"`
	// HasLabelImage flags that the file carries a label/macro photo. On clinical
	// slides those photos show the printed patient identifiers ("burned-in" PHI,
	// PS3.15). The stripping mechanism is StripAssociatedImages (slide_strip.go,
	// D-1b：改链 + 抹像素,金字塔逐字节不动);它是 DeidPipeline 的 WSI 臂,随临床
	// 上线接入(公开去标识数据的 label 本就匿名,不必剥)。
	HasLabelImage bool `json:"has_label_image"`
}

// SlideLevel is one pyramid level.
type SlideLevel struct {
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	TileWidth  int     `json:"tile_width"`
	TileHeight int     `json:"tile_height"`
	// Downsample is level0Width / levelWidth (≈1, 4, 16 …). Vendors do not
	// guarantee powers of two, so viewers must use this rather than assume 2^n.
	Downsample float64 `json:"downsample"`
}

// TIFF tags we care about.
const (
	tagNewSubfileType = 254
	tagImageWidth     = 256
	tagImageLength    = 257
	tagImageDesc      = 270
	tagTileWidth      = 322
	tagTileLength     = 323
)

// ParseSlideMeta reads pyramid + vendor metadata from a pyramidal TIFF (SVS…).
//
// Takes an io.ReadSeeker rather than a []byte because slides are gigabytes —
// TIFF is random-access by design (IFDs are a linked list of offsets), so we
// only ever read a few kilobytes of headers.
func ParseSlideMeta(r io.ReadSeeker) (*SlideMeta, error) {
	hdr := make([]byte, 16)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("slide: read header: %w", err)
	}

	var bo binary.ByteOrder
	switch string(hdr[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("slide: not a TIFF (bad byte order %q)", hdr[0:2])
	}

	magic := bo.Uint16(hdr[2:4])
	big := magic == 43
	if magic != 42 && magic != 43 {
		return nil, fmt.Errorf("slide: not a TIFF (magic %d)", magic)
	}
	var next int64
	if big {
		// BigTIFF: offset size (must be 8) + reserved, then 8-byte first IFD offset.
		if osz := bo.Uint16(hdr[4:6]); osz != 8 {
			return nil, fmt.Errorf("slide: unsupported BigTIFF offset size %d", osz)
		}
		next = int64(bo.Uint64(hdr[8:16]))
	} else {
		next = int64(bo.Uint32(hdr[4:8]))
	}

	meta := &SlideMeta{}
	seen := 0
	for next > 0 && seen < 64 { // 64 IFDs is far past any real slide; stops loops
		seen++
		ifd, nxt, err := readIFD(r, bo, big, next)
		if err != nil {
			return nil, err
		}
		next = nxt

		w, hgt := ifd[tagImageWidth], ifd[tagImageLength]
		if w == 0 || hgt == 0 {
			continue
		}
		tw, th := ifd[tagTileWidth], ifd[tagTileLength]
		desc := ifdDesc(r, bo, big, next, ifd)

		if tw > 0 && th > 0 {
			// Tiled → a pyramid level. Non-tiled pages are the associated
			// thumbnail/label/macro images (openslide uses the same rule).
			meta.Levels = append(meta.Levels, SlideLevel{
				Width: int(w), Height: int(hgt), TileWidth: int(tw), TileHeight: int(th),
			})
			if meta.Width == 0 {
				meta.Width, meta.Height = int(w), int(hgt)
				meta.MPP, meta.Magnification, meta.Vendor = parseAperioDesc(desc)
			}
			continue
		}
		switch {
		case strings.Contains(strings.ToLower(desc), "label"):
			meta.Associated = append(meta.Associated, "label")
			meta.HasLabelImage = true
		case strings.Contains(strings.ToLower(desc), "macro"):
			meta.Associated = append(meta.Associated, "macro")
			meta.HasLabelImage = true // macro often shows the label too
		default:
			meta.Associated = append(meta.Associated, "thumbnail")
		}
	}

	if len(meta.Levels) == 0 {
		return nil, fmt.Errorf("slide: no tiled pyramid level found (not a WSI?)")
	}
	for i := range meta.Levels {
		if meta.Levels[i].Width > 0 {
			meta.Levels[i].Downsample = float64(meta.Width) / float64(meta.Levels[i].Width)
		}
	}
	return meta, nil
}

// readIFD returns tag→value for the scalar tags we need, plus the next IFD offset.
func readIFD(r io.ReadSeeker, bo binary.ByteOrder, big bool, off int64) (map[uint16]uint64, int64, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, 0, err
	}
	var n uint64
	if big {
		b := make([]byte, 8)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, 0, err
		}
		n = bo.Uint64(b)
	} else {
		b := make([]byte, 2)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, 0, err
		}
		n = uint64(bo.Uint16(b))
	}
	if n > 4096 {
		return nil, 0, fmt.Errorf("slide: implausible IFD entry count %d", n)
	}

	entrySize := 12
	if big {
		entrySize = 20
	}
	buf := make([]byte, int(n)*entrySize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, 0, err
	}
	out := make(map[uint16]uint64, 8)
	descPtr := make(map[uint16][2]uint64) // tag → (offset, count) for ImageDescription
	for i := 0; i < int(n); i++ {
		e := buf[i*entrySize:]
		tag := bo.Uint16(e[0:2])
		typ := bo.Uint16(e[2:4])
		var count, valOff uint64
		if big {
			count = bo.Uint64(e[4:12])
			valOff = bo.Uint64(e[12:20])
		} else {
			count = uint64(bo.Uint32(e[4:8]))
			valOff = uint64(bo.Uint32(e[8:12]))
		}
		if tag == tagImageDesc {
			descPtr[tag] = [2]uint64{valOff, count}
			continue
		}
		// Scalars we need are SHORT(3) or LONG(4)/LONG8(16), stored inline.
		switch typ {
		case 3: // SHORT — inline, high bytes are padding
			if big {
				out[tag] = valOff & 0xFFFF
			} else {
				out[tag] = uint64(bo.Uint16(e[8:10]))
			}
		case 4, 16:
			out[tag] = valOff
		}
	}
	// Stash the description pointer so ifdDesc can fetch it lazily.
	if p, ok := descPtr[tagImageDesc]; ok {
		out[descOffKey] = p[0]
		out[descLenKey] = p[1]
	}

	var nxt int64
	tail := make([]byte, 8)
	if big {
		if _, err := io.ReadFull(r, tail); err != nil {
			return out, 0, nil
		}
		nxt = int64(bo.Uint64(tail))
	} else {
		if _, err := io.ReadFull(r, tail[:4]); err != nil {
			return out, 0, nil
		}
		nxt = int64(bo.Uint32(tail[:4]))
	}
	return out, nxt, nil
}

// Pseudo-tags used to carry the ImageDescription location through the tag map.
const (
	descOffKey uint16 = 0xFFF0
	descLenKey uint16 = 0xFFF1
)

func ifdDesc(r io.ReadSeeker, _ binary.ByteOrder, _ bool, _ int64, ifd map[uint16]uint64) string {
	off, ok := ifd[descOffKey]
	ln := ifd[descLenKey]
	if !ok || ln == 0 || ln > 1<<20 {
		return ""
	}
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return ""
	}
	b := make([]byte, ln)
	if _, err := io.ReadFull(r, b); err != nil {
		return ""
	}
	return string(b)
}

// parseAperioDesc pulls MPP / AppMag / vendor out of the Aperio ImageDescription,
// which is a `|`-separated key=value soup after a free-form first line:
//
//	Aperio Image Library v11.2.1 \r\n 46000x32914 [...] |AppMag = 20|MPP = 0.4990|...
//
// Returns 0 for anything absent — **never a default**. See the type doc.
func parseAperioDesc(desc string) (mpp, mag float64, vendor string) {
	if desc == "" {
		return 0, 0, ""
	}
	if strings.Contains(desc, "Aperio") {
		vendor = "aperio"
	}
	for _, part := range strings.Split(desc, "|") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "MPP":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				mpp = f
			}
		case "APPMAG":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				mag = f
			}
		}
	}
	return mpp, mag, vendor
}
