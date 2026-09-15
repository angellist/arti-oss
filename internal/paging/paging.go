// Package paging holds the page bounds every arti list endpoint shares.
package paging

const (
	// Default is the page size for a caller that asks for none.
	Default = 50
	// Max is the largest page any list endpoint returns.
	Max = 500
)

// ClampLimit bounds a requested page size, returning Default for an unset one.
// A request above Max is clamped down to Max rather than falling back to
// Default, so asking for more rows can never return fewer.
func ClampLimit[T ~int | ~int32 | ~int64](n T) T {
	switch {
	case n <= 0:
		return Default
	case n > Max:
		return Max
	default:
		return n
	}
}
