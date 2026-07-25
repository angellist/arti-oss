package blob

import "sort"

func sortObjectInfoByKey(xs []ObjectInfo) {
	sort.Slice(xs, func(i, j int) bool { return xs[i].Key < xs[j].Key })
}
