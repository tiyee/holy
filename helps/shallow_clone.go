package helps

// ShallowClone allocates a new value and copies src without reflection.
// It returns nil for a nil source. Slices, maps and pointer fields still share
// their referenced data; this is not a deep copy.
func ShallowClone[T any](src *T) *T {
	if src == nil {
		return nil
	}
	dst := new(T)
	*dst = *src
	return dst
}
