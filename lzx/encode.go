package lzx

// compress implements this package's WIM-flavor LZX encoder. Per the scope
// documented in lzx.go, it:
//
//   - always emits exactly one block per call (valid since window orders
//     here never require more than 24 bits to represent the block size,
//     and 2^maxWindowOrder always fits) -- either VERBATIM or ALIGNED,
//     whichever encodes smaller for this chunk (see encodeBlock/
//     buildAlignedTable below); it never emits an uncompressed block;
//   - uses a one-step lazy binary-tree LZ77 match finder with a bounded
//     search depth, not a full optimal/DP parse, though it does track and
//     prefer the repeat-offset LRU queue (see matcher.go) -- both were real,
//     measured sources of gowim's compression-ratio gap against wimlib (see
//     gowim's own TODO.md);
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

	// Two-pass parse: pass 1 uses a flat, data-independent cost estimate
	// (costModel{}) to choose among candidate matches, since no real
	// Huffman codeword lengths exist yet. Its resulting token frequencies
	// build a first Huffman table, which is then used as pass 2's cost
	// model -- a refined, this-chunk's-real-code-informed re-parse. This
	// is a standard two-pass technique approximating the joint parse/code
	// optimization a full iterative optimal parser would do (see
	// matcher.go's costModel doc and gowim's own TODO.md for why this
	// package doesn't implement the latter).
	toks1 := findMatches(data, costModel{})
	mainLens1, lenLens1 := buildTables(toks1, nMainSyms)

	toks := findMatches(data, costModel{mainLens: mainLens1, lenLens: lenLens1})
	mainLens, lenLens := buildTables(toks, nMainSyms)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

	verbatim := encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, nil, nil)

	// Try an ALIGNED-offset block too: it costs 24 extra header bits (8
	// codeword lengths for the aligned code) but replaces the low 3 raw
	// extra-offset bits of every match at slot >= minAlignedOffsetSlot with
	// a (possibly cheaper, since it's Huffman-coded) aligned symbol. Since
	// the main/length trees are identical either way, simply encoding both
	// and keeping whichever is smaller is exact and correctness-preserving
	// -- no cost model or estimation needed, unlike the match-selection
	// heuristics above.
	alignedLens, alignedCodes := buildAlignedTable(toks)
	aligned := encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes)
	if len(aligned) < len(verbatim) {
		return aligned
	}
	return verbatim
}

// encodeBlock writes a single LZX block (VERBATIM if alignedLens/
// alignedCodes are nil, ALIGNED otherwise) for toks against the given main/
// length (and, for ALIGNED, aligned-offset) Huffman tables.
func encodeBlock(data []byte, order int, toks []token, mainLens, lenLens []byte, mainCodes, lenCodes []uint16, alignedLens []byte, alignedCodes []uint16) []byte {
	nMainSyms := numMainSyms(order)
	useAligned := alignedLens != nil

	w := newBitWriter()

	blockType := uint32(blockTypeVerbatim)
	if useAligned {
		blockType = blockTypeAligned
	}
	w.writeBits(blockType, 3)
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

	if useAligned {
		for _, l := range alignedLens {
			w.writeBits(uint32(l), alignedCodeElementSize)
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
			extraBits := int(lzxExtraOffsetBits[slot])
			if extraBits > 0 {
				extra := uint32(t.offset) - uint32(lzxOffsetSlotBase[slot])
				if useAligned && slot >= minAlignedOffsetSlot {
					rawBits := extraBits - numAlignedOffsetBits
					if rawBits > 0 {
						w.writeBits(extra>>numAlignedOffsetBits, uint(rawBits))
					}
					asym := extra & (alignedCodeNumSymbols - 1)
					w.writeBits(uint32(alignedCodes[asym]), uint(alignedLens[asym]))
				} else {
					w.writeBits(extra, uint(extraBits))
				}
			}
		}
	}

	return w.flush()
}

