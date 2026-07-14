package lzx

import "testing"

// TestAlignSkipsFullUnitWhenAlreadyAligned pins the exact quirk documented
// on bitReader.align: LZX's format requires that, even when the bitstream
// is already sitting on a 16-bit coding-unit boundary (nothing buffered),
// align must still force-fetch and discard one whole 16-bit unit rather
// than being a no-op. See align's doc comment and the real-data regression
// this guards, uncompressed_block_test.go's
// TestDecompressRealUncompressedBlockChunk.
func TestAlignSkipsFullUnitWhenAlreadyAligned(t *testing.T) {
	// Three 16-bit units (6 bytes) up front, consumed exactly via one
	// 16-bit read so nbits lands back at exactly 0 -- the "already
	// aligned" case -- plus 4 trailing bytes for the final readU32 to
	// land on.
	data := []byte{0xAA, 0xBB, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	r := newBitReader(data)
	r.readBits(16) // consumes the first unit exactly; nbits == 0 afterward
	if r.nbits != 0 {
		t.Fatalf("test setup: nbits = %d, want 0 before align", r.nbits)
	}

	r.align()

	// The next 16-bit unit (0x11, 0x22) must have been fetched and
	// discarded even though the stream was already aligned; the following
	// raw read must return the unit after that (0x33, 0x44, 0x55, 0x66),
	// not (0x11, 0x22, 0x33, 0x44).
	if got, want := r.readU32(), uint32(0x66554433); got != want {
		t.Errorf("readU32 after align = %#x, want %#x", got, want)
	}
	if r.pos != 8 {
		t.Errorf("r.pos after align+readU32 = %d, want 8 (0x11/0x22 must have been skipped)", r.pos)
	}
}

// TestAlignDiscardsPartialUnitWhenNotAligned covers the other branch: when
// bits are already buffered (not aligned), align must discard exactly
// those buffered bits, landing on the very next 16-bit boundary -- not an
// extra whole unit beyond it.
func TestAlignDiscardsPartialUnitWhenNotAligned(t *testing.T) {
	data := []byte{0xAA, 0xBB, 0x11, 0x22, 0x33, 0x44}
	r := newBitReader(data)
	r.readBits(3) // leaves 13 bits of the first unit buffered (not aligned)
	if r.nbits == 0 {
		t.Fatalf("test setup: nbits = 0, want nonzero before align")
	}

	r.align()

	if r.pos != 2 {
		t.Fatalf("r.pos after align = %d, want 2 (only the partially-consumed first unit discarded)", r.pos)
	}
	if v := r.readU32(); v != 0x44332211 {
		t.Errorf("readU32 after align = %#x, want 0x44332211 (0x11,0x22,0x33,0x44 little-endian)", v)
	}
}
