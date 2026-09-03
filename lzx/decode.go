package lzx

import "fmt"

// decode implements the LZX WIM-flavor decompression algorithm, ported from
// wimlib's src/lzx_decompress.c (lzx_decompress / lzx_decompress_block /
// lzx_read_block_header / lzx_read_codeword_lens). See lzx.go for the
// overall scope and source citations.
func decompress(data []byte, expectedSize int) ([]byte, error) {
	order, err := windowOrder(expectedSize)
	if err != nil {
		return nil, err
	}
	nMainSyms := numMainSyms(order)
	nOffsetSlots := numOffsetSlots(order)

	out := make([]byte, expectedSize)
	pos := 0

	r := newBitReader(data)

	// Codeword lengths persist across blocks within a single Decompress
	// call (but always start at all-zero at the start of the call), per
	// lzx_read_codeword_lens' delta encoding relative to the previous
	// block's lengths.
	mainLens := make([]byte, nMainSyms)
	lenLens := make([]byte, lenCodeNumSymbols)

	recentOffsets := [numRecentOffsets]uint32{1, 1, 1}
	sawE8Literal := false

	for pos != expectedSize {
		blockType := int(r.readBits(3))
		var blockSize int
		if r.readBits(1) == 1 {
			blockSize = defaultBlockSize
		} else if order >= 16 {
			blockSize = int(r.readBits(24))
		} else {
			blockSize = int(r.readBits(16))
		}
		if blockSize < 1 || blockSize > expectedSize-pos {
			return nil, fmt.Errorf("lzx: %w: invalid block size", ErrInvalidData)
		}
		switch blockType {
		case blockTypeVerbatim, blockTypeAligned:
			var alignedLens [alignedCodeNumSymbols]byte
			if blockType == blockTypeAligned {
				for i := range alignedLens {
					alignedLens[i] = byte(r.readBits(alignedCodeElementSize))
				}
			}

			if err := readCodewordLens(r, mainLens[:numChars]); err != nil {
				return nil, err
			}
			if err := readCodewordLens(r, mainLens[numChars:]); err != nil {
				return nil, err
			}
			if err := readCodewordLens(r, lenLens); err != nil {
				return nil, err
			}

			mainDec := newHuffDecoder(mainLens, maxMainCodewordLen)
			lenDec := newHuffDecoder(lenLens, maxLenCodewordLen)
			var alignedDec *huffDecoder
			minAlignedSlot := nOffsetSlots // effectively disables aligned-slot handling
			if blockType == blockTypeAligned {
				alignedDec = newHuffDecoder(alignedLens[:], maxAlignedCodewordLen)
				minAlignedSlot = minAlignedOffsetSlot
			}

			blockEnd := pos + blockSize
			for pos != blockEnd {
				mainSym, ok := mainDec.decode(r)
				if !ok {
					return nil, fmt.Errorf("lzx: %w: bad main symbol", ErrInvalidData)
				}
				if int(mainSym) < numChars {
					out[pos] = byte(mainSym)
					pos++
					if mainSym == 0xE8 {
						sawE8Literal = true
					}
					continue
				}

				length := int(mainSym) % numLenHeaders
				offsetSlot := (int(mainSym) - numChars) / numLenHeaders
				if offsetSlot >= nOffsetSlots {
					return nil, fmt.Errorf("lzx: %w: offset slot out of range", ErrInvalidData)
				}

				if length == numPrimaryLens {
					lsym, ok := lenDec.decode(r)
					if !ok {
						return nil, fmt.Errorf("lzx: %w: bad length symbol", ErrInvalidData)
					}
					length += int(lsym)
				}
				length += minMatchLen

				var offset uint32
				if offsetSlot < numRecentOffsets {
					offset = recentOffsets[offsetSlot]
					recentOffsets[offsetSlot] = recentOffsets[0]
				} else {
					extra := lzxExtraOffsetBits[offsetSlot]
					useAligned := offsetSlot >= minAlignedSlot
					if useAligned {
						// The low numAlignedOffsetBits bits are supplied by
						// the aligned Huffman code instead of raw bits.
						extra -= numAlignedOffsetBits
					}
					offset = r.readBits(uint(extra))
					if useAligned {
						asym, ok := alignedDec.decode(r)
						if !ok {
							return nil, fmt.Errorf("lzx: %w: bad aligned symbol", ErrInvalidData)
						}
						offset = (offset << numAlignedOffsetBits) | uint32(asym)
					}
					offset += uint32(lzxOffsetSlotBase[offsetSlot])
					recentOffsets[2] = recentOffsets[1]
					recentOffsets[1] = recentOffsets[0]
				}
				recentOffsets[0] = offset

				if err := lzCopy(out, pos, int(offset), length, blockEnd); err != nil {
					return nil, err
				}
				pos += length
			}
			sawE8Literal = sawE8Literal || mainLens[0xE8] != 0

		case blockTypeUncompressed:
			// Realign to the next 16-bit coding-unit boundary (discarding
			// buffered bits), then read the recent-offsets queue as three
			// raw little-endian u32 values.
			r.align()
			recentOffsets[0] = r.readU32()
			recentOffsets[1] = r.readU32()
			recentOffsets[2] = r.readU32()
			if recentOffsets[0] == 0 || recentOffsets[1] == 0 || recentOffsets[2] == 0 {
				return nil, fmt.Errorf("lzx: %w: zero recent offset in uncompressed block", ErrInvalidData)
			}
			if !r.readBytes(out[pos : pos+blockSize]) {
				return nil, fmt.Errorf("lzx: %w: truncated uncompressed block", ErrInvalidData)
			}
			if blockSize&1 != 0 {
				r.readByte()
			}
			pos += blockSize
			sawE8Literal = true

		default:
			return nil, fmt.Errorf("lzx: %w: bad block type %d", ErrInvalidData, blockType)
		}
	}

	if sawE8Literal {
		lzxPostprocess(out)
	}

	return out, nil
}

