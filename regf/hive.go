package regf

import "fmt"

// maxKeyTreeDepth guards against cyclic/malicious subkey offsets. `[MSDN]`
// (quoted in the spec's "Overview" section) states "a Registry tree can be
// 512 levels deep"; this package allows some slack over that for safety
// margin while still bounding recursion.
const maxKeyTreeDepth = 1024

// Hive is a fully parsed (or freshly-built) registry hive file: the base
// block plus the key tree rooted at BaseBlock.RootCellOffset.
type Hive struct {
	// BaseBlock is the hive's 4096-byte file header. On Parse it is
	// populated from the input; on AppendTo, RootCellOffset and
	// HiveBinsDataSize are recomputed from Root (any values previously set
	// there are ignored), and Checksum is recomputed by BaseBlock.AppendTo.
	// Other fields (sequence numbers, timestamps, version, file name, ...)
	// round-trip whatever the caller set.
	BaseBlock BaseBlock
	// Root is the hive's root key (conventionally flagged KeyFlagHiveEntry).
	Root *Key
}

// Parse decodes a complete hive file (base block + hive bins) from data.
func Parse(data []byte) (*Hive, error) {
	bb, err := ParseBaseBlock(data)
	if err != nil {
		return nil, err
	}
	if bb.FileType != FileTypePrimary {
		return nil, wrapErr("hive", fmt.Errorf("file type %d is not a primary hive (transaction log parsing is out of scope)", bb.FileType))
	}

	hbins := data[HBinDataStart:]
	if uint32(len(hbins)) < bb.HiveBinsDataSize {
		return nil, wrapErr("hive", fmt.Errorf("hive bins data size %d exceeds available data (%d bytes)", bb.HiveBinsDataSize, len(hbins)))
	}

	// Validate the hbin header chain up to HiveBinsDataSize; this catches
	// gross corruption early even though cell resolution below navigates by
	// offset rather than by walking bins.
	for off := uint32(0); off < bb.HiveBinsDataSize; {
		bin, next, err := parseHBin(hbins, off)
		if err != nil {
			return nil, wrapErr("hive bins", err)
		}
		if bin.Offset != off {
			return nil, wrapErr("hive bins", fmt.Errorf("hbin at %#x reports offset %#x", off, bin.Offset))
		}
		off = next
	}

	root, err := parseKeyTree(hbins, bb.RootCellOffset, 0)
	if err != nil {
		return nil, wrapErr("hive", err)
	}

	return &Hive{BaseBlock: *bb, Root: root}, nil
}

// parseKeyTree recursively decodes the nk cell at offset (relative to
// hbins) into a Key, resolving its values, security descriptor, class name,
// and subkeys.
func parseKeyTree(hbins []byte, offset uint32, depth int) (*Key, error) {
	if depth >= maxKeyTreeDepth {
		return nil, fmt.Errorf("key tree exceeds max depth %d (cyclic subkey offsets?)", maxKeyTreeDepth)
	}
	allocated, data, _, err := readCell(hbins, offset)
	if err != nil {
		return nil, wrapErr(fmt.Sprintf("nk cell at %#x", offset), err)
	}
	if !allocated {
		return nil, wrapErr(fmt.Sprintf("nk cell at %#x", offset), fmt.Errorf("cell is not allocated"))
	}
	n, err := parseNKCell(data)
	if err != nil {
		return nil, wrapErr(fmt.Sprintf("nk cell at %#x", offset), err)
	}

	k := &Key{
		Flags:       n.flags,
		LastWritten: n.lastWritten,
		Name:        n.name,
	}

	if n.classNameOffset != NoCellOffset && n.classNameSize > 0 {
		_, cnData, _, err := readCell(hbins, n.classNameOffset)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("class name for nk at %#x", offset), err)
		}
		if int(n.classNameSize) > len(cnData) {
			return nil, wrapErr(fmt.Sprintf("class name for nk at %#x", offset), fmt.Errorf("class name size %d overruns cell", n.classNameSize))
		}
		k.ClassName = cloneBytes(cnData[:n.classNameSize])
	}

	if n.securityOffset != NoCellOffset {
		_, skData, _, err := readCell(hbins, n.securityOffset)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("sk cell for nk at %#x", offset), err)
		}
		sk, err := parseSKCell(skData)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("sk cell for nk at %#x", offset), err)
		}
		k.Security = sk.descriptor
	}

	if n.valuesOffset != NoCellOffset && n.numValues > 0 {
		values, err := parseValuesList(hbins, n.valuesOffset, n.numValues)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("values list for nk at %#x", offset), err)
		}
		k.Values = values
	}

	if n.subkeysOffset != NoCellOffset && n.numSubkeys > 0 {
		subOffsets, err := resolveSubkeyList(hbins, n.subkeysOffset, 0)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("subkey list for nk at %#x", offset), err)
		}
		k.Subkeys = make([]*Key, 0, len(subOffsets))
		for _, so := range subOffsets {
			child, err := parseKeyTree(hbins, so, depth+1)
			if err != nil {
				return nil, err
			}
			k.Subkeys = append(k.Subkeys, child)
		}
	}

	return k, nil
}

