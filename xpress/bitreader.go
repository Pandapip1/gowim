package xpress

import "encoding/binary"

// bitReader is the decode-side counterpart of bitWriter: it mirrors wimlib's
// input_bitstream (decompress_common.h) exactly. A single forward cursor
// (next) is shared between bit-oriented reads (which pull 16-bit
// little-endian coding units into a left-justified 32-bit buffer and peel
// bits off MSB-first) and byte-oriented reads (raw bytes and u16s read
// directly from the same cursor, used for match-length extensions). This
// only works — i.e. only reconstructs the original data — because the
// encoder produces its output with the matching delayed coding-unit
// placement described in bitwriter.go.
type bitReader struct {
	data []byte
	pos  int // next unread byte offset into data

	bitBuf   uint32 // left-justified: the next bit to read is bit 31
	bitsLeft uint32
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

// ensureBits guarantees that at least numBits bits are available to peek.
// numBits must be at most 16. If the underlying buffer is exhausted, the
// missing bits are treated as zero (matching wimlib's documented behavior:
// malformed/truncated input is not specially detected here since the
// decompressed-size check in Decompress catches the resulting mismatch).
func (r *bitReader) ensureBits(numBits uint32) {
	if r.bitsLeft >= numBits {
		return
	}
	var word uint16
	if len(r.data)-r.pos >= 2 {
		word = binary.LittleEndian.Uint16(r.data[r.pos:])
		r.pos += 2
	}
	r.bitBuf |= uint32(word) << (16 - r.bitsLeft)
	r.bitsLeft += 16
}

// peekBits returns the next numBits bits without consuming them. ensureBits
// must have been called first with an argument >= numBits.
func (r *bitReader) peekBits(numBits uint32) uint32 {
	if numBits == 0 {
		return 0
	}
	return r.bitBuf >> (32 - numBits)
}

// removeBits consumes numBits bits previously guaranteed by ensureBits.
func (r *bitReader) removeBits(numBits uint32) {
	r.bitBuf <<= numBits
	r.bitsLeft -= numBits
}

// popBits reads and removes numBits bits.
func (r *bitReader) popBits(numBits uint32) uint32 {
	v := r.peekBits(numBits)
	r.removeBits(numBits)
	return v
}

// readBits ensures and then pops numBits bits (<=16).
func (r *bitReader) readBits(numBits uint32) uint32 {
	r.ensureBits(numBits)
	return r.popBits(numBits)
}

// readByte reads one raw interleaved byte directly from the stream cursor,
// bypassing the bit buffer. Returns 0 if the input is exhausted.
func (r *bitReader) readByte() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

// readU16 reads a raw little-endian 16-bit value directly from the stream
// cursor. Returns 0 if the input is exhausted.
func (r *bitReader) readU16() uint16 {
	if len(r.data)-r.pos < 2 {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v
}
