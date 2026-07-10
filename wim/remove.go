package wim

import (
	"fmt"
	"strings"
)

// Remove deletes the entry at path from the tree rooted at d, unlinking it
// (and, if it names a directory, its entire subtree) from its parent's
// Children. It returns an error wrapping ErrNotFound if path does not
// resolve.
//
// Remove does not adjust any BlobTable's reference counts for streams that
// become unreferenced: this package's job is the dentry tree structure, not
// blob-table bookkeeping, exactly as it already leaves capture/apply and
// compression to the caller (see the wim package doc). A caller that cares can
// walk the removed subtree's Streams (e.g. via the returned removed entry, or
// by looking it up before calling Remove) and decide what to do with the
// corresponding BlobTable entries itself.
func (d *DirEntry) Remove(path string) error {
	components := splitPath(path)
	if len(components) == 0 {
		return fmt.Errorf("wim: remove: empty path")
	}

	parent := d
	for _, name := range components[:len(components)-1] {
		child := parent.Child(name)
		if child == nil {
			return fmt.Errorf("wim: remove %q: %w", path, ErrNotFound)
		}
		parent = child
	}

	leafName := components[len(components)-1]
	for i, c := range parent.Children {
		if strings.EqualFold(c.NameUTF8(), leafName) {
			parent.Children = append(parent.Children[:i:i], parent.Children[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("wim: remove %q: %w", path, ErrNotFound)
}