// resolveSubkeyList decodes the subkey list cell at offset, returning the
// flat list of nk cell offsets it (transitively, through any "ri" index
// root) describes.
func resolveSubkeyList(hbins []byte, offset uint32, depth int) ([]uint32, error) {
	if depth >= maxKeyTreeDepth {
		return nil, fmt.Errorf("subkey list nesting exceeds max depth %d", maxKeyTreeDepth)
	}
	_, data, _, err := readCell(hbins, offset)
	if err != nil {
		return nil, err
	}
	list, err := parseSubkeyList(data)
	if err != nil {
		return nil, err
	}
	if list.Signature == subkeyListRI {
		var out []uint32
		for _, e := range list.Entries {
			sub, err := resolveSubkeyList(hbins, e.Offset, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		}
		return out, nil
	}
	out := make([]uint32, len(list.Entries))
	for i, e := range list.Entries {
		out[i] = e.Offset
	}
	return out, nil
}

// parseValuesList decodes the values list cell at offset (a flat array of
// count vk cell offsets; see "Values list - version 1.2 and later") and
// resolves each entry's vk cell and data.
func parseValuesList(hbins []byte, offset uint32, count uint32) ([]Value, error) {
	_, data, _, err := readCell(hbins, offset)
	if err != nil {
		return nil, err
	}
	need := int(count) * 4
	if len(data) < need {
		return nil, fmt.Errorf("values list: need %d bytes for %d entries, have %d", need, count, len(data))
	}
	values := make([]Value, count)
	for i := range values {
		vkOffset := le.Uint32(data[i*4 : i*4+4])
		_, vkData, _, err := readCell(hbins, vkOffset)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("vk cell at %#x", vkOffset), err)
		}
		name, dataSize, dataOffset, typ, flags, err := parseVKCell(vkData)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("vk cell at %#x", vkOffset), err)
		}
		valData, err := resolveValueData(hbins, dataSize, dataOffset)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("data for vk cell at %#x", vkOffset), err)
		}
		values[i] = Value{Name: name, Type: typ, Flags: flags, Data: valData}
	}
	return values, nil
}

