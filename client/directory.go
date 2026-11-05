// Directory-tree listing support added in this fork.
package client

import (
	"path"
	"strings"
)

// directoryWithin compares upstream paths without consulting the bridge host's
// filesystem. Component boundaries exclude siblings such as /projects-other.
// This is a listing predicate; mutation ownership remains exact-directory.
func directoryWithin(root, directory string) bool {
	if root == "" || directory == "" {
		return false
	}
	normalize := func(value string) string {
		return path.Clean(strings.ReplaceAll(value, `\`, "/"))
	}
	root, directory = normalize(root), normalize(directory)
	return directory == root || strings.HasPrefix(directory, strings.TrimRight(root, "/")+"/")
}
