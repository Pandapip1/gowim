package cab

import "errors"

// ErrInvalidLZXData is returned for any structurally-invalid CAB-LZX
// bitstream (bad Huffman symbol, bad block type, out-of-range match, etc).
var ErrInvalidLZXData = errors.New("invalid LZX data")

// ErrEmptyHuffmanTree is returned by newHuffTable when every codeword
// length is zero (a real, valid case for LZX's LENGTH tree when a block has
// no matches at all -- libmspack's own lzxd.c header comment notes this
// explicitly: "many CAB files contain blocks where the length tree is
// completely empty ... and this is expected to succeed").
var ErrEmptyHuffmanTree = errors.New("cab: empty huffman tree")

// huffTable is a full, direct-lookup canonical Huffman decode table: for a
// tree whose longest codeword is maxLen bits, table has 2^maxLen entries,
// each entry naming the symbol decoded by every bitstream prefix beginning
// with that codeword (so a lookup is a single peek(maxLen)+table index,
// with no secondary/nested table needed -- simpler than libmspack's own
// multi-level readhuff.h table, but must produce bit-for-bit identical
// decode decisions since canonical Huffman code assignment from a set of
// codeword lengths is uniquely determined, independent of implementation
// strategy: this is not this package's own invention, but the standard
// canonical-code construction LZX/DEFLATE-family formats always use, the
// same one gowim's sibling lzx package's decoder already relies on for the
// shared WIM/CAB LZX bit-level coding).
type huffTable struct {
	maxLen uint
	sym    []uint16
	length []byte
}

// newHuffTable builds a canonical Huffman decode table from codeword
// lengths (lens[i] is symbol i's codeword length, 0 meaning "unused").
// tableBits is accepted for documentation parity with libmspack's TABLEBITS
// constants but does not otherwise affect this implementation (see
// huffTable's doc comment).
func newHuffTable(lens []byte, tableBits int) (*huffTable, error) {
	t, empty, err := newHuffTableMaybeEmpty(lens, tableBits)
	if err != nil {
		return nil, err
	}
	if empty {
		return nil, ErrEmptyHuffmanTree
	}
	return t, nil
}

// newHuffTableMaybeEmpty is like newHuffTable but returns (nil, true, nil)
// instead of an error when every length is zero, for the one real tree
// (LENGTH) that is allowed to be empty.
func newHuffTableMaybeEmpty(lens []byte, tableBits int) (*huffTable, bool, error) {
	var maxLen uint
	var count [17]int
	for _, l := range lens {
		if l > 16 {
			return nil, false, ErrInvalidLZXData
		}
		if l == 0 {
			// count[0] must stay 0 for the next-code derivation below
			// (a zero-length "codeword" is just "unused symbol", not a
			// real 0-bit code to count) -- per RFC 1951 section 3.2.2's
			// explicit warning, "the bl_count[0] entry should never be
			// used". Skipping it here was the actual bug found while
			// decoding this package's real test cabinet: counting unused
			// symbols corrupted every subsequent length's next_code.
			continue
		}
		count[l]++
		if uint(l) > maxLen {
			maxLen = uint(l)
		}
	}
	if maxLen == 0 {
		return nil, true, nil
	}

	// Standard canonical-Huffman first-code-per-length derivation.
	var nextCode [17]int
	code := 0
	for l := 1; l <= int(maxLen); l++ {
		code = (code + count[l-1]) << 1
		nextCode[l] = code
	}

	size := 1 << maxLen
	tbl := &huffTable{
		maxLen: maxLen,
		sym:    make([]uint16, size),
		length: make([]byte, size),
	}
	for sym, l := range lens {
		if l == 0 {
			continue
		}
		c := nextCode[l]
		nextCode[l]++
		// This codeword occupies every table index whose top l bits equal
		// c: shift c into the high bits of a maxLen-bit index and fill the
		// low (maxLen-l) "don't care" bits.
		start := c << (maxLen - uint(l))
		span := 1 << (maxLen - uint(l))
		for i := start; i < start+span; i++ {
			tbl.sym[i] = uint16(sym)
			tbl.length[i] = l
		}
	}
	return tbl, false, nil
}

// decode reads one Huffman-coded symbol from r using t.
func (t *huffTable) decode(r *bitReader) (uint16, bool) {
	r.ensure(t.maxLen)
	idx := r.peek(t.maxLen)
	l := t.length[idx]
	if l == 0 {
		return 0, false
	}
	r.remove(uint(l))
	return t.sym[idx], true
}
