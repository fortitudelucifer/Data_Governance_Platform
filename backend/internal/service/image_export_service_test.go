package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makeMaskPNG builds an NRGBA PNG where paintFn(x,y)=true → red opaque pixel.
func makeMaskPNG(w, h int, paintFn func(x, y int) bool) string {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			if paintFn(x, y) {
				img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestMaskToCocoRLE_2x2Block: 4×4 image, 2×2 red block at columns/rows 1-2.
// Column-major sequence: [0,0,0,0, 0,1,1,0, 0,1,1,0, 0,0,0,0]
// RLE: 5 zeros, 2 ones, 2 zeros, 2 ones, 5 zeros → [5,2,2,2,5]
func TestMaskToCocoRLE_2x2Block(t *testing.T) {
	b64 := makeMaskPNG(4, 4, func(x, y int) bool {
		return x >= 1 && x <= 2 && y >= 1 && y <= 2
	})
	rle, err := maskToCocoRLE(b64, 4, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	size := rle["size"].([]int)
	if size[0] != 4 || size[1] != 4 {
		t.Errorf("size: got %v, want [4 4]", size)
	}
	counts := rle["counts"].([]int)
	want := []int{5, 2, 2, 2, 5}
	if len(counts) != len(want) {
		t.Fatalf("counts len: got %d want %d (counts=%v)", len(counts), len(want), counts)
	}
	for i, v := range want {
		if counts[i] != v {
			t.Errorf("counts[%d]: got %d want %d", i, counts[i], v)
		}
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	if total != 16 {
		t.Errorf("total pixels: got %d want 16", total)
	}
}

// TestMaskToCocoRLE_AllOff: all pixels transparent → single run of zeros.
func TestMaskToCocoRLE_AllOff(t *testing.T) {
	b64 := makeMaskPNG(3, 3, func(x, y int) bool { return false })
	rle, err := maskToCocoRLE(b64, 3, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	counts := rle["counts"].([]int)
	if len(counts) != 1 || counts[0] != 9 {
		t.Errorf("all-off: got %v want [9]", counts)
	}
}

// TestMaskToCocoRLE_AllOn: all pixels painted → [0, N] (zero leading zeros).
func TestMaskToCocoRLE_AllOn(t *testing.T) {
	b64 := makeMaskPNG(3, 3, func(x, y int) bool { return true })
	rle, err := maskToCocoRLE(b64, 3, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	counts := rle["counts"].([]int)
	if len(counts) != 2 || counts[0] != 0 || counts[1] != 9 {
		t.Errorf("all-on: got %v want [0 9]", counts)
	}
}

// TestMaskToCocoRLE_FallbackDims: imgW=imgH=0 → dimensions inferred from PNG bounds.
// 2×2 image, only (0,0) painted.
// Column-major: (x=0,y=0)=1, (x=0,y=1)=0, (x=1,y=0)=0, (x=1,y=1)=0
// Expected counts: [0, 1, 3]
func TestMaskToCocoRLE_FallbackDims(t *testing.T) {
	b64 := makeMaskPNG(2, 2, func(x, y int) bool { return x == 0 && y == 0 })
	rle, err := maskToCocoRLE(b64, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	size := rle["size"].([]int)
	if size[0] != 2 || size[1] != 2 {
		t.Errorf("size: got %v want [2 2]", size)
	}
	counts := rle["counts"].([]int)
	want := []int{0, 1, 3}
	if len(counts) != len(want) {
		t.Fatalf("counts len: got %d want %d (counts=%v)", len(counts), len(want), counts)
	}
	for i, v := range want {
		if counts[i] != v {
			t.Errorf("counts[%d]: got %d want %d", i, counts[i], v)
		}
	}
}

// TestMaskToCocoRLE_InvalidBase64: malformed input must return an error.
func TestMaskToCocoRLE_InvalidBase64(t *testing.T) {
	_, err := maskToCocoRLE("not-valid-base64!!", 4, 4)
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}
