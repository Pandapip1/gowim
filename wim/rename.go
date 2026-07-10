package wim

import (
	"fmt"
	"strings"
)

// containsEntry reports whether target is root itself or appears anywhere in
// root's subtree. It is used by Rename to reject moving a directory into its
// own subtree, which would otherwise create a cycle.
func containsEntry(root, target *DirEntry) bool {
	if root == target {
		return true
	}
	for _, c := range root.Children {
		if containsEntry(c, target) {
			return true
		}
	}
	return false
}

// Rename moves the entry at oldPath to newPath within the tree rooted at d,
// creating any intervening directory entries for newPath's parent that do not
// already exist (as Add does). Both paths are resolved from d.
//
// Rename returns an error wrapping ErrNotFound if oldPath does not resolve,
// and a plain error if: a non-leaf component of either path names something
// other than a directory, newPath already exists, or oldPath names a
// directory and newPath would place it inside its own subtree.
//
// Unlike a POSIX rename(2), Rename never silently replaces an existing
// destination - this package's tree is an in-memory structure the caller
// fully controls, so an ambiguous overwrite is rejected rather than guessed
// at; a caller that wants replace-semantics can call Remove(newPath) first
// (ignoring ErrNotFound) and then Rename.
func (d *DirEntry) Rename(oldPath, newPath string) error {
	oldComponents := splitPath(oldPath)
	if len(oldComponents) == 0 {
		return fmt.Errorf("wim: rename: empty source path")
	}
	newComponents := splitPath(newPath)
	if len(newComponents) == 0 {
		return fmt.Errorf("wim: rename: empty destination path")
	}

	oldParent := d
	for _, name := range oldComponents[:len(oldComponents)-1] {
		child := oldParent.Child(name)
		if child == nil {
			return fmt.Errorf("wim: rename %q: %w", oldPath, ErrNotFound)
		}
		oldParent = child
	}
	oldName := oldComponents[len(oldComponents)-1]
	idx := -1
	for i, c := range oldParent.Children {
		if strings.EqualFold(c.NameUTF8(), oldName) {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("wim: rename %q: %w", oldPath, ErrNotFound)
	}
	entry := oldParent.Children[idx]

	// Reject moving a directory into its own subtree. Since newParent's
	// intervening directories may not exist yet, this checks the cheaper
	// necessary condition that newComponents' path string is not prefixed by
	// oldComponents; it is a heuristic on path text, not tree identity, but
	// suffices given both paths are resolved from the same root d.
	if entry.IsDirectory() && len(newComponents) >= len(oldComponents) {
		isPrefix := true
		for i := range oldComponents {
			if !strings.EqualFold(oldComponents[i], newComponents[i]) {
				isPrefix = false
				break
			}
		}
		if isPrefix {
			return fmt.Errorf("wim: rename %q to %q: cannot move a directory into its own subtree", oldPath, newPath)
		}
	}

	newParent := d
	for _, name := range newComponents[:len(newComponents)-1] {
		child := newParent.Child(name)
		if child == nil {
			child = &DirEntry{
				Attributes: FileAttributeDirectory,
				SecurityID: SecurityIDNone,
				Name:       stringToUTF16LE(name),
			}
			newParent.Children = append(newParent.Children, child)
		} else if !child.IsDirectory() {
			return fmt.Errorf("wim: rename %q: path component %q already exists as a non-directory", newPath, name)
		}
		newParent = child
	}
	if containsEntry(entry, newParent) {
		// Belt-and-suspenders check now that newParent is fully resolved.
		return fmt.Errorf("wim: rename %q to %q: cannot move a directory into its own subtree", oldPath, newPath)
	}
	newName := newComponents[len(newComponents)-1]
	if newParent.Child(newName) != nil {
		return fmt.Errorf("wim: rename %q to %q: destination already exists", oldPath, newPath)
	}

	oldParent.Children = append(oldParent.Children[:idx:idx], oldParent.Children[idx+1:]...)
	entry.Name = stringToUTF16LE(newName)
	newParent.Children = append(newParent.Children, entry)
	return nil
}

// ReadDir returns the children of the directory at path, resolved from d. It
// returns an error wrapping ErrNotFound if path does not resolve, and a plain
// error if path resolves to a non-directory entry.
//
// This is a thin convenience wrapper around Lookup + Children; it exists
// mainly so callers get an explicit "not a directory" error instead of having
// to remember to check IsDirectory() themselves before reading Children.
func (d *DirEntry) ReadDir(path string) ([]*DirEntry, error) {
	entry, err := d.Lookup(path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDirectory() {
		return nil, fmt.Errorf("wim: read dir %q: not a directory", path)
	}
	return entry.Children, nil
}
