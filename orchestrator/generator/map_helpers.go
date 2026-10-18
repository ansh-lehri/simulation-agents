package generator

import "sort"

// sortedStringKeys returns the keys of a string-keyed map in sorted
// order. Every map iteration in the trait-draw engine goes through this
// helper because Go's native map iteration is deliberately randomized,
// and a randomized iteration would silently break the standing
// reproducibility invariant: a fixed seed must replay an identical trait
// vector.
//
// Generic over the value type so one implementation covers every
// string-keyed map the draw iterates — method anchors, perception
// ranges, bias tilt strengths, trait channel sets, exit-mode anchors.
func sortedStringKeys[V any](mapping map[string]V) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
