package lzms

// This file implements LZMS's binary range coder and the two auxiliary bit
// streams ("forwards" for range-coded bits, "backwards" for Huffman symbols
// and verbatim bits) it is packaged with. Ported from
// src/lzms_decompress.c (struct lzms_range_decoder, struct
// lzms_input_bitstream) and src/lzms_compress.c (struct lzms_range_encoder,
// struct lzms_output_bitstream) in wimlib; see lzms.go for the exact
// source commit.
//
// An LZMS block is read/written in 16-bit little-endian units from both
// ends: one logical bitstream (range-coded) starts at the front and reads
// forwards; another (Huffman-coded / verbatim bits) starts at the end and
// reads backwards. Both streams order the bits within each 16-bit unit
// high-order-first.

// ---------------------------------------------------------------------
// Range decoder
// ---------------------------------------------------------------------

type rangeDecoder struct {
	rng  uint32
	code uint32
	next int // byte offset into buf
	buf  []byte
}

func newRangeDecoder(buf []byte) *rangeDecoder {
	rd := &rangeDecoder{
		rng: 0xffffffff,
		buf: buf,
	}
	rd.code = uint32(le16(buf, 0))<<16 | uint32(le16(buf, 2))
	rd.next = 4
	return rd
}

func le16(b []byte, off int) uint16 {
	return uint16(b[off]) | uint16(b[off+1])<<8
}

// decodeBit decodes one bit using the probability entry selected by
// *statePtr (an index into probs, which is masked to numStates-1 states),
// then updates both the state and the probability entry.
func (rd *rangeDecoder) decodeBit(statePtr *uint32, numStates uint32, probs []probEntry) int {
	entry := &probs[*statePtr]

	*statePtr = (*statePtr << 1) & (numStates - 1)

	prob := entry.probability()

	if rd.rng&0xFFFF0000 == 0 {
		rd.rng <<= 16
		rd.code <<= 16
		if rd.next+1 < len(rd.buf) {
			rd.code |= uint32(le16(rd.buf, rd.next))
		}
		rd.next += 2
	}

	bound := (rd.rng >> probabilityBits) * prob

	if rd.code < bound {
		rd.rng = bound
		entry.update(0)
		return 0
	}
	rd.rng -= bound
	rd.code -= bound
	entry.update(1)
	*statePtr |= 1
	return 1
}

// ---------------------------------------------------------------------
// Range encoder
// ---------------------------------------------------------------------

type rangeEncoder struct {
	lowerBound uint64
	rangeSize  uint32
	cache      uint16
	cacheSize  uint32
	out        []byte
	started    bool
}

func newRangeEncoder() *rangeEncoder {
	return &rangeEncoder{
		rangeSize: 0xffffffff,
		cacheSize: 1,
	}
}

func (rc *rangeEncoder) shiftLow() {
	if uint32(rc.lowerBound) < 0xffff0000 || uint32(rc.lowerBound>>32) != 0 {
		carry := uint16(rc.lowerBound >> 32)
		for {
			if rc.started {
				rc.out = append(rc.out, byte(rc.cache+carry), byte((rc.cache+carry)>>8))
			}
			rc.started = true
			rc.cacheSize--
			if rc.cacheSize == 0 {
				break
			}
			rc.cache = 0xffff
		}
		rc.cache = uint16((rc.lowerBound >> 16) & 0xffff)
	}
	rc.cacheSize++
	rc.lowerBound = (rc.lowerBound & 0xffff) << 16
}

// encodeBit encodes bit using the probability entry selected by statePtr,
// updating both the state and the probability entry the same way the
// decoder does.
func (rc *rangeEncoder) encodeBit(bit int, statePtr *uint32, numStates uint32, probs []probEntry) {
	entry := &probs[*statePtr]

	*statePtr = ((*statePtr << 1) | uint32(bit)) & (numStates - 1)

	prob := entry.probability()
	entry.update(bit)

	if rc.rangeSize <= 0xffff {
		rc.rangeSize <<= 16
		rc.shiftLow()
	}

	bound := (rc.rangeSize >> probabilityBits) * prob
	if bit == 0 {
		rc.rangeSize = bound
	} else {
		rc.lowerBound += uint64(bound)
		rc.rangeSize -= bound
	}
}

