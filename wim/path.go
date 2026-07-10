package wim

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned (wrapped, so callers should use errors.Is) by
// Lookup, ReadFile, Remove, Rename, and ReadDir when a path does not resolve
// to an existing entry. This mirrors the service package's ErrNotFound
// convention (see service.ErrNotFound): a typo'd/missing path is a hard
// failure rather than a silent no-op or nil result.
var ErrNotFound = errors.New("wim: path not found")

// splitPath splits a path into its non-empty components, accepting both
// '/'- and '\\'-separated input (WIM paths are conventionally
// backslash-separated, matching Windows namespace conventions, but this
// package accepts either so callers don't have to normalize first). Leading,
// trailing, and repeated separators are ignored, so "", "/", and "\\" all
// yield an empty component list (referring to the entry itself).
func splitPath(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Child looks up a direct child of d by name, using Windows' case-insensitive
// namespace (mirroring findChildFold in driver/install.go, generalized here so
// callers throughout the module can share one implementation). It returns nil
// if no child matches.
func (d *DirEntry) Child(name string) *DirEntry {
	for _, c := range d.Children {
		if strings.EqualFold(c.NameUTF8(), name) {
			return c
		}
	}
	return nil
}

// Lookup resolves a '/'- or '\\'-separated path against d, treating d as the
// root of the walk. An empty path (or one consisting only of separators)
// returns d itself. It returns an error wrapping ErrNotFound (checkable with
// errors.Is) if any path component does not exist, or if a non-leaf component
// names something other than a directory.
func (d *DirEntry) Lookup(path string) (*DirEntry, error) {
	cur := d
	components := splitPath(path)
	for i, name := range components {
		if !cur.IsDirectory() {
			return nil, fmt.Errorf("wim: lookup %q: %q is not a directory: %w",
				path, strings.Join(components[:i], "/"), ErrNotFound)
		}
		child := cur.Child(name)
		if child == nil {
			return nil, fmt.Errorf("wim: lookup %q: no such entry %q: %w", path, name, ErrNotFound)
		}
		cur = child
	}
	return cur, nil
}