// resolveValueData decodes a vk cell's data, given its raw dataSize and
// dataOffset fields, handling the inline convention and "db" big-data
// reassembly.
func resolveValueData(hbins []byte, dataSize, dataOffset uint32) ([]byte, error) {
	if dataSize&dataSizeInlineFlag != 0 {
		size := dataSize &^ dataSizeInlineFlag
		if size > maxInlineDataSize {
			return nil, fmt.Errorf("inline data size %d exceeds %d", size, maxInlineDataSize)
		}
		var raw [4]byte
		le.PutUint32(raw[:], dataOffset)
		return cloneBytes(raw[:size]), nil
	}
	if dataSize == 0 {
		return nil, nil
	}
	_, cellData, _, err := readCell(hbins, dataOffset)
	if err != nil {
		return nil, err
	}
	// A "db" (big-data) cell is only used when the value's data exceeds
	// DBSegmentMaxSize (16344 bytes) -- see "Value data": "values larger
	// than 16344 bytes are stored in multiple segments. Data about these
	// segments is stored in the data block key" (libregf's "Windows NT
	// Registry File (REGF) format specification"). Gating this only on
	// sniffing cellData[0:2] == "db", regardless of dataSize, is a real
	// false-positive risk: an ordinary (non-big-data) value whose first
	// two bytes happen to be 0x64 0x62 ('d','b') would be misidentified as
	// a data-block key and its own content misparsed as a segment-count/
	// segment-list-offset pair, corrupting the read with a bogus huge
	// offset. Found 2026-07-14 against a real Windows 11 25H2 COMPONENTS
	// hive value that hit exactly this (see hive_test.go's
	// TestResolveValueDataIgnoresCoincidentalDBSignature).
	if dataSize > DBSegmentMaxSize && len(cellData) >= 2 && string(cellData[0:2]) == string(dbMagic[:]) {
		db, err := parseDBCell(cellData)
		if err != nil {
			return nil, err
		}
		_, listData, _, err := readCell(hbins, db.segmentListOffset)
		if err != nil {
			return nil, wrapErr("db segment list", err)
		}
		segOffsets, err := parseSegmentList(listData, db.numSegments)
		if err != nil {
			return nil, wrapErr("db segment list", err)
		}
		out := make([]byte, 0, dataSize)
		remaining := int(dataSize)
		for _, so := range segOffsets {
			_, segData, _, err := readCell(hbins, so)
			if err != nil {
				return nil, wrapErr(fmt.Sprintf("db segment at %#x", so), err)
			}
			// A segment cell's data is 8-byte aligned, so it may run up to
			// 7 bytes longer than the actual segment content (e.g. a full
			// DBSegmentMaxSize segment, itself already a multiple of 8,
			// still gets padding if a trailing cell-size rounding pushed it
			// over). Cap at DBSegmentMaxSize first so that padding is never
			// mistaken for the next segment's leading bytes, then at
			// whatever's left of the value's true total size.
			take := segData
			if len(take) > DBSegmentMaxSize {
				take = take[:DBSegmentMaxSize]
			}
			if len(take) > remaining {
				take = take[:remaining]
			}
			out = append(out, take...)
			remaining -= len(take)
		}
		return out, nil
	}
	if int(dataSize) > len(cellData) {
		return nil, fmt.Errorf("data size %d exceeds cell (%d bytes)", dataSize, len(cellData))
	}
	return cloneBytes(cellData[:dataSize]), nil
}

