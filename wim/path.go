package wim

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
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

// dirEntryChildIndex is a name->child lookup index cached on a DirEntry (see
// DirEntry.childIndex). children records exactly the Children slice this
// index was built from, so a later Child call can tell in O(1) whether the
// cache is still valid (see sameChildrenSlice) without re-scanning anything.
type dirEntryChildIndex struct {
	children []*DirEntry
	byName   map[string]*DirEntry
}

// sameChildrenSlice reports whether a and b are the same slice value (same
// backing array, same length) - not merely equal element-by-element. Every
// mutation this package makes to a DirEntry's Children (Add, Remove, Rename,
// AttachAt, and initial tree parsing) reassigns the field via `=` or
// `append`, which always yields a new slice value (append either grows into
// a new backing array or, even when it reuses one in place, changes the
// length), so comparing identity this way is a cheap, exact staleness check
// for the case this package actually needs to handle - it does not attempt
// to detect an element being mutated in place without changing the slice
// itself, since nothing in this package does that.
func sameChildrenSlice(a, b []*DirEntry) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// foldKey returns a canonical form of name such that foldKey(a) == foldKey(b)
// exactly when strings.EqualFold(a, b) - i.e. it maps every rune to the
// smallest rune in its simple case-folding orbit (the same notion of
// case-insensitivity strings.EqualFold itself uses internally). This lets
// buildChildIndex key its map by foldKey and get precisely the same matching
// behavior Child had before it was indexed (including exotic Unicode cases
// EqualFold handles specially, such as Greek sigma variants), rather than the
// looser and occasionally different-in-edge-cases strings.ToLower.
func foldKey(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		min := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < min {
				min = f
			}
		}
		b.WriteRune(min)
	}
	return b.String()
}

// buildChildIndex builds and caches a name->child index for d from its
// current Children, and returns it.
//
// This turns Child from a linear scan that re-decodes every sibling's
// UTF-16 name (allocating a new Go string via NameUTF8/utf16leToString) on
// every single comparison into a single map lookup after the first call
// following any change to Children: measured against a real Windows image's
// Windows\WinSxS\Manifests directory (28,069 entries), the old scan cost
// ~0.156ms per Child call and made component.BuildFromImage spend ~4.4s in
// this scan alone (see BenchmarkDirEntryChild in path_bench_test.go for the
// reproducible A/B numbers). Building the index still allocates one folded
// name per child, but only once per directory per Children-change, not once
// per child per lookup - the actual hot cost this was fixing.
//
// It is safe to call concurrently with other buildChildIndex/Child calls on
// the same d, provided nothing is concurrently mutating d.Children (see the
// childIndex field doc comment): every call only reads Children and produces
// its own local map before publishing it with a single atomic Store, so a
// race between two callers building redundant indices from the same
// Children snapshot wastes work but never corrupts anything or requires a
// lock.
func (d *DirEntry) buildChildIndex() *dirEntryChildIndex {
	children := d.Children
	byName := make(map[string]*DirEntry, len(children))
	for _, c := range children {
		k := foldKey(c.NameUTF8())
		if _, exists := byName[k]; !exists {
			// First match wins, matching the old linear scan's behavior on
			// the (Windows-namespace-violating, but not impossible in an
			// arbitrary in-memory tree this package doesn't itself enforce
			// uniqueness on) case where two siblings' names differ only by
			// case.
			byName[k] = c
		}
	}
	idx := &dirEntryChildIndex{children: children, byName: byName}
	d.childIndex.Store(idx)
	return idx
}

// Child looks up a direct child of d by name, using Windows' case-insensitive
// namespace (mirroring findChildFold in driver/install.go, generalized here so
// callers throughout the module can share one implementation). It returns nil
// if no child matches.
func (d *DirEntry) Child(name string) *DirEntry {
	idx := d.childIndex.Load()
	if idx == nil || !sameChildrenSlice(idx.children, d.Children) {
		idx = d.buildChildIndex()
	}
	return idx.byName[foldKey(name)]
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
