//go:build purego

package purego

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestClampWindowSize(t *testing.T) {
	cases := []struct {
		in   fyne.Size
		want fyne.Size
	}{
		{fyne.NewSize(320, 240), fyne.NewSize(320, 240)},
		{fyne.NewSize(0, 0), fyne.NewSize(1, 1)},
		{fyne.NewSize(-2, -2), fyne.NewSize(1, 1)},
		{fyne.NewSize(65534, 65534), fyne.NewSize(maxWindowDim, maxWindowDim)},
	}
	for _, c := range cases {
		if got := clampWindowSize(c.in); got != c.want {
			t.Errorf("clampWindowSize(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
