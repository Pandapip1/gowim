package lzx

import "encoding/binary"

// bitReader reads an LZX bitstream: bits are packed into little-endian
// 16-bit coding units, with the bits of each unit consumed from high to low
// (MSB first). This mirrors wimlib's struct input_bitstream in
// include/wimlib/decompress_common.h, though the internal representation
// here differs (a right-growing 64-bit accumulator instead of the C code's
// 32-bit left-justified one) since this package does not need to match
// wimlib's exact register-level tricks, only the resulting bitstream
// semantics.
//
// If the input is exhausted, further reads behave as though the missing
// bits are all zero, matching wimlib's documented behavior (bad compressed
// data may go undetected this way, but that is not a concern here since the
// caller checksums/round-trips the uncompressed data anyway).
type bitReader struct {
	data  []byte
	pos   int
	buf   uint64
	nbits uint
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

// ensure guarantees that at least n bits (n <= 32) are available to peek in
// buf, topmost-justified within the 64-bit word.
func (r *bitReader) ensure(n uint) {
	for r.nbits < n {
		var v uint16
		if r.pos+2 <= len(r.data) {
			v = binary.LittleEndian.Uint16(r.data[r.pos : r.pos+2])
			r.pos += 2
		} else if r.pos+1 == len(r.data) {
			v = uint16(r.data[r.pos])
			r.pos++
		} else {
			v = 0
		}
		r.buf |= uint64(v) << (48 - r.nbits)
		r.nbits += 16
	}
}

// peek returns the next n bits without consuming them. Requires a prior
// ensure(n) (or ensure of at least n bits).
func (r *bitReader) peek(n uint) uint32 {
	if n == 0 {
		return 0
	}
	return uint32(r.buf >> (64 - n))
}

func (r *bitReader) remove(n uint) {
	r.buf <<= n
	r.nbits -= n
}

// readBits reads and consumes the next n bits (0 <= n <= 32).
func (r *bitReader) readBits(n uint) uint32 {
	if n == 0 {
		return 0
	}
	r.ensure(n)
	v := r.peek(n)
	r.remove(n)
	return v
}

// align discards any bits currently buffered (but not yet consumed from the
// byte stream), realigning subsequent raw byte reads to the next 16-bit
// coding-unit boundary. Matches bitstream_align().
func (r *bitReader) align() {
	r.buf = 0
	r.nbits = 0
}

// readByte reads one literal byte directly from the underlying stream
// (bypassing the bit buffer). Used for uncompressed-block padding.
func (r *bitReader) readByte() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

// readU32 reads a little-endian 32-bit integer directly from the underlying
// stream (bypassing the bit buffer). Used for uncompressed-block recent
// offsets.
func (r *bitReader) readU32() uint32 {
	if r.pos+4 > len(r.data) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return v
}

// readBytes reads count literal bytes directly from the underlying stream
// (bypassing the bit buffer) into a new slice. Used for uncompressed block
// bodies. Returns false if the stream does not have count bytes remaining.
func (r *bitReader) readBytes(dst []byte) bool {
	if r.pos+len(dst) > len(r.data) {
		return false
	}
	copy(dst, r.data[r.pos:r.pos+len(dst)])
	r.pos += len(dst)
	return true
}
