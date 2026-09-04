package wim

import (
	"fmt"
	"sync/atomic"
)

// DirEntryFixedSize is the size of the fixed-length portion of a directory
// entry on disk (WIM_DENTRY_DISK_SIZE).
const DirEntryFixedSize = 102

// ExtraStreamFixedSize is the size of the fixed-length portion of an extra
// stream entry on disk (length + reserved + hash + name_nbytes).
const ExtraStreamFixedSize = 8 + 8 + SHA1Size + 2 // 38

// Windows file attribute flags relevant to WIM structure interpretation.
const (
	FileAttributeDirectory    uint32 = 0x00000010
	FileAttributeReparsePoint uint32 = 0x00000400
	FileAttributeEncrypted    uint32 = 0x00004000
	SecurityIDNone            int32  = -1 // 0xFFFFFFFF: no security descriptor
	maxDentryTreeDepth               = 16384
)

// Stream is one data stream of a file: the unnamed (main) stream, or a named
// (alternate) NTFS-style data stream. Name is UTF-16LE bytes (empty for the
// unnamed stream); Hash is the SHA-1 of the stream's uncompressed contents.
type Stream struct {
	// Name is the stream name in UTF-16LE (no terminator). Empty means the
	// unnamed/main stream.
	Name []byte
	// Hash is the SHA-1 of the stream's uncompressed data (all zero if empty).
	Hash Hash
}

// NameUTF8 decodes the stream name from UTF-16LE to a Go string.
func (s Stream) NameUTF8() string { return utf16leToString(s.Name) }

// DirEntry is one node of a WIM image's directory-entry tree (a merged
// dentry+inode, since this package does not reconstruct hard-link groups).
//
// The first Stream (index 0) is always the unnamed/main stream, whose Hash is
// the dentry's main_hash on disk; any further Streams are named alternate data
// streams stored as extra stream entries.
type DirEntry struct {
	// Attributes is the Windows file-attribute bitmask (FileAttribute*).
	Attributes uint32
	// SecurityID is the 0-based index into the image SecurityData, or
	// SecurityIDNone (-1) if the entry has no security descriptor.
	SecurityID int32
	// CreationTime, LastAccessTime, LastWriteTime are Windows FILETIME values
	// (100-ns ticks since 1601-01-01 UTC).
	CreationTime   uint64
	LastAccessTime uint64
	LastWriteTime  uint64
	// Unknown0x54 preserves the reserved 4 bytes at offset 0x54.
	Unknown0x54 uint32

	// The union at offset 0x58 is interpreted per Attributes:
	// reparse-point files carry reparse metadata; others a hard-link group ID.

	// ReparseTag/ReparseReserved/ReparseFlags are meaningful only when
	// Attributes has FileAttributeReparsePoint set.
	ReparseTag      uint32
	ReparseReserved uint16
	ReparseFlags    uint16
	// HardLinkGroupID is meaningful only when the entry is not a reparse
	// point; entries sharing a value are hard links to the same inode.
	HardLinkGroupID uint64

	// Name is the long file name in UTF-16LE (no terminator). The root entry
	// has an empty name.
	Name []byte
	// ShortName is the 8.3 short name in UTF-16LE (no terminator), or empty.
	ShortName []byte

	// Streams holds the file's data streams; Streams[0] is the unnamed stream.
	Streams []Stream

	// Extra preserves any tagged-item metadata stored between the names and
	// the extra stream entries, as opaque bytes (already 8-byte aligned on
	// disk). This package does not interpret tagged items.
	Extra []byte

	// Children are the entries in this directory (nil/empty for non-directories
	// or empty directories).
	Children []*DirEntry

	// childIndex caches a name->child lookup index built from Children, so
	// that Child (see path.go) does not have to linearly re-scan and
	// re-decode every sibling's UTF-16 name on every single lookup (see
	// path.go's buildChildIndex doc comment for the measured impact). It is
	// validated against, and rebuilt from, the current Children slice on
	// every Child call (see sameChildrenSlice), so callers that mutate
	// Children directly (append/reassign, as this package's own Add, Remove,
	// Rename, and AttachAt all do) never observe a stale index - the only
	// requirement is that Children is not mutated *concurrently* with a
	// Child call on the same DirEntry, which this package does not do
	// anywhere (concurrent callers, such as component.BuildFromImage's
	// worker pool, only ever read the tree in parallel, never mutate it).
	childIndex atomic.Pointer[dirEntryChildIndex]
}