// readCodewordLens reads a precode from r, then uses it to decode
// len(lens) codeword-length values into lens, applying them as deltas
// (mod 17) against the previous contents of lens (which the caller has
// primed with the previous block's lengths, or all-zero for the first
// block), per lzx_read_codeword_lens.
func readCodewordLens(r *bitReader, lens []byte) error {
	var precodeLens [precodeNumSymbols]byte
	for i := range precodeLens {
		precodeLens[i] = byte(r.readBits(precodeElementSize))
	}
	dec := newHuffDecoder(precodeLens[:], maxPrecodeCodewordLen)

	i := 0
	for i < len(lens) {
		presym, ok := dec.decode(r)
		if !ok {
			return fmt.Errorf("lzx: %w: bad precode symbol", ErrInvalidData)
		}
		if presym < 17 {
			delta := int(presym)
			l := int(lens[i]) - delta
			if l < 0 {
				l += 17
			}
			lens[i] = byte(l)
			i++
			continue
		}

		var runLen, l int
		switch presym {
		case 17:
			runLen = 4 + int(r.readBits(4))
			l = 0
		case 18:
			runLen = 20 + int(r.readBits(5))
			l = 0
		case 19:
			runLen = 4 + int(r.readBits(1))
			presym2, ok := dec.decode(r)
			if !ok || presym2 > 17 {
				return fmt.Errorf("lzx: %w: bad precode run symbol", ErrInvalidData)
			}
			delta := int(presym2)
			l = int(lens[i]) - delta
			if l < 0 {
				l += 17
			}
		default:
			return fmt.Errorf("lzx: %w: bad precode symbol %d", ErrInvalidData, presym)
		}
		for ; runLen > 0 && i < len(lens); runLen-- {
			lens[i] = byte(l)
			i++
		}
	}
	return nil
}

// lzCopy validates and performs an LZ77 match copy of length bytes from
// out[pos-offset:] to out[pos:], bounded by blockEnd (matches may not cross
// a block boundary, matching lz_copy()'s out_end argument being the current
// block's end rather than the whole buffer's).
func lzCopy(out []byte, pos, offset, length, blockEnd int) error {
	if offset <= 0 || offset > pos {
		return fmt.Errorf("lzx: %w: match offset out of range", ErrInvalidData)
	}
	if length < 0 || pos+length > blockEnd {
		return fmt.Errorf("lzx: %w: match length out of range", ErrInvalidData)
	}
	src := pos - offset
	if offset >= length {
		// Source and destination ranges are disjoint, so a bulk copy
		// (vectorized by the runtime) produces the same bytes as the
		// byte-by-byte loop below, just faster. When offset < length
		// the ranges overlap (an RLE-style self-referential repeat)
		// and each byte's source may itself have just been written by
		// this same copy, so it must proceed one byte at a time.
		copy(out[pos:pos+length], out[src:src+length])
		return nil
	}
	for i := 0; i < length; i++ {
		out[pos+i] = out[src+i]
	}
	return nil
}
