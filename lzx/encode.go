package lzx

// compress implements this package's WIM-flavor LZX encoder. Per the scope
// documented in lzx.go, it:
//
//   - always emits exactly one LZX_BLOCKTYPE_VERBATIM block per call
//     (valid since window orders here never require more than 24 bits to
//     represent the block size, and 2^maxWindowOrder always fits);
//   - never emits an "aligned offset" tree or an uncompressed block;
//   - uses a simple greedy hash-chain LZ77 match finder with a bounded
//     search depth, not an optimal parse, though it does track and prefer
//     the repeat-offset LRU queue (see matcher.go) since that is nearly
//     free to do in a greedy parser and a real, measured source of gowim's
//     compression-ratio gap against wimlib (see gowim's own TODO.md);
//   - applies the E8 call-translation filter unconditionally, exactly as
//     real WIM encoders do (see lzx.go's WIM-vs-CAB notes), so that this
//     package's own compressed output round-trips through a real WIM/LZX
//     decoder (e.g. wimlib) as well as through this package's own
//     Decompress.
func compress(input []byte) []byte {
	// Work on a private copy: E8 preprocessing mutates the buffer in place,
	// and the caller must not see their slice modified.
	data := make([]byte, len(input))
	copy(data, input)
	lzxPreprocess(data)

	order, err := windowOrder(len(data))
	if err != nil {
		// Compress already validated len(data) <= maxWindowSize.
		panic(err)
	}
	nMainSyms := numMainSyms(order)

	toks := findMatches(data)

	mainFreqs := make([]uint32, nMainSyms)
	lenFreqs := make([]uint32, lenCodeNumSymbols)
	for _, t := range toks {
		if t.isMatch {
			slot := t.repeat
			if slot < 0 {
				slot = offsetSlot(uint32(t.offset))
			}
			lengthField := t.length - minMatchLen
			header := lengthField
			if header > numPrimaryLens {
				header = numPrimaryLens
			}
			mainFreqs[numChars+slot*numLenHeaders+header]++
			if header == numPrimaryLens {
				lenFreqs[lengthField-numPrimaryLens]++
			}
		} else {
			mainFreqs[t.literal]++
		}
	}

	mainLens := buildLengths(mainFreqs, maxMainCodewordLen)
	lenLens := buildLengths(lenFreqs, maxLenCodewordLen)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

	w := newBitWriter()

	// Block header: type (VERBATIM), block size.
	w.writeBits(blockTypeVerbatim, 3)
	if len(data) == defaultBlockSize {
		w.writeBits(1, 1)
	} else {
		w.writeBits(0, 1)
		if order >= 16 {
			w.writeBits(uint32(len(data)), 24)
		} else {
			w.writeBits(uint32(len(data)), 16)
		}
	}

	// Codeword length tables, delta-coded against all-zero "previous"
	// lengths (this package always emits exactly one block per call, so
	// there is no real previous block to delta against).
	zeros := make([]byte, nMainSyms)
	writeCodewordLens(w, mainLens[:numChars], zeros[:numChars])
	writeCodewordLens(w, mainLens[numChars:], zeros[numChars:nMainSyms])
	writeCodewordLens(w, lenLens, zeros[:lenCodeNumSymbols])

	// Literals and matches.
	for _, t := range toks {
		if !t.isMatch {
			w.writeBits(uint32(mainCodes[t.literal]), uint(mainLens[t.literal]))
			continue
		}
		slot := t.repeat
		if slot < 0 {
			slot = offsetSlot(uint32(t.offset))
		}
		lengthField := t.length - minMatchLen
		header := lengthField
		if header > numPrimaryLens {
			header = numPrimaryLens
		}
		mainSym := numChars + slot*numLenHeaders + header
		w.writeBits(uint32(mainCodes[mainSym]), uint(mainLens[mainSym]))
		if header == numPrimaryLens {
			lsym := lengthField - numPrimaryLens
			w.writeBits(uint32(lenCodes[lsym]), uint(lenLens[lsym]))
		}
		// Repeat-offset matches (t.repeat >= 0) need no extra offset bits:
		// lzxExtraOffsetBits[0..2] are all 0, and the decoder reads the
		// offset straight out of its recentOffsets queue for these slots
		// (see decode.go) rather than from the bitstream.
		if t.repeat < 0 {
			extraBits := lzxExtraOffsetBits[slot]
			if extraBits > 0 {
				extra := uint32(t.offset) - uint32(lzxOffsetSlotBase[slot])
				w.writeBits(extra, uint(extraBits))
			}
		}
	}

	return w.flush()
}

