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
	base := m.Security.EncodedLen()
	offsets, treeLen := assignSubdirOffsets(m.Root, base)
	total := base + treeLen

	// Grow dst once to its final exact length, so both the security data
	// and the dentry tree are written directly into final position instead
	// of through throwaway intermediates that get copied a second time.
	if need := len(dst) + int(total); cap(dst) < need {
		grown := make([]byte, len(dst), need)
		copy(grown, dst)
		dst = grown
	}

	dst = m.Security.AppendTo(dst)
	var err error
	dst, err = appendDirEntryTreeWithOffsets(dst, m.Root, offsets)
	if err != nil {
		return dst, err
	}
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
	offsets, _ := assignSubdirOffsets(root, base)
	return appendDirEntryTreeWithOffsets(dst, root, offsets)
}

// appendDirEntryTreeWithOffsets serializes the dentry tree rooted at root
// using precomputed (already-biased) subdir offsets, as produced by
// assignSubdirOffsets. Factored out of appendDirEntryTreeBased so callers that
// already need the offsets map (e.g. to size a destination buffer up front)
// don't have to recompute it.
func appendDirEntryTreeWithOffsets(dst []byte, root *DirEntry, offsets map[*DirEntry]uint64) ([]byte, error) {
	if root == nil {
		return dst, fmt.Errorf("wim: cannot serialize a nil dentry tree root")
	}

	var err error
	// Root dentry + its end-of-directory marker.
	if dst, err = root.appendDentry(dst, offsets[root]); err != nil {
		return dst, err
	}
	dst = append(dst, make([]byte, 8)...)

	// Pre-order walk writing each directory's children.
	var walk func(dir *DirEntry) error
	walk = func(dir *DirEntry) error {
		if dir.IsDirectory() && offsets[dir] != 0 {
			for _, c := range dir.Children {
				if dst, err = c.appendDentry(dst, offsets[c]); err != nil {
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
