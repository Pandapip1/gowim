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

// align discards any bits currently buffered, realigning subsequent raw
// byte reads to the next 16-bit coding-unit boundary. Ported line-for-line
// from wimlib's own two-step sequence at the one call site that uses it
// (src/lzx_decompress.c's LZX_BLOCKTYPE_UNCOMPRESSED case:
// "bitstream_ensure_bits(is, 1); bitstream_align(is);"), not just
// bitstream_align() alone (include/wimlib/decompress_common.h), which by
// itself is trivial (just zeroes bitsleft/bitbuf) -- the preceding
// ensure_bits(1) call is what actually matters here. wimlib's own comment
// at that call site explains why: "if the stream is *already* aligned, the
// correct thing to do is to throw away the next 16 bits (this is probably
// a mistake in the format)" -- i.e. requesting at least 1 bit forces a
// fresh 16-bit fetch whenever none is currently buffered, and that whole
// unit must then be discarded even though the stream was already
// byte/unit-aligned, a deliberate (if accidental) quirk real encoders
// reproduce and a decoder must match bit-for-bit or desync. Omitting the
// ensure(1) call here (i.e. calling align only when nbits happens to
// already be 0) was the actual bug found 2026-07-14 via a real Windows 11
// install.wim file
// (amd64_windows-senseclient-service_.../nl7models0804.dll's chunk 60,
// which contains an UNCOMPRESSED block landing exactly on an
// already-aligned boundary): without the forced fetch, no extra unit was
// discarded, desynchronizing every subsequent block header read.
func (r *bitReader) align() {
	r.ensure(1)
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
