package pgstore

import "testing"

// IsPackageLike must cover PACKAGE and APP (both stored as zips) and nothing
// else — the predicate that keeps APP from silently diverging from PACKAGE in
// package-behavior branches (serving files, listing the manifest, rejecting
// append-corrupts-the-zip).
func TestIsPackageLike(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		want bool
	}{
		{TypePackage, true},
		{TypeApp, true},
		{TypeText, false},
		{TypeAttachment, false},
		{"", false},
	} {
		if got := IsPackageLike(tc.typ); got != tc.want {
			t.Errorf("IsPackageLike(%q) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}
