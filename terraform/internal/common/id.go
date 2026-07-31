package common

import (
	"fmt"
	"strings"
)

const DefaultNamespace = "default"

// MakeID builds the canonical "namespace/name" resource ID.
func MakeID(namespace, name string) string {
	return namespace + "/" + name
}

// ParseID splits "namespace/name" (or a bare "name", which maps to the
// default namespace) into its parts.
func ParseID(id string) (namespace, name string, err error) {
	parts := strings.Split(id, "/")
	switch len(parts) {
	case 1:
		return DefaultNamespace, parts[0], nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("invalid ID %q: expected \"namespace/name\"", id)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("invalid ID %q: expected \"namespace/name\"", id)
	}
}
