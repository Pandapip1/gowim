package wim

import "fmt"

// outLen returns the on-disk length of this single dentry (fixed fields +
// names + aligned extra data + extra stream entries), matching
// dentry_out_total_length. It does NOT include child dentries or the
// end-of-directory marker.
//
// This package writes back the stream layout that was parsed: when the entry
// uses extra stream entries (see usesExtraStreams), each entry beyond the one
// carried in main_hash is emitted as an extra stream entry; otherwise the
// single stream's hash is packed into main_hash and no extra entries follow.
func (d *DirEntry) outLen() uint64 {
	l := uint64(dentryMinLenWithNames(len(d.Name), len(d.ShortName)))
	l = alignUp8(l)
	l += alignUp8(uint64(len(d.Extra)))
	if d.usesExtraStreams() {
		for _, s := range d.extraStreamsToWrite() {
			entry := uint64(ExtraStreamFixedSize)
			if len(s.Name) != 0 {
				entry += uint64(len(s.Name)) + 2
			}
			l += alignUp8(entry)
		}
	}
	return l
}

// usesExtraStreams reports whether this entry must be written using extra
// stream entries rather than an inline main_hash. This is true whenever there
// is more than one stream, or the single stream is named.
func (d *DirEntry) usesExtraStreams() bool {
	if len(d.Streams) > 1 {
		return true
	}
	if len(d.Streams) == 1 && len(d.Streams[0].Name) != 0 {
		return true
	}
	return false
}

// extraStreamsToWrite returns the streams that will be emitted as extra stream
// entries (i.e. everything except the one placed in main_hash). It must agree
// with appendDentry's emission logic and with parseDirEntry's inversion.
func (d *DirEntry) extraStreamsToWrite() []Stream {
	if !d.usesExtraStreams() {
		return nil
	}
	if len(d.Streams) > 0 && len(d.Streams[0].Name) == 0 {
		// Streams[0] (unnamed) goes into main_hash; the rest are extras.
		return d.Streams[1:]
	}
	// Degenerate case: first stream is named, so all streams are extras.
	return d.Streams
}

// assignSubdirOffsets walks the tree in wimlib's order, assigning each
// directory a subdir offset relative to the start of the dentry region, and
// returns the total size of the region. It mirrors calculate_subdir_offsets:
// the root dentry and its trailing end-of-directory marker come first, then a
// pre-order walk lays out each directory's children contiguously.
func assignSubdirOffsets(root *DirEntry) (map[*DirEntry]uint64, uint64) {
	offsets := make(map[*DirEntry]uint64)
	// The root dentry itself occupies root.outLen() bytes, followed by an
	// 8-byte end-of-directory marker; children regions start after that.
	cursor := root.outLen() + 8

	walk := func(dir *DirEntry) {
		if !dir.IsDirectory() {
			offsets[dir] = 0
			return
		}
		offsets[dir] = cursor
		for _, c := range dir.Children {
			cursor += c.outLen()
		}
		cursor += 8 // end-of-directory marker
	}
	// Assign in the same visitation order wimlib uses (parent before
	// descendants, siblings left to right).
	var order []*DirEntry
	var collect func(d *DirEntry)
	collect = func(d *DirEntry) {
		order = append(order, d)
		for _, c := range d.Children {
			collect(c)
		}
	}
	collect(root)
	for _, d := range order {
		walk(d)
	}
	return offsets, cursor
}