// IsDirectory reports whether the entry has the directory attribute.
func (d *DirEntry) IsDirectory() bool { return d.Attributes&FileAttributeDirectory != 0 }

// IsReparsePoint reports whether the entry is a reparse point.
func (d *DirEntry) IsReparsePoint() bool { return d.Attributes&FileAttributeReparsePoint != 0 }

// NameUTF8 decodes the long name from UTF-16LE to a Go string.
func (d *DirEntry) NameUTF8() string { return utf16leToString(d.Name) }

// MainHash returns the SHA-1 of the unnamed stream (the dentry main_hash),
// or the zero hash if there are no streams.
func (d *DirEntry) MainHash() Hash {
	if len(d.Streams) == 0 {
		return Hash{}
	}
	return d.Streams[0].Hash
}

// dirEntry field offsets, from struct wim_dentry_on_disk.
const (
	deLength         = 0x00 // le64
	deAttributes     = 0x08 // le32
	deSecurityID     = 0x0c // le32 (signed)
	deSubdirOffset   = 0x10 // le64
	deUnused1        = 0x18 // le64
	deUnused2        = 0x20 // le64
	deCreationTime   = 0x28 // le64
	deLastAccessTime = 0x30 // le64
	deLastWriteTime  = 0x38 // le64
	deMainHash       = 0x40 // 20 bytes
	deUnknown0x54    = 0x54 // le32
	deUnion          = 0x58 // 8 bytes (reparse{tag le32, reserved le16, flags le16} | le64 ino)
	deNumExtraStream = 0x60 // le16
	deShortNameNB    = 0x62 // le16
	deNameNB         = 0x64 // le16
	// 0x66: variable-length names, extra data, extra streams
)

func alignUp8Int(n int) int { return (n + 7) &^ 7 }

// dentryMinLenWithNames mirrors dentry_min_len_with_names.
func dentryMinLenWithNames(nameNB, shortNameNB int) int {
	l := DirEntryFixedSize
	if nameNB != 0 {
		l += nameNB + 2
	}
	if shortNameNB != 0 {
		l += shortNameNB + 2
	}
	return l
}

