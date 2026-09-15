package paging_test

import (
	"testing"

	"github.com/angellist/arti-oss/internal/paging"
)

// An oversized limit used to fall back to Default, so a client asking for 1000
// rows got 50 and no signal that it had asked for something impossible. couch's
// skill catalog read one 500-row page as the whole set for that reason.
func TestClampLimit(t *testing.T) {
	for _, tc := range []struct {
		in, want int32
	}{
		{0, paging.Default},
		{-1, paging.Default},
		{1, 1},
		{499, 499},
		{500, 500},
		{501, paging.Max},
		{100000, paging.Max},
	} {
		if got := paging.ClampLimit(tc.in); got != tc.want {
			t.Errorf("ClampLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
