package wim

import "fmt"

// ImageMetadata is the decoded contents of one image's metadata resource: the
// per-image security-descriptor table plus the root of the directory-entry
// tree.
//
// The on-disk metadata resource is laid out as the security data followed
// immediately (at security-data total length) by the root directory entry,
// then all other entries; each directory's subdir offset is measured from the
// start of the metadata resource.
type ImageMetadata struct {
	Security *SecurityData
	Root     *DirEntry
}

// ParseImageMetadata decodes an image's metadata resource from its
// (uncompressed) bytes.
func ParseImageMetadata(buf []byte) (*ImageMetadata, error) {
	sd, sdLen, err := ParseSecurityData(buf)
	if err != nil {
		return nil, err
	}
	root, err := ParseDirEntryTree(buf, sdLen)
	if err != nil {
		return nil, err
	}
	return &ImageMetadata{Security: sd, Root: root}, nil
}

// AppendTo serializes the metadata resource, appending the security data
// followed by the directory-entry tree to dst.
//
// Because directory subdir offsets are measured from the start of the metadata
// resource, the tree is serialized with every non-zero subdir offset biased by
// the security-data length, so the tree sits correctly after the security data.
func (m *ImageMetadata) AppendTo(dst []byte) ([]byte, error) {
	if m.Security == nil {
		m.Security = &SecurityData{}
	}
	if m.Root == nil {
		return dst, fmt.Errorf("wim: image metadata has no root directory entry")
	}

	// Security data first; its length is where the dentry region begins.
	sdBytes := m.Security.AppendTo(nil)
	base := uint64(len(sdBytes))

	treeBytes, err := appendDirEntryTreeBased(nil, m.Root, base)
	if err != nil {
		return dst, err
	}

	dst = append(dst, sdBytes...)
	dst = append(dst, treeBytes...)
	return dst, nil
}

// appendDirEntryTreeBased serializes the dentry tree rooted at root, adding
// 'base' to every non-zero subdir offset so the tree can be placed at byte
// offset 'base' within the metadata resource. With base == 0 it produces a
// self-relative tree (see AppendDirEntryTree).
//
// The layout matches write_dentry_tree: the root dentry, an end-of-directory
// marker, then a pre-order walk emitting each directory's children followed by
// that directory's end-of-directory marker.
func appendDirEntryTreeBased(dst []byte, root *DirEntry, base uint64) ([]byte, error) {
	if root == nil {
		return dst, fmt.Errorf("wim: cannot serialize a nil dentry tree root")
	}
	offsets, _ := assignSubdirOffsets(root)
	biased := make(map[*DirEntry]uint64, len(offsets))
	for d, off := range offsets {
		if off != 0 {
			biased[d] = off + base
		} else {
			biased[d] = 0
		}
	}

	var err error
	// Root dentry + its end-of-directory marker.
	if dst, err = root.appendDentry(dst, biased[root]); err != nil {
		return dst, err
	}
	dst = append(dst, make([]byte, 8)...)

	// Pre-order walk writing each directory's children.
	var walk func(dir *DirEntry) error
	walk = func(dir *DirEntry) error {
		if dir.IsDirectory() && biased[dir] != 0 {
			for _, c := range dir.Children {
				if dst, err = c.appendDentry(dst, biased[c]); err != nil {
					return err
				}
			}
			dst = append(dst, make([]byte, 8)...)
		}
		for _, c := range dir.Children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return dst, err
	}
	return dst, nil
}
