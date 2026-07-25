package artifacts

import "testing"

// clampBrowsePageSize must never let the value used to compute Offset exceed
// pgstore.BrowseAggregates' own no-op threshold (>500 resets Limit to 50) —
// otherwise a client requesting a large page_size gets an Offset computed
// from the raw value while the actual Limit silently resets to 50, causing
// most of the intervening rows to never be returned on any page.
func TestClampBrowsePageSize(t *testing.T) {
	cases := []struct {
		in   int32
		want int32
	}{
		{in: 50, want: 50},
		{in: 500, want: 500},
		{in: 501, want: 500},
		{in: 1000, want: 500},
	}
	for _, c := range cases {
		if got := clampBrowsePageSize(c.in); got != c.want {
			t.Errorf("clampBrowsePageSize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