// offsetSlot returns the offset slot (>= 3) whose range
// [lzxOffsetSlotBase[slot], lzxOffsetSlotBase[slot+1]) contains a *fresh*
// (non-repeat-offset) match offset. Repeat-offset matches (slots 0-2) are
// resolved directly by the matcher/encoder from the recent-offsets queue and
// never go through this function -- see token.repeat and matcher.go.
func offsetSlot(offset uint32) int {
	// Linear scan is fine: at most maxOffsetSlots (50) entries, called once
	// per match.
	slot := 3
	for slot+1 < len(lzxOffsetSlotBase) && lzxOffsetSlotBase[slot+1] <= int32(offset) {
		slot++
	}
	return slot
}

// codewordLenToken is one symbol emitted while transmitting a run of
// codeword lengths via the precode: either a single delta value (presym
// 0-16) or a compressed run of consecutive zero-delta ("no change from
// prevLens") entries using precode symbol 17 (a run of 4-19) or 18 (a run
// of 20-51), matching wimlib's lzx_write_compressed_code /
// LZX_PRECODE_NUM_SYMBOLS run-length convention (this package's decoder
// already implements the read side -- see decode.go's readCodewordLens).
// Symbol 19 (a short run of identical *nonzero* deltas) is not produced
// here: it is a secondary optimization on top of this one, of much smaller
// value, and is left for a future pass -- see gowim's own TODO.md.
type codewordLenToken struct {
	presym int
	runLen int // only meaningful when presym is 17 or 18
}

func codewordLenTokens(deltas []byte) []codewordLenToken {
	var toks []codewordLenToken
	i := 0
	for i < len(deltas) {
		if deltas[i] != 0 {
			toks = append(toks, codewordLenToken{presym: int(deltas[i])})
			i++
			continue
		}
		j := i
		for j < len(deltas) && deltas[j] == 0 {
			j++
		}
		run := j - i
		for run >= 4 {
			if run >= 20 {
				l := run
				if l > 51 {
					l = 51
				}
				toks = append(toks, codewordLenToken{presym: 18, runLen: l})
				run -= l
			} else {
				toks = append(toks, codewordLenToken{presym: 17, runLen: run})
				run = 0
			}
		}
		for k := 0; k < run; k++ {
			toks = append(toks, codewordLenToken{presym: 0})
		}
		i = j
	}
	return toks
}

// writeCodewordLens writes a precode-compressed representation of lens
// (delta-coded against prevLens, mod 17) to w: the raw 4-bit precode
// codeword lengths, followed by one precode-encoded symbol per element of
// lens (or one symbol for a whole run of consecutive zero-delta elements,
// via codewordLenTokens above -- real WIM chunks routinely have most of
// their ~256-500 symbol main alphabet unused, and this is the dominant
// saving for such chunks; see gowim's own TODO.md for the ground-truthed
// comparison against wimlib that found this).
func writeCodewordLens(w *bitWriter, lens, prevLens []byte) {
	deltas := make([]byte, len(lens))
	for i, l := range lens {
		d := int(prevLens[i]) - int(l)
		if d < 0 {
			d += 17
		}
		deltas[i] = byte(d)
	}

	toks := codewordLenTokens(deltas)

	freqs := make([]uint32, precodeNumSymbols)
	for _, t := range toks {
		freqs[t.presym]++
	}
	precodeLens := buildLengths(freqs, maxPrecodeCodewordLen)
	precodeCodes := canonicalCodewords(precodeLens, maxPrecodeCodewordLen)

	for _, l := range precodeLens {
		w.writeBits(uint32(l), precodeElementSize)
	}
	for _, t := range toks {
		w.writeBits(uint32(precodeCodes[t.presym]), uint(precodeLens[t.presym]))
		switch t.presym {
		case 17:
			w.writeBits(uint32(t.runLen-4), 4)
		case 18:
			w.writeBits(uint32(t.runLen-20), 5)
		}
	}
}
