package xpress

import "math/bits"

// Compress encodes data using the XPRESS LZ77+Huffman algorithm (the variant
// used for WIM resources; see the package doc in xpress.go) and returns the
// compressed bytes.
//
// The returned buffer is always a complete, self-contained XPRESS stream
// (Huffman header plus coded items) with no framing beyond what the format
// itself requires -- it never falls back to "stored uncompressed", since
// that decision (comparing against the uncompressed size and choosing
// whichever is smaller) is a policy of the container format, not of the
// codec. Callers that want that behavior compare len(Compress(data)) against
// len(data) themselves.
//
// Compress does not aim for optimal or even near-optimal compression ratio;
// see the "Encoder scope" discussion in xpress.go.
func Compress(data []byte) []byte {
	items := parseItems(data)

	var freqs [numSymbols]uint32
	for _, it := range items {
		freqs[symbolFor(it)]++
	}
	freqs[endOfData]++

	lens := buildLengths(&freqs)
	codewords := canonicalCodewords(lens)

	header := packHuffmanLengths(lens)
	// Compressed output is essentially never larger than the input (XPRESS
	// items are never longer than the literals/matches they replace), so
	// sizing the initial buffer to fit the whole input up front avoids
	// almost all of growTo's incremental reallocation.
	out := make([]byte, huffmanHeaderSize, huffmanHeaderSize+len(data)+16)
	copy(out, header[:])

	w := newBitWriter(out, huffmanHeaderSize)
	for _, it := range items {
		writeItem(w, it, &lens, &codewords)
	}
	// Write the end-of-data symbol. Some XPRESS decoders (notably
	// Microsoft's WIMGAPI, per wimlib's xpress_decompress.c comment)
	// require this trailing marker even though it carries no information
	// this package's own decoder needs.
	w.writeBits(uint32(codewords[endOfData]), uint32(lens[endOfData]))

	return w.flush()
}

// symbolFor returns the Huffman alphabet symbol (0-511) an item is coded
// with. For matches this folds the match into the 256 "match header"
// symbols using log2(offset) and a saturating low-length nibble, exactly as
// wimlib's xpress_record_match/xpress_write_item_list do.
func symbolFor(it item) int {
	if !it.isMatch {
		return int(it.literal)
	}
	adjustedLen := it.length - minMatchLen
	lenHdr := adjustedLen
	if lenHdr > 0xf {
		lenHdr = 0xf
	}
	log2Offset := bits.Len32(uint32(it.offset)) - 1
	return numChars + (log2Offset<<4 | lenHdr)
}

// writeExtraLengthBytes writes the byte/u16 length extension that follows a
// match's Huffman symbol whenever its adjusted length (length-minMatchLen)
// does not fit in the symbol's 4-bit length nibble.
func writeExtraLengthBytes(w *bitWriter, adjustedLen int) {
	if adjustedLen < 0xf {
		return
	}
	extra := adjustedLen - 0xf
	if extra > 0xff {
		extra = 0xff
	}
	w.writeByte(byte(extra))
	if extra == 0xff {
		w.writeU16(uint16(adjustedLen))
	}
}

// writeItem writes a single literal or match to the output bitstream.
func writeItem(w *bitWriter, it item, lens *[numSymbols]uint8, codewords *[numSymbols]uint16) {
	sym := symbolFor(it)
	w.writeBits(uint32(codewords[sym]), uint32(lens[sym]))
	if !it.isMatch {
		return
	}
	adjustedLen := it.length - minMatchLen
	log2Offset := bits.Len32(uint32(it.offset)) - 1
	writeExtraLengthBytes(w, adjustedLen)
	extraOffsetBits := uint32(it.offset) - (uint32(1) << uint(log2Offset))
	w.writeBits(extraOffsetBits, uint32(log2Offset))
}
