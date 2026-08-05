package service

import "testing"

func TestNormalizeRotation(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 0}, {90, 90}, {180, 180}, {270, 270},
		{-90, 270}, {-180, 180}, {360, 0}, {450, 90}, {720, 0},
		// snapping odd metadata to the nearest quarter turn
		{44, 0}, {46, 90}, {89, 90}, {134, 90}, {135, 180},
		{224, 180}, {225, 270}, {314, 270}, {315, 0}, {359, 0},
	}
	for _, c := range cases {
		if got := normalizeRotation(c.in); got != c.want {
			t.Errorf("normalizeRotation(%d)=%d want %d", c.in, got, c.want)
		}
	}
}
