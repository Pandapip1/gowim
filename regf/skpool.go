package regf

// skPool deduplicates sk (security key) cells during hive serialization.
//
// Windows shares one sk cell across every key with a byte-identical
// security descriptor, via the format's circular doubly-linked list (see
// skCell's own doc comment) -- a real hive typically has only a few dozen
// unique descriptors shared across tens of thousands of keys. getOrCreate
// reproduces that sharing: it allocates a new sk cell only the first time a
// given descriptor is seen, and reuses (incrementing refCount) that same
// cell's offset for every subsequent key with the identical descriptor.
//
// This replaces AppendTo's previous behavior of giving every non-nil
// Key.Security its own new sk cell (a trivial one-node circular list per
// key) -- found 2026-07-15, via a real Windows 11 SOFTWARE hive, to more
// than double the hive's size (76.5MB parsed down to a working tree,
// 181MB back out) purely from this duplication, since almost every key in
// a real hive shares one of a small number of descriptors. Bloat alone
// would already justify fixing this, but the real-world impact was worse:
// the resulting WIM, though byte-valid and correctly verified by
// wimlib-imagex, made Windows hang indefinitely (black screen, active CPU)
// during first-logon specialize -- consistent with some first-logon
// component enumerating registry security descriptors and choking on tens
// of thousands of trivially-distinct one-node lists instead of the small
// shared pool a real hive has.
type skPool struct {
	byDescriptor map[string]uint32
	first, last  uint32
}

func newSKPool() *skPool {
	return &skPool{byDescriptor: make(map[string]uint32), first: NoCellOffset, last: NoCellOffset}
}

// getOrCreate returns the offset of an sk cell holding descriptor's bytes,
// within arena, deduplicating against every descriptor already seen by
// this pool.
func (p *skPool) getOrCreate(arena *cellArena, descriptor []byte) uint32 {
	key := string(descriptor)
	if off, ok := p.byDescriptor[key]; ok {
		pos := int(off) + cellSizeFieldSize + 12 // refCount field, see skHeaderSize's layout
		le.PutUint32(arena.buf[pos:pos+4], le.Uint32(arena.buf[pos:pos+4])+1)
		return off
	}

	sk := &skCell{refCount: 1, descriptor: descriptor}
	off := arena.alloc(sk.appendTo(nil))
	p.byDescriptor[key] = off

	if p.first == NoCellOffset {
		// First cell in the pool: trivial one-node circular list, matching
		// the previous single-key behavior for the first descriptor seen.
		patchUint32(arena.buf, off, 4, off)
		patchUint32(arena.buf, off, 8, off)
		p.first = off
		p.last = off
		return off
	}

	// Insert after the current last cell, keeping the list circular:
	// last -> off -> first, and first's prev now points back to off.
	patchUint32(arena.buf, p.last, 8, off)
	patchUint32(arena.buf, off, 4, p.last)
	patchUint32(arena.buf, off, 8, p.first)
	patchUint32(arena.buf, p.first, 4, off)
	p.last = off
	return off
}
