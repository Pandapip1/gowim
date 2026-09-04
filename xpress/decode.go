package xpress

import "fmt"

// Decompress decodes an XPRESS LZ77+Huffman compressed buffer produced by
// Compress (or by any other conforming XPRESS encoder, such as wimlib or
// WIMGAPI). expectedSize is the exact size of the decompressed output; it
// must be known ahead of time (from the WIM resource header, in the
// container-format caller), since -- like wimlib's decoder -- this decoder
// stops as soon as it has produced that many bytes rather than looking for
// an explicit end marker.
func Decompress(data []byte, expectedSize int) ([]byte, error) {
	if expectedSize < 0 {
		return nil, fmt.Errorf("xpress: negative expected size %d", expectedSize)
	}
	out := make([]byte, expectedSize)
	if err := DecompressInto(out, data); err != nil {
		return nil, err
	}
	return out, nil
}

// DecompressInto decodes data into dst, whose length is the exact expected
// decompressed size (see Decompress), instead of allocating a fresh buffer.
func DecompressInto(dst, data []byte) error {
	expectedSize := len(dst)
	if expectedSize == 0 {
		return nil
	}
	if len(data) < huffmanHeaderSize {
		return fmt.Errorf("xpress: compressed data too short: need at least %d bytes for the Huffman header, have %d", huffmanHeaderSize, len(data))
	}

	lens := unpackHuffmanLengths(data[:huffmanHeaderSize])
	dec := buildHuffmanDecoder(lens)

	r := newBitReader(data[huffmanHeaderSize:])
	pos := 0

	for pos < expectedSize {
		sym, ok := dec.decode(r)
		if !ok {
			return fmt.Errorf("xpress: invalid Huffman codeword at output offset %d", pos)
		}
		if sym < numChars {
			dst[pos] = byte(sym)
			pos++
			continue
		}

		lenHdr := sym & 0xf
		log2Offset := (sym >> 4) & 0xf

		r.ensureBits(16)
		offset := (1 << uint(log2Offset)) | int(r.popBits(uint32(log2Offset)))

		length := lenHdr
		if length == 0xf {
			length += int(r.readByte())
			if length == 0xf+0xff {
				length = int(r.readU16())
			}
		}
		length += minMatchLen

		if offset <= 0 || offset > pos {
			return fmt.Errorf("xpress: match offset %d invalid at output offset %d", offset, pos)
		}
		remaining := expectedSize - pos
		if length > remaining {
			// A conforming encoder never emits a match that overruns
			// expectedSize; if this happens the compressed data is
			// inconsistent with expectedSize.
			return fmt.Errorf("xpress: match length %d overruns expected size %d at output offset %d", length, expectedSize, pos)
		}
		xpressCopy(dst, pos, offset, length)
		pos += length
	}

	return nil
}

// xpressCopy performs an LZ77 match copy of length bytes from
// dst[pos-offset:] to dst[pos:]. It applies the same disjoint-vs-overlapping
// fast path as lzx's lzCopy (see lzx/decode.go): when offset >= length the
// source and destination ranges are disjoint, so a bulk copy (vectorized by
// the runtime) produces the same bytes as the byte-by-byte loop, just
// faster; when offset < length the ranges overlap (an RLE-style
// self-referential repeat) and each byte's source may itself have just been
// written by this same copy, so it must proceed one byte at a time.
func xpressCopy(dst []byte, pos, offset, length int) {
	src := pos - offset
	if offset >= length {
		copy(dst[pos:pos+length], dst[src:src+length])
		return
	}
	for k := 0; k < length; k++ {
		dst[pos+k] = dst[src+k]
	}
}
