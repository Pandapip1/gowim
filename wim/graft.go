package wim

import "fmt"

// SecurityRemapper deep-copies DirEntry subtrees from one image's tree into
// another while remapping SecurityID references down to just the
// descriptors actually used by the copied subtrees, in first-seen order --
// the security-table equivalent of RebuildBlobTable, for the same reason:
// a subtree grafted from a much larger donor image should not drag that
// donor's entire, now-mismatched-index security table along with it.
//
// Use NewSecurityRemapper, call Copy once per subtree to graft (sharing one
// remapper across every subtree from the same donor image so repeated
// SecurityID references collapse to the same output index), then call
// BuildSecurityData once, after every Copy, to get the new image's real
// SecurityData.
type SecurityRemapper struct {
	index map[int32]int32
	ids   []int32
}

// NewSecurityRemapper returns a ready-to-use remapper with no entries yet.
func NewSecurityRemapper() *SecurityRemapper {
	return &SecurityRemapper{index: make(map[int32]int32)}
}

// Copy deep-copies entry and its entire subtree, preserving every real
// field -- Attributes, the three FILETIME timestamps, ShortName, tagged
// Extra items, HardLinkGroupID, reparse fields, and Streams (not just the
// unnamed stream's hash, which is all a path-based DirEntry.Add call would
// leave populated, defaulting everything else to zero/absent). A real WIM
// consumer can plausibly depend on some of these being real rather than
// zero -- e.g. Attributes of exactly 0 isn't even a legal file-attribute
// value -- so Copy preserves them defensively rather than only fields
// already proven load-bearing.
//
// Each copy's SecurityID is remapped through r: the first time a given
// source SecurityID (other than SecurityIDNone) is seen across any Copy
// call on this remapper, it is recorded and assigned the next output index;
// a repeated reference to the same source SecurityID (in this or a later
// Copy call) reuses that same output index.
func (r *SecurityRemapper) Copy(entry *DirEntry) *DirEntry {
	cp := &DirEntry{
		Attributes:      entry.Attributes,
		SecurityID:      SecurityIDNone,
		CreationTime:    entry.CreationTime,
		LastAccessTime:  entry.LastAccessTime,
		LastWriteTime:   entry.LastWriteTime,
		Unknown0x54:     entry.Unknown0x54,
		ReparseTag:      entry.ReparseTag,
		ReparseReserved: entry.ReparseReserved,
		ReparseFlags:    entry.ReparseFlags,
		HardLinkGroupID: entry.HardLinkGroupID,
		Name:            entry.Name,
		ShortName:       entry.ShortName,
		Streams:         entry.Streams,
		Extra:           entry.Extra,
	}
	if entry.SecurityID != SecurityIDNone {
		if remapped, ok := r.index[entry.SecurityID]; ok {
			cp.SecurityID = remapped
		} else {
			remapped := int32(len(r.ids))
			r.ids = append(r.ids, entry.SecurityID)
			r.index[entry.SecurityID] = remapped
			cp.SecurityID = remapped
		}
	}
	for _, child := range entry.Children {
		cp.Children = append(cp.Children, r.Copy(child))
	}
	return cp
}

// BuildSecurityData returns a new SecurityData holding just the descriptors
// referenced by every Copy call made on r so far, pulled from donor (the
// same SecurityData the copied entries' original SecurityIDs referenced),
// in the output index order Copy assigned them.
func (r *SecurityRemapper) BuildSecurityData(donor *SecurityData) *SecurityData {
	descriptors := make([][]byte, len(r.ids))
	for i, origID := range r.ids {
		if donor != nil && int(origID) < len(donor.Descriptors) {
			descriptors[i] = donor.Descriptors[origID]
		}
	}
	return &SecurityData{Descriptors: descriptors}
}

// AttachAt attaches node -- a whole prebuilt subtree, e.g. from
// SecurityRemapper.Copy -- as a child of root at path, creating any
// intervening directory entries that don't already exist (with the same
// defaults DirEntry.Add uses for intermediate directories: FileAttributeDirectory,
// SecurityIDNone). This is DirEntry.Add generalized to attach an entire
// prebuilt node instead of a single hash-only leaf, for callers (like a
// subtree graft) that already have a real, fully-populated DirEntry to
// place rather than just a hash.
//
// AttachAt sets node.Name from path's final component, overwriting whatever
// Name node already had (matching Add's own convention: a path's leaf name
// always comes from path, not from the value being placed).
func AttachAt(root *DirEntry, path string, node *DirEntry) error {
	components := splitPath(path)
	if len(components) == 0 {
		return fmt.Errorf("wim: AttachAt: empty path")
	}

	cur := root
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
			return fmt.Errorf("wim: AttachAt %q: path component %q already exists as a non-directory", path, name)
		}
		cur = child
	}

	leafName := components[len(components)-1]
	if cur.Child(leafName) != nil {
		return fmt.Errorf("wim: AttachAt %q: already exists", path)
	}
	node.Name = stringToUTF16LE(leafName)
	cur.Children = append(cur.Children, node)
	return nil
}