// AppendTo serializes h as a complete hive file (base block + hive bins),
// appending to dst.
//
// Allocation strategy: AppendTo always builds a single hive bin sized to
// exactly fit h.Root's serialized cells, rounded up to a 4096-byte boundary
// (with any leftover space at the end represented as one free/unallocated
// cell, so the bin is itself well-formed). Cells are emitted in a
// deterministic post-order walk of the Key tree (a key's subkeys, their
// values/security/class-name cells, and finally the key's own nk cell), and
// every key's subkeys are combined into a single "lh" subkey-list cell using
// the documented LH hash algorithm (see lhHash in subkeylist.go) -- this
// package never needs "ri" index roots or multiple lists per key, since it
// does not try to mimic Windows' own list-splitting thresholds. sk cells
// ARE deduplicated across the whole tree (see skPool): every key with a
// byte-identical Security descriptor shares one sk cell, linked into a
// single circular list, matching how Windows itself shares descriptors --
// found essential 2026-07-15, not just a size optimization: a real hive
// resaved without this deduplication (every key given its own one-node sk
// list) more than doubled in size and made Windows hang indefinitely
// during first-logon specialize on the resulting image, even though the
// hive itself still parsed back correctly. This is sufficient to produce a
// hive that Parse reads back correctly, and that (when built from bytes
// this package itself produced) round-trips byte-for-byte; it does not
// reproduce an arbitrary pre-existing hive's original bin/cell layout or
// sk-list ordering.
func (h *Hive) AppendTo(dst []byte) ([]byte, error) {
	if h.Root == nil {
		return dst, wrapErr("hive", fmt.Errorf("nil root key"))
	}

	// Cell offsets are relative to the whole hive bins data area, which
	// begins with this bin's own 32-byte header (see "Hive bin header":
	// offsets are "relative from the start of the hive bin data area", and
	// note the file-offset formula in the "File header" section). Reserve
	// that header's bytes at the front of the arena up front, so offsets
	// returned by arena.alloc for the cells built below are already correct
	// without needing a further adjustment.
	arena := &cellArena{buf: make([]byte, HBinHeaderSize)}
	pool := newSKPool()
	rootOffset, err := buildKeyCell(arena, h.Root, NoCellOffset, pool)
	if err != nil {
		return dst, wrapErr("hive", err)
	}

	binSize := ((len(arena.buf) + (BaseBlockSize - 1)) / BaseBlockSize) * BaseBlockSize
	if binSize < BaseBlockSize {
		binSize = BaseBlockSize
	}
	if free := binSize - len(arena.buf); free > 0 {
		filler := make([]byte, free)
		le.PutUint32(filler[0:4], uint32(free)) // positive size => free cell
		arena.buf = append(arena.buf, filler...)
	}

	bin := &HBin{
		Offset:    0,
		Size:      uint32(binSize),
		Timestamp: h.BaseBlock.LastWritten,
	}
	bin.writeHeader(arena.buf)

	bb := h.BaseBlock
	bb.RootCellOffset = rootOffset
	bb.HiveBinsDataSize = uint32(binSize)
	if bb.FileType == 0 && bb.MajorVersion == 0 {
		// Caller left the base block entirely zero-valued; fill in sane
		// defaults for a fresh hive rather than emitting a structurally
		// nonsensical version 0.0 file.
		bb.MajorVersion = 1
		bb.MinorVersion = Version1_5
	}

	dst, err = bb.AppendTo(dst)
	if err != nil {
		return dst, wrapErr("hive base block", err)
	}
	dst = append(dst, arena.buf...)
	return dst, nil
}

