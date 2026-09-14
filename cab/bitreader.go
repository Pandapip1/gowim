package cab

import "encoding/binary"

// bitReader reads a CAB-LZX bitstream: bits packed into little-endian
// 16-bit coding units, consumed MSB-first, matching libmspack's lzxd.c
// READ_BYTES macro (BITS_ORDER_MSB, INJECT_BITS((b1<<8)|b0, 16)) -- the
// same bit-level convention gowim's sibling lzx package's own bitReader
// documents for the WIM flavor of LZX (this is shared, unlike the
// window/framing rules cablzx.go's doc comment describes).
type bitReader struct {
	data  []byte
	pos   int
	buf   uint64
	nbits uint
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

func (r *bitReader) ensure(n uint) {
	for r.nbits < n {
		var v uint16
		if r.pos+2 <= len(r.data) {
			v = binary.LittleEndian.Uint16(r.data[r.pos : r.pos+2])
			r.pos += 2
		} else if r.pos+1 == len(r.data) {
			v = uint16(r.data[r.pos])
			r.pos++
		}
		r.buf |= uint64(v) << (48 - r.nbits)
		r.nbits += 16
	}
}

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

func (r *bitReader) readBits(n uint) uint32 {
	if n == 0 {
		return 0
	}
	r.ensure(n)
	v := r.peek(n)
	r.remove(n)
	return v
}

// align discards buffered bits, then (per libmspack's own
// "if (bits_left == 0) ENSURE_BITS(16); bits_left = 0" sequence at its one
// LZX_BLOCKTYPE_UNCOMPRESSED call site) forces a fresh 16-bit unit fetch
// first if the stream happened to already be bit-aligned, matching the
// real encoder's behavior bit-for-bit.
func (r *bitReader) align() {
	if r.nbits == 0 {
		r.ensure(16)
	}
	r.buf = 0
	r.nbits = 0
}

func (r *bitReader) readByte() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *bitReader) readU32() uint32 {
	if r.pos+4 > len(r.data) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return v
}

func (r *bitReader) readBytes(dst []byte) bool {
	if r.pos+len(dst) > len(r.data) {
		return false
	}
	copy(dst, r.data[r.pos:r.pos+len(dst)])
	r.pos += len(dst)
	return true
}