// buildAlignedTable computes the aligned-offset Huffman table (codeword
// lengths and codes for the low numAlignedOffsetBits bits of every fresh
// match's extra offset value, at slots >= minAlignedOffsetSlot) from toks'
// symbol frequencies. Repeat-offset matches never reach an aligned slot
// (their slots are always 0-2, well below minAlignedOffsetSlot), so only
// fresh matches contribute.
func buildAlignedTable(toks []token) (lens []byte, codes []uint16) {
	freqs := make([]uint32, alignedCodeNumSymbols)
	for _, t := range toks {
		if !t.isMatch || t.repeat >= 0 {
			continue
		}
		slot := offsetSlot(uint32(t.offset))
		if slot < minAlignedOffsetSlot {
			continue
		}
		extra := uint32(t.offset) - uint32(lzxOffsetSlotBase[slot])
		freqs[extra&(alignedCodeNumSymbols-1)]++
	}
	lens = buildLengths(freqs, maxAlignedCodewordLen)
	codes = canonicalCodewords(lens, maxAlignedCodewordLen)
	return lens, codes
}

// buildTables computes main/length Huffman codeword lengths from toks'
// symbol frequencies, per the LZX main/length code alphabets.
func buildTables(toks []token, nMainSyms int) (mainLens, lenLens []byte) {
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
	return buildLengths(mainFreqs, maxMainCodewordLen), buildLengths(lenFreqs, maxLenCodewordLen)
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
// codeword lengths via the precode: a single delta value (presym 0-16), a
// compressed run of consecutive zero-delta ("no change from prevLens")
// entries using precode symbol 17 (a run of 4-19) or 18 (a run of 20-51),
// or a short run of 4-5 consecutive *equal nonzero* deltas using precode
// symbol 19 (sym2 carries the shared delta value, itself precode-encoded
// from the same alphabet) -- matching wimlib's lzx_write_compressed_code /
// LZX_PRECODE_NUM_SYMBOLS run-length convention (this package's decoder
// already implements the read side -- see decode.go's readCodewordLens).
type codewordLenToken struct {
	presym int
	runLen int // meaningful when presym is 17, 18, or 19
	sym2   int // meaningful when presym is 19: the shared delta value (a plain 0-16 symbol)
}

func codewordLenTokens(deltas []byte) []codewordLenToken {
	var toks []codewordLenToken
	i := 0
	for i < len(deltas) {
		if deltas[i] == 0 {
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
			continue
		}

		// A run of 4-5 consecutive equal *nonzero* deltas collapses into 2
		// symbols (19 plus the shared delta) instead of 4-5 individual
		// symbols. Valid here specifically because this package's encoder
		// always transmits codeword lengths against an all-zero "previous
		// block" baseline (see writeCodewordLens' prevLens argument being
		// all-zero at every call site in compress()), so a run of equal
		// deltas is exactly a run of equal actual codeword lengths, which
		// is what symbol 19's decode side (decode.go's readCodewordLens,
		// case 19) assumes when it applies the same resolved length to
		// every position in the run.
		j := i
		for j < len(deltas) && deltas[j] == deltas[i] {
			j++
		}
		run := j - i
		if run >= 4 {
			rl := 5
			if run < 5 {
				rl = 4
			}
			toks = append(toks, codewordLenToken{presym: 19, runLen: rl, sym2: int(deltas[i])})
			i += rl
			continue
		}

		toks = append(toks, codewordLenToken{presym: int(deltas[i])})
		i++
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
		if t.presym == 19 {
			// sym2 is itself precode-encoded from the same alphabet (see
			// decode.go's readCodewordLens, case 19), so it needs its own
			// frequency contribution alongside every top-level symbol.
			freqs[t.sym2]++
		}
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
		case 19:
			w.writeBits(uint32(t.runLen-4), 1)
			w.writeBits(uint32(precodeCodes[t.sym2]), uint(precodeLens[t.sym2]))
		}
	}
}