// parseDirEntry reads a single directory entry starting at buf[off]. It returns
// the entry (nil for an end-of-directory marker), the offset of the subdir
// child list (0 if none), and the offset just past this entry and its extra
// stream entries.
func parseDirEntry(buf []byte, off uint64) (entry *DirEntry, subdirOff, next uint64, err error) {
	if off+8 > uint64(len(buf)) || off+8 < off {
		return nil, 0, 0, fmt.Errorf("%w: dentry length field out of bounds", ErrInvalidHeader)
	}
	p := buf[off:]
	length := alignUp8(le.Uint64(p[deLength:]))

	// A length of <= 8 marks end-of-directory.
	if length <= 8 {
		return nil, 0, 0, nil
	}
	if length < DirEntryFixedSize {
		return nil, 0, 0, fmt.Errorf("%w: dentry length %d shorter than fixed size", ErrInvalidHeader, length)
	}
	if off+length > uint64(len(buf)) || off+length < off {
		return nil, 0, 0, fmt.Errorf("%w: dentry overruns metadata resource", ErrInvalidHeader)
	}

	d := &DirEntry{}
	d.Attributes = le.Uint32(p[deAttributes:])
	d.SecurityID = int32(le.Uint32(p[deSecurityID:]))
	subdirOff = le.Uint64(p[deSubdirOffset:])
	d.CreationTime = le.Uint64(p[deCreationTime:])
	d.LastAccessTime = le.Uint64(p[deLastAccessTime:])
	d.LastWriteTime = le.Uint64(p[deLastWriteTime:])
	d.Unknown0x54 = le.Uint32(p[deUnknown0x54:])

	if d.Attributes&FileAttributeReparsePoint != 0 {
		d.ReparseTag = le.Uint32(p[deUnion:])
		d.ReparseReserved = le.Uint16(p[deUnion+4:])
		d.ReparseFlags = le.Uint16(p[deUnion+6:])
	} else {
		d.HardLinkGroupID = le.Uint64(p[deUnion:])
	}

	var mainHash Hash
	copy(mainHash[:], p[deMainHash:deMainHash+SHA1Size])

	numExtraStreams := int(le.Uint16(p[deNumExtraStream:]))
	shortNameNB := int(le.Uint16(p[deShortNameNB:]))
	nameNB := int(le.Uint16(p[deNameNB:]))
	if shortNameNB&1 != 0 || nameNB&1 != 0 {
		return nil, 0, 0, fmt.Errorf("%w: dentry name length not even", ErrInvalidHeader)
	}

	minSize := uint64(dentryMinLenWithNames(nameNB, shortNameNB))
	if length < minSize {
		return nil, 0, 0, fmt.Errorf("%w: dentry length %d too small for its names", ErrInvalidHeader, length)
	}

	// Variable-length fields begin right after the fixed portion.
	q := DirEntryFixedSize
	if nameNB != 0 {
		d.Name = cloneBytes(p[q : q+nameNB])
		q += nameNB + 2 // skip null terminator
	}
	if shortNameNB != 0 {
		d.ShortName = cloneBytes(p[q : q+shortNameNB])
		q += shortNameNB + 2
	}

	// Any remaining space within 'length' after 8-byte alignment is tagged
	// extra data. Preserve it verbatim.
	q = alignUp8Int(q)
	if uint64(q) < length {
		d.Extra = cloneBytes(p[q:length])
	}

	// The unnamed stream comes from main_hash; named streams follow the
	// dentry, starting at the next entry offset (off + length).
	d.Streams = make([]Stream, 0, 1+numExtraStreams)
	d.Streams = append(d.Streams, Stream{Hash: mainHash})

	streamOff := off + length
	for i := 0; i < numExtraStreams; i++ {
		if streamOff+ExtraStreamFixedSize > uint64(len(buf)) {
			return nil, 0, 0, fmt.Errorf("%w: extra stream entry out of bounds", ErrInvalidHeader)
		}
		sp := buf[streamOff:]
		slen := alignUp8(le.Uint64(sp[0:]))
		if slen < ExtraStreamFixedSize || streamOff+slen > uint64(len(buf)) {
			return nil, 0, 0, fmt.Errorf("%w: invalid extra stream entry length %d", ErrInvalidHeader, slen)
		}
		var sh Hash
		copy(sh[:], sp[16:16+SHA1Size])
		snNB := int(le.Uint16(sp[36:]))
		var sname []byte
		if snNB != 0 {
			if snNB&1 != 0 {
				return nil, 0, 0, fmt.Errorf("%w: extra stream name length not even", ErrInvalidHeader)
			}
			if uint64(ExtraStreamFixedSize+snNB) > slen {
				return nil, 0, 0, fmt.Errorf("%w: extra stream name overruns entry", ErrInvalidHeader)
			}
			sname = cloneBytes(sp[ExtraStreamFixedSize : ExtraStreamFixedSize+snNB])
		}
		d.Streams = append(d.Streams, Stream{Name: sname, Hash: sh})
		streamOff += slen
	}

	return d, subdirOff, streamOff, nil
}

// parseDirEntryTreeChildren reads the sibling list beginning at 'off' into
// children.
func parseDirEntryTreeChildren(buf []byte, off uint64, depth int) ([]*DirEntry, error) {
	if depth >= maxDentryTreeDepth {
		return nil, fmt.Errorf("%w: directory structure too deep", ErrInvalidHeader)
	}
	var children []*DirEntry
	cur := off
	for {
		child, subdirOff, next, err := parseDirEntry(buf, cur)
		if err != nil {
			return nil, err
		}
		if child == nil {
			return children, nil // end-of-directory
		}
		if subdirOff != 0 && child.IsDirectory() {
			sub, err := parseDirEntryTreeChildren(buf, subdirOff, depth+1)
			if err != nil {
				return nil, err
			}
			child.Children = sub
		}
		children = append(children, child)
		cur = next
	}
}

// ParseDirEntryTree reads a directory-entry tree from a decompressed metadata
// resource buffer, starting at rootOffset. It returns the root entry (which may
// be nil if the resource contained only an end-of-directory marker).
func ParseDirEntryTree(buf []byte, rootOffset uint64) (*DirEntry, error) {
	root, subdirOff, _, err := parseDirEntry(buf, rootOffset)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, nil
	}
	if subdirOff != 0 && root.IsDirectory() {
		children, err := parseDirEntryTreeChildren(buf, subdirOff, 0)
		if err != nil {
			return nil, err
		}
		root.Children = children
	}
	return root, nil
}

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
