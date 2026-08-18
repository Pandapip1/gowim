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

// writeCodewordLens writes a precode-compressed representation of lens
// (delta-coded against prevLens, mod 17) to w: the raw 4-bit precode
// codeword lengths, followed by one precode-encoded delta symbol per
// element of lens. Unlike wimlib's encoder, this does not use the
// precode's run-length symbols (17/18/19) -- every length is sent
// individually -- which is a valid (if less compact) subset of the format,
// consistent with this package's scope of prioritizing simplicity over
// optimal compression ratio.
func writeCodewordLens(w *bitWriter, lens, prevLens []byte) {
	deltas := make([]byte, len(lens))
	for i, l := range lens {
		d := int(prevLens[i]) - int(l)
		if d < 0 {
			d += 17
		}
		deltas[i] = byte(d)
	}

	freqs := make([]uint32, precodeNumSymbols)
	for _, d := range deltas {
		freqs[d]++
	}
	precodeLens := buildLengths(freqs, maxPrecodeCodewordLen)
	precodeCodes := canonicalCodewords(precodeLens, maxPrecodeCodewordLen)

	for _, l := range precodeLens {
		w.writeBits(uint32(l), precodeElementSize)
	}
	for _, d := range deltas {
		w.writeBits(uint32(precodeCodes[d]), uint(precodeLens[d]))
	}
}