// flush finalizes the range encoder's output. The first shiftLow() call
// made overall never actually writes a coding unit (mirroring wimlib's
// behavior of not counting the very first cache byte); this is handled by
// the 'started' flag above, which starts false and is set true right
// before the first byte would be written -- except wimlib's encoder
// actually *does* emit the first coding unit as real output (unlike the
// analogous LZMA construction, per the lzms_decompress.c file comment:
// "In LZMS, the first coding unit is not ignored by the decompressor").
// See lzms_range_encoder_shift_low's doc comment in lzms_compress.c.
func (rc *rangeEncoder) flush() {
	for i := 0; i < 4; i++ {
		rc.shiftLow()
	}
}

// ---------------------------------------------------------------------
// Backwards-reading input bitstream (Huffman symbols + verbatim bits)
// ---------------------------------------------------------------------

type inputBitstream struct {
	bitbuf   uint64
	bitsleft uint
	next     int // byte offset one-past the next 16-bit unit to read (reading backwards)
	begin    int
	buf      []byte
}

const bitbufNBits = 64

func newInputBitstream(buf []byte) *inputBitstream {
	return &inputBitstream{
		next:  len(buf),
		begin: 0,
		buf:   buf,
	}
}

func (is *inputBitstream) ensureBits(numBits uint) {
	if is.bitsleft >= numBits {
		return
	}
	avail := bitbufNBits - is.bitsleft
	if is.next != is.begin {
		is.next -= 2
		v := le16(is.buf, is.next)
		is.bitbuf |= uint64(v) << (avail - 16)
	}
	if is.next != is.begin {
		is.next -= 2
		v := le16(is.buf, is.next)
		is.bitbuf |= uint64(v) << (avail - 32)
	}
	is.bitsleft += 32
}

func (is *inputBitstream) peekBits(numBits uint) uint64 {
	return (is.bitbuf >> 1) >> (bitbufNBits - numBits - 1)
}

func (is *inputBitstream) removeBits(numBits uint) {
	is.bitbuf <<= numBits
	is.bitsleft -= numBits
}

func (is *inputBitstream) popBits(numBits uint) uint64 {
	bits := is.peekBits(numBits)
	is.removeBits(numBits)
	return bits
}

func (is *inputBitstream) readBits(numBits uint) uint32 {
	if numBits == 0 {
		return 0
	}
	is.ensureBits(numBits)
	return uint32(is.popBits(numBits))
}

// ---------------------------------------------------------------------
// Backwards-writing output bitstream
// ---------------------------------------------------------------------

// outputBitstream accumulates bits and, once flushed, must be reversed and
// appended after the range-coder output. We buffer 16-bit units in a slice
// in the order they are produced (which is the reverse of file order) and
// let the top-level encoder lay them out correctly.
type outputBitstream struct {
	bitbuf   uint64
	bitcount uint
	units    []uint16 // produced in back-to-front order
}

func newOutputBitstream() *outputBitstream {
	return &outputBitstream{}
}

// writeBits writes the low numBits bits of bits (numBits <= 32).
func (os *outputBitstream) writeBits(bits uint32, numBits uint) {
	if numBits == 0 {
		return
	}
	os.bitcount += numBits
	os.bitbuf = (os.bitbuf << numBits) | uint64(bits)
	for os.bitcount >= 16 {
		os.bitcount -= 16
		os.units = append(os.units, uint16(os.bitbuf>>os.bitcount))
	}
}

// flush emits any partial trailing unit (padded with zero low-order bits).
func (os *outputBitstream) flush() {
	if os.bitcount != 0 {
		os.units = append(os.units, uint16(os.bitbuf<<(16-os.bitcount)))
		os.bitcount = 0
	}
}

// bytes returns the bitstream's bytes in correct file order (i.e. as they
// must appear at the end of the compressed buffer, reading backwards from
// the very end).
func (os *outputBitstream) bytes() []byte {
	out := make([]byte, len(os.units)*2)
	// os.units[0] is the *last* 16-bit unit in the file (closest to the
	// end of the buffer); each later entry in os.units goes earlier in
	// the file. So reverse the order when laying out into a forward byte
	// slice representing "this chunk of the file, in file order".
	n := len(os.units)
	for i, u := range os.units {
		pos := (n - 1 - i) * 2
		out[pos] = byte(u)
		out[pos+1] = byte(u >> 8)
	}
	return out
}
