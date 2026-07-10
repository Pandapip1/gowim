package wim

import "fmt"

// Add creates or replaces a file at path within the tree rooted at d,
// creating any intervening directory entries that do not already exist. This
// is the generalized, in-package form of placeFile in driver/install.go.
//
// Add takes a Hash, not raw bytes: this package's job is the container
// structure (the dentry tree), not resource compression or blob placement.
// The caller is responsible for getting the actual content bytes into a
// BlobTable/output file under this same hash - exactly as driver.Install
// already does today, returning []NewBlob for the caller to place.
//
// If path already exists as a directory, Add returns an error. If path
// already exists as a file, its unnamed stream's hash is replaced (any named
// alternate data streams are dropped, matching placeFile's behavior). Add
// returns the created or updated leaf entry.
func (d *DirEntry) Add(path string, hash Hash) (*DirEntry, error) {
	components := splitPath(path)
	if len(components) == 0 {
		return nil, fmt.Errorf("wim: add: empty path")
	}

	cur := d
	for _, name := range components[:len(components)-1] {
		child := cur.Child(name)
		if child == nil {
			child = &DirEntry{
				Attributes: FileAttributeDirectory,
				SecurityID: SecurityIDNone,
				Name:       stringToUTF16LE(name),
			}
			cur.Children = append(cur.Children, child)
		} else if !child.IsDirectory() {
			return nil, fmt.Errorf("wim: add %q: path component %q already exists as a non-directory", path, name)
		}
		cur = child
	}

	leafName := components[len(components)-1]
	if leaf := cur.Child(leafName); leaf != nil {
		if leaf.IsDirectory() {
			return nil, fmt.Errorf("wim: add %q: already exists as a directory", path)
		}
		leaf.Streams = []Stream{{Hash: hash}}
		return leaf, nil
	}

	leaf := &DirEntry{
		SecurityID: SecurityIDNone,
		Name:       stringToUTF16LE(leafName),
		Streams:    []Stream{{Hash: hash}},
	}
	cur.Children = append(cur.Children, leaf)
	return leaf, nil
}
