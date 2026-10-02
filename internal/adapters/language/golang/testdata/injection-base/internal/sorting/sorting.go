package sorting

import "sort"

func Stable(names []string) {
	sort.SliceStable(names, func(i, j int) bool { return names[i] < names[j] })
}