// appendDentry serializes one dentry (and its extra stream entries) to dst.
func (d *DirEntry) appendDentry(dst []byte, subdirOffset uint64) ([]byte, error) {
	if len(d.Name)&1 != 0 || len(d.ShortName)&1 != 0 {
		return dst, fmt.Errorf("wim: dentry name length must be even (UTF-16LE)")
	}
	if len(d.Name) > 0xffff || len(d.ShortName) > 0xffff {
		return dst, fmt.Errorf("wim: dentry name too long")
	}
	start := len(dst)

	var fixed [DirEntryFixedSize]byte
	le.PutUint32(fixed[deAttributes:], d.Attributes)
	le.PutUint32(fixed[deSecurityID:], uint32(d.SecurityID))
	le.PutUint64(fixed[deSubdirOffset:], subdirOffset)
	le.PutUint64(fixed[deCreationTime:], d.CreationTime)
	le.PutUint64(fixed[deLastAccessTime:], d.LastAccessTime)
	le.PutUint64(fixed[deLastWriteTime:], d.LastWriteTime)
	le.PutUint32(fixed[deUnknown0x54:], d.Unknown0x54)
	if d.Attributes&FileAttributeReparsePoint != 0 {
		le.PutUint32(fixed[deUnion:], d.ReparseTag)
		le.PutUint16(fixed[deUnion+4:], d.ReparseReserved)
		le.PutUint16(fixed[deUnion+6:], d.ReparseFlags)
	} else {
		le.PutUint64(fixed[deUnion:], d.HardLinkGroupID)
	}

	useExtra := d.usesExtraStreams()
	if !useExtra {
		var mh Hash
		if len(d.Streams) == 1 {
			mh = d.Streams[0].Hash
		}
		copy(fixed[deMainHash:], mh[:])
		le.PutUint16(fixed[deNumExtraStream:], 0)
	} else {
		// main_hash carries the unnamed stream (Streams[0]) when it is
		// unnamed; the remaining streams become extra stream entries. This
		// inverts parseDirEntry, which builds Streams[0] from main_hash and
		// then reads num_extra_streams further entries.
		if len(d.Streams) > 0 && len(d.Streams[0].Name) == 0 {
			copy(fixed[deMainHash:], d.Streams[0].Hash[:])
		}
		// (Degenerate named-first case leaves main_hash zero.)
		le.PutUint16(fixed[deNumExtraStream:], uint16(len(d.extraStreamsToWrite())))
	}
	le.PutUint16(fixed[deShortNameNB:], uint16(len(d.ShortName)))
	le.PutUint16(fixed[deNameNB:], uint16(len(d.Name)))
	dst = append(dst, fixed[:]...)

	if len(d.Name) != 0 {
		dst = append(dst, d.Name...)
		dst = append(dst, 0, 0) // UTF-16LE null terminator
	}
	if len(d.ShortName) != 0 {
		dst = append(dst, d.ShortName...)
		dst = append(dst, 0, 0)
	}
	dst = padTo8(dst, start)

	if len(d.Extra) != 0 {
		dst = append(dst, d.Extra...)
		dst = padTo8(dst, start)
	}

	// The dentry 'length' field covers everything up to (but not including)
	// the extra stream entries.
	length := uint64(len(dst) - start)
	le.PutUint64(dst[start+deLength:], length)

	if useExtra {
		for _, s := range d.extraStreamsToWrite() {
			dst = appendExtraStream(dst, s)
		}
	}
	return dst, nil
}

// appendExtraStream serializes one extra stream entry.
func appendExtraStream(dst []byte, s Stream) []byte {
	start := len(dst)
	var fixed [ExtraStreamFixedSize]byte
	// length filled in after alignment; reserved stays zero.
	copy(fixed[16:], s.Hash[:])
	le.PutUint16(fixed[36:], uint16(len(s.Name)))
	dst = append(dst, fixed[:]...)
	if len(s.Name) != 0 {
		dst = append(dst, s.Name...)
		dst = append(dst, 0, 0)
	}
	dst = padTo8(dst, start)
	le.PutUint64(dst[start:], uint64(len(dst)-start))
	return dst
}

// padTo8 appends zero bytes until len(dst)-start is a multiple of 8.
func padTo8(dst []byte, start int) []byte {
	for (len(dst)-start)&7 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

// AppendDirEntryTree serializes a directory-entry tree rooted at root into a
// buffer suitable for placement at the start of the dentry region of a metadata
// resource. It returns the serialized bytes appended to dst.
//
// The layout matches write_dentry_tree: the root dentry, an end-of-directory
// marker, then a pre-order walk emitting each directory's children followed by
// that directory's end-of-directory marker. Directory subdir offsets are
// computed to match calculate_subdir_offsets, expressed relative to the start
// of the appended region. To place the tree at a nonzero offset within a
// metadata resource, use ImageMetadata.AppendTo.
func AppendDirEntryTree(dst []byte, root *DirEntry) ([]byte, error) {
	return appendDirEntryTreeBased(dst, root, 0)
}
