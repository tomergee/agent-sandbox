package install

// DiffAppliedSets returns the object keys present in prior but absent from
// current — objects that a version upgrade removed from the manifest and
// that must therefore be deleted from the cluster.
func DiffAppliedSets(prior, current []string) []string {
	currentSet := make(map[string]struct{}, len(current))
	for _, key := range current {
		currentSet[key] = struct{}{}
	}
	var removed []string
	for _, key := range prior {
		if _, ok := currentSet[key]; !ok {
			removed = append(removed, key)
		}
	}
	return removed
}
