package service

// 切片传输格式的**跨语言锁**(C3.2b)。testdata/volume/gray16_ramp.png 是由
// 生产编码器 encodeSlicePNG16 产出的 golden 夹具:
//   · 本测试断言 Go 编码器仍能逐字节重现它;
//   · frontend/src/lib/png16.test.ts 断言 TS 解码器从同一文件读出同一批 16-bit 值。
// 两端锁在同一份字节上——编码器改了形状、或解码器读错字节序/滤波,任一端都会红。
// 这是 testdata/interpolation 那套双端夹具做法在"切片传输"上的复用。
//
// 夹具像素刻意选得能戳破两类静默错误:
//   · 全部 >255 或含 0/65535 → 8-bit 截断当场暴露;
//   · 258(0x0102)与 513(0x0201)是彼此的字节交换 → 字节序读反当场暴露。

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// gray16FixtureValues 是 ramp 夹具的像素值(行优先,x 最快),与 TS 侧同一批。
var gray16FixtureValues = []uint16{1000, 6000, 11000, 16000, 258, 513, 32768, 40000, 65535, 0, 4660, 43981}

// filtersFixtureValue 是 filters 夹具(16×16)的生成式,与 TS 侧同一份。
// 分段刻意让 Go 的按行择优编码器挑中不同 filter(常量行→Up、水平斜坡→Sub、
// 二维渐变→Paeth、伪随机→None),两份夹具合起来覆盖全部 5 种 filter——
// 少覆盖一种,TS 那一侧对应的去滤波分支就在裸奔(解出"像图但像素全错")。
func filtersFixtureValue(x, y int) uint16 {
	switch {
	case y < 4:
		return uint16(3000 + y*7)
	case y < 8:
		return uint16(500 + x*400)
	case y < 12:
		return uint16(1000 + x*300 + y*250)
	default:
		return uint16((x*7919 + y*104729) % 65536)
	}
}

func encodeGray16(t *testing.T, vals []uint16, nx, ny int) []byte {
	t.Helper()
	buf := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	dt, ok := datatypeByName("uint16")
	if !ok {
		t.Fatal("uint16 datatype missing")
	}
	_, _, enc, ok := sliceEncoder(dt, 1, 0)
	if !ok {
		t.Fatal("uint16 slice encoder missing")
	}
	got, err := encodeSlicePNG16(buf, nx, ny, dt, enc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return got
}

func assertFixture(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "volume", name)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v (夹具是跨语言锁的一半，不能缺)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: PNG16 编码结果与夹具不一致(%d vs %d 字节)——编码器变了形状。"+
			"若确属有意变更，重新生成夹具并同步 frontend/src/lib/png16.test.ts", name, len(got), len(want))
	}
}

func TestGray16SliceFixtureMatchesEncoder(t *testing.T) {
	assertFixture(t, "gray16_ramp.png", encodeGray16(t, gray16FixtureValues, 4, 3))
}

func TestGray16FilterFixtureMatchesEncoder(t *testing.T) {
	const nx, ny = 16, 16
	vals := make([]uint16, nx*ny)
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			vals[x+y*nx] = filtersFixtureValue(x, y)
		}
	}
	assertFixture(t, "gray16_filters.png", encodeGray16(t, vals, nx, ny))
}