// buildKeyCell serializes key and everything it owns into arena (subkeys
// first, then this key's own values/security/class-name/nk cells), and
// returns the offset of its nk cell. parentOffset is the offset of the
// caller's own nk cell if already known (it never is, since children are
// built before their parent in this post-order walk); pass NoCellOffset and
// buildKeyCell will patch each child's parentOffset field once this key's
// own offset becomes known. pool deduplicates security descriptors across
// the whole tree (see skPool's own doc comment for why this matters).
func buildKeyCell(arena *cellArena, key *Key, parentOffset uint32, pool *skPool) (uint32, error) {
	var largestSubkeyNameSize, largestValueNameSize, largestValueDataSize uint32

	subkeyOffsets := make([]uint32, len(key.Subkeys))
	for i, sub := range key.Subkeys {
		off, err := buildKeyCell(arena, sub, NoCellOffset, pool)
		if err != nil {
			return 0, err
		}
		subkeyOffsets[i] = off
		if n := uint32(len(sub.Name)); n > largestSubkeyNameSize {
			largestSubkeyNameSize = n
		}
	}

	subkeysOffset := NoCellOffset
	if len(subkeyOffsets) > 0 {
		list := &parsedSubkeyList{Signature: subkeyListLH}
		for i, sub := range key.Subkeys {
			list.Entries = append(list.Entries, subkeyListEntry{
				Offset: subkeyOffsets[i],
				Hash:   lhHash(sub.Name, sub.Flags&KeyFlagCompName != 0),
			})
		}
		data, err := list.appendTo(nil)
		if err != nil {
			return 0, err
		}
		subkeysOffset = arena.alloc(data)
	}

	valuesOffset := NoCellOffset
	if len(key.Values) > 0 {
		vkOffsets := make([]byte, 0, len(key.Values)*4)
		for _, v := range key.Values {
			vkOff, err := buildValueCell(arena, v)
			if err != nil {
				return 0, err
			}
			var buf [4]byte
			le.PutUint32(buf[:], vkOff)
			vkOffsets = append(vkOffsets, buf[:]...)
			if n := uint32(len(v.Name)); n > largestValueNameSize {
				largestValueNameSize = n
			}
			if n := uint32(len(v.Data)); n > largestValueDataSize {
				largestValueDataSize = n
			}
		}
		valuesOffset = arena.alloc(vkOffsets)
	}

	securityOffset := NoCellOffset
	if key.Security != nil {
		securityOffset = pool.getOrCreate(arena, key.Security)
	}

	classNameOffset := NoCellOffset
	var classNameSize uint16
	if key.ClassName != nil {
		classNameOffset = arena.alloc(key.ClassName)
		classNameSize = uint16(len(key.ClassName))
	}

	n := &nkCell{
		flags:                 key.Flags,
		lastWritten:           key.LastWritten,
		parentOffset:          parentOffset,
		numSubkeys:            uint32(len(key.Subkeys)),
		subkeysOffset:         subkeysOffset,
		volatileSubkeysOffset: NoCellOffset, // volatile keys are runtime-only; see package doc
		numValues:             uint32(len(key.Values)),
		valuesOffset:          valuesOffset,
		securityOffset:        securityOffset,
		classNameOffset:       classNameOffset,
		classNameSize:         classNameSize,
		largestSubkeyNameSize: largestSubkeyNameSize,
		largestValueNameSize:  largestValueNameSize,
		largestValueDataSize:  largestValueDataSize,
		name:                  key.Name,
	}
	offset := arena.alloc(n.appendTo(nil))

	for _, so := range subkeyOffsets {
		// nk parentOffset field lives at data-relative offset 16 (see
		// nkHeaderSize layout in nk.go: parentOffset is data[16:20]).
		patchUint32(arena.buf, so, 16, offset)
	}

	return offset, nil
}

// buildValueCell serializes one Value's data (inline, single out-of-line
// cell, or "db" big-data segments per DBSegmentMaxSize) and its vk cell,
// returning the vk cell's offset.
func buildValueCell(arena *cellArena, v Value) (uint32, error) {
	var dataSize, dataOffset uint32
	n := len(v.Data)
	switch {
	case n == 0:
		dataSize = 0
		dataOffset = NoCellOffset
	case isInlinableDataSize(n):
		var raw [4]byte
		copy(raw[:], v.Data)
		dataOffset = le.Uint32(raw[:])
		dataSize = uint32(n) | dataSizeInlineFlag
	case n <= DBSegmentMaxSize:
		dataOffset = arena.alloc(v.Data)
		dataSize = uint32(n)
	default:
		var segOffsets []uint32
		for i := 0; i < n; i += DBSegmentMaxSize {
			end := i + DBSegmentMaxSize
			if end > n {
				end = n
			}
			segOffsets = append(segOffsets, arena.alloc(v.Data[i:end]))
		}
		listData := appendSegmentList(nil, segOffsets)
		segListOffset := arena.alloc(listData)
		db := &dbCell{numSegments: uint16(len(segOffsets)), segmentListOffset: segListOffset}
		dataOffset = arena.alloc(db.appendTo(nil))
		dataSize = uint32(n)
	}

	vkBytes := appendToVK(nil, v.Name, dataSize, dataOffset, v.Type, v.Flags)
	return arena.alloc(vkBytes), nil
}

// patchUint32 overwrites the little-endian uint32 at data-relative offset
// fieldOff within the cell whose size field starts at cellOffset (i.e. the
// cell's data begins at cellOffset+4, per the "Hive bin cell" framing).
func patchUint32(buf []byte, cellOffset uint32, fieldOff int, value uint32) {
	pos := int(cellOffset) + cellSizeFieldSize + fieldOff
	le.PutUint32(buf[pos:pos+4], value)
}
