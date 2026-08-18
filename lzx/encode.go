package lzx

import "sync"

// compress implements this package's WIM-flavor LZX encoder. Per the scope
// documented in lzx.go, it:
//
//   - emits 1 or 2 blocks per call (never more -- see trySplitChunk for
//     why this is a single bounded split attempt, not a general search),
//     each independently VERBATIM or ALIGNED, whichever encodes smaller
//     (see encodeBlock/writeBlockInto/buildAlignedTable below); it never
//     emits an uncompressed block;
//   - uses a bounded 3-way-lookahead binary-tree LZ77 match finder, plus a
//     bounded multi-state beam DP parse tried as an alternative (see
//     optimal.go's findMatchesOptimal, kept only if smaller) -- both
//     bounded search depth, not a full optimal/DP parse with unbounded
//     repeat-offset-state exploration (see optimal.go's own doc for the
//     precise gap between this and wimlib's lzx_compress_near_optimal),
//     though both do track and prefer the repeat-offset LRU queue (see
//     matcher.go) -- real, measured sources of gowim's compression-ratio
//     gap against wimlib (see gowim's own TODO.md);
//   - runs the bounded-lookahead parse and the DP parse concurrently (see
//     the goroutines below): both only depend on pass 1's tables, not on
//     each other, and the DP parse in particular is real, measured extra
//     CPU time (see gowim's own TODO.md) that's worth overlapping with the
//     lookahead parse rather than paying serially. This is independent of,
//     and stacks with, the outer per-chunk/per-blob parallelism in the
//     `wim` package (see gowim's own TODO.md's "Performance: concurrency
//     opportunities" section) -- that parallelizes *across* chunks/blobs;
//     this parallelizes *within* a single chunk's own compress() call, so
//     it still helps even when there's only one chunk to compress, or
//     when the outer parallelism is already saturating all cores (this
//     adds real CPU-time cost, not just wall-clock latency, in that case);
//   - applies the E8 call-translation filter unconditionally, exactly as
//     real WIM encoders do (see lzx.go's WIM-vs-CAB notes), so that this
//     package's own compressed output round-trips through a real WIM/LZX
//     decoder (e.g. wimlib) as well as through this package's own
//     Decompress.
func compress(input []byte) []byte {
	// Work on a private copy: E8 preprocessing mutates the buffer in place,
	// and the caller must not see their slice modified. Both goroutines
	// below only ever read data/order/nMainSyms after this point -- no
	// shared mutable state, so no locking is needed between them.
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
	pass1Model := costModel{mainLens: mainLens1, lenLens: lenLens1}

	// The bounded-lookahead parse (and everything downstream of it:
	// ALIGNED/split trials) and the DP parse are independent given
	// pass1Model, so run them concurrently rather than serially -- the DP
	// parse in particular is the most expensive single step in compress()
	// (see gowim's own TODO.md's measured time cost), making this the
	// highest-value place in the encoder to overlap work.
	var lookaheadBest, optBest []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		lookaheadBest = compressLookahead(data, order, nMainSyms, pass1Model)
	}()
	go func() {
		defer wg.Done()
		optBest = compressOptimal(data, order, nMainSyms, pass1Model)
	}()
	wg.Wait()

	best := lookaheadBest
	if len(optBest) < len(best) {
		best = optBest
	}
	return best
}

// compressLookahead runs findMatches' bounded-lookahead parse (pass 2,
// refining pass1Model) and returns the smallest of its VERBATIM, ALIGNED,
// and 2-block-split encodings -- the non-DP half of compress()'s work,
// split out so it can run concurrently with compressOptimal (see compress
// above).
func compressLookahead(data []byte, order, nMainSyms int, pass1Model costModel) []byte {
	toks := findMatches(data, pass1Model)
	mainLens, lenLens := buildTables(toks, nMainSyms)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

	// VERBATIM, ALIGNED, and the 2-block split trial are all independent
	// given toks/mainLens/lenLens/mainCodes/lenCodes (none reads another's
	// output), so run them concurrently rather than serially. The split
	// trial does meaningfully more work than the other two (it rebuilds
	// its own per-half tables and tries VERBATIM-vs-ALIGNED per half --
	// see trySplitChunk, itself further parallelized internally), so it's
	// the one most worth overlapping with the other two's cheaper,
	// already-built-table encodeBlock calls.
	var verbatim, aligned, split []byte
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		verbatim = encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, nil, nil)
	}()
	go func() {
		defer wg.Done()
		// Try an ALIGNED-offset block too: it costs 24 extra header bits
		// (8 codeword lengths for the aligned code) but replaces the low
		// 3 raw extra-offset bits of every match at slot >=
		// minAlignedOffsetSlot with a (possibly cheaper, since it's
		// Huffman-coded) aligned symbol. Since the main/length trees are
		// identical either way, simply encoding both and keeping
		// whichever is smaller is exact and correctness-preserving -- no
		// cost model or estimation needed, unlike the match-selection
		// heuristics above.
		alignedLens, alignedCodes := buildAlignedTable(toks)
		aligned = encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes)
	}()
	go func() {
		defer wg.Done()
		// Try splitting into 2 blocks too: real data's statistics can vary
		// enough within one 32768-byte chunk (e.g. text vs binary content)
		// that giving each half its own Huffman table beats one table for
		// the whole chunk, even after paying a second block's header
		// overhead. See trySplitChunk for why this is a single, bounded
		// 2-block attempt (not a general multi-way search) and gowim's
		// own TODO.md for how much (or little) this actually saved on
		// real content.
		split = trySplitChunk(data, order, toks, nMainSyms)
	}()
	wg.Wait()

	best := verbatim
	if len(aligned) < len(best) {
		best = aligned
	}
	if split != nil && len(split) < len(best) {
		best = split
	}
	return best
}

// compressOptimal runs the bounded multi-state beam DP parse
// (findMatchesOptimal in optimal.go), using the same pass-1-informed cost
// model as compressLookahead, and returns its VERBATIM encoding -- the DP
// half of compress()'s work, split out so it can run concurrently with
// compressLookahead (see compress above). findMatchesOptimal's beam-width
// cap means it is NOT guaranteed to beat the bounded lookahead on every
// input (see optimal.go's doc for the precise scope/limitation), so
// compress() keeps whichever of the two is actually smaller rather than
// assuming this one wins.
func compressOptimal(data []byte, order, nMainSyms int, pass1Model costModel) []byte {
	optToks := findMatchesOptimal(data, pass1Model)
	optMainLens, optLenLens := buildTables(optToks, nMainSyms)
	optMainCodes := canonicalCodewords(optMainLens, maxMainCodewordLen)
	optLenCodes := canonicalCodewords(optLenLens, maxLenCodewordLen)
	return encodeBlock(data, order, optToks, optMainLens, optLenLens, optMainCodes, optLenCodes, nil, nil)
}

// tokensByteLen returns the total uncompressed byte length covered by toks.
func tokensByteLen(toks []token) int {
	n := 0
	for _, t := range toks {
		if t.isMatch {
			n += t.length
		} else {
			n++
		}
	}
	return n
}

// trySplitChunk attempts encoding data as 2 LZX blocks instead of 1, split
// at the token boundary closest to the chunk's midpoint (so the split never
// truncates a match -- matches may not cross a block boundary, see
// decode.go's lzCopy). Each block gets its own Huffman tables (main/length,
// and its own independent VERBATIM-vs-ALIGNED trial), with the second
// block's tables delta-coded against the first's actual lengths (not an
// all-zero baseline -- see writeBlockInto). Returns nil if no meaningful
// split point exists (e.g. a single token spans the whole chunk).
//
// This tries exactly one split point, not a general search over every
// possible number/position of blocks: real near-optimal encoders (e.g.
// wimlib's) decide block splits via an iterative cost-based search across
// many candidate boundaries, which is a substantially bigger undertaking
// than justified here without first checking whether even one bounded
// split point captures a meaningful share of the benefit -- see gowim's
// own TODO.md for the measured result.
func trySplitChunk(data []byte, order int, toks []token, nMainSyms int) []byte {
	target := len(data) / 2
	pos := 0
	splitIdx := -1
	for idx, t := range toks {
		tl := 1
		if t.isMatch {
			tl = t.length
		}
		if pos+tl > target {
			if target-pos <= pos+tl-target {
				splitIdx = idx
			} else {
				splitIdx = idx + 1
			}
			break
		}
		pos += tl
	}
	if splitIdx <= 0 || splitIdx >= len(toks) {
		return nil
	}

	first := toks[:splitIdx]
	second := toks[splitIdx:]
	splitByte := tokensByteLen(first)
	if splitByte <= 0 || splitByte >= len(data) {
		return nil
	}
	firstData := data[:splitByte]
	secondData := data[splitByte:]

	// The two halves' table-building and VERBATIM-vs-ALIGNED decisions are
	// completely independent of each other (each only reads its own
	// half's data/tokens), so run them concurrently in two goroutines
	// rather than one after the other.
	zeros := make([]byte, nMainSyms)
	zerosLen := make([]byte, lenCodeNumSymbols)

	// Decide VERBATIM vs ALIGNED for one half, using the existing
	// standalone single-block encoder for the comparison (same byte-length
	// comparison the whole-chunk case above already uses).
	chooseAligned := func(blkData []byte, blkToks []token, mainLens, lenLens []byte, mainCodes, lenCodes []uint16) (bool, []byte, []uint16) {
		v := encodeBlock(blkData, order, blkToks, mainLens, lenLens, mainCodes, lenCodes, nil, nil)
		aLens, aCodes := buildAlignedTable(blkToks)
		a := encodeBlock(blkData, order, blkToks, mainLens, lenLens, mainCodes, lenCodes, aLens, aCodes)
		return len(a) < len(v), aLens, aCodes
	}

	var mainLens1, lenLens1 []byte
	var mainCodes1, lenCodes1 []uint16
	var useAligned1 bool
	var alignedLens1 []byte
	var alignedCodes1 []uint16

	var mainLens2, lenLens2 []byte
	var mainCodes2, lenCodes2 []uint16
	var useAligned2 bool
	var alignedLens2 []byte
	var alignedCodes2 []uint16

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mainLens1, lenLens1 = buildTables(first, nMainSyms)
		mainCodes1 = canonicalCodewords(mainLens1, maxMainCodewordLen)
		lenCodes1 = canonicalCodewords(lenLens1, maxLenCodewordLen)
		useAligned1, alignedLens1, alignedCodes1 = chooseAligned(firstData, first, mainLens1, lenLens1, mainCodes1, lenCodes1)
	}()
	go func() {
		defer wg.Done()
		mainLens2, lenLens2 = buildTables(second, nMainSyms)
		mainCodes2 = canonicalCodewords(mainLens2, maxMainCodewordLen)
		lenCodes2 = canonicalCodewords(lenLens2, maxLenCodewordLen)
		useAligned2, alignedLens2, alignedCodes2 = chooseAligned(secondData, second, mainLens2, lenLens2, mainCodes2, lenCodes2)
	}()
	wg.Wait()

	if !useAligned1 {
		alignedLens1, alignedCodes1 = nil, nil
	}
	if !useAligned2 {
		alignedLens2, alignedCodes2 = nil, nil
	}

	w := newBitWriter()
	writeBlockInto(w, firstData, order, first, mainLens1, zeros, lenLens1, zerosLen, mainCodes1, lenCodes1, alignedLens1, alignedCodes1)
	writeBlockInto(w, secondData, order, second, mainLens2, mainLens1, lenLens2, lenLens1, mainCodes2, lenCodes2, alignedLens2, alignedCodes2)
	return w.flush()
}

// encodeBlock writes a single, standalone LZX block (VERBATIM if
// alignedLens/alignedCodes are nil, ALIGNED otherwise) for toks against the
// given main/length (and, for ALIGNED, aligned-offset) Huffman tables,
// delta-coded against an all-zero "previous block" baseline. This is the
// single-block case (compress() always uses it for whole-chunk encoding);
// see writeBlockInto for the shared multi-block-capable core, used when
// splitting a chunk into more than one block (see trySplitChunk).
func encodeBlock(data []byte, order int, toks []token, mainLens, lenLens []byte, mainCodes, lenCodes []uint16, alignedLens []byte, alignedCodes []uint16) []byte {
	nMainSyms := numMainSyms(order)
	zeros := make([]byte, nMainSyms)
	zerosLen := make([]byte, lenCodeNumSymbols)
	w := newBitWriter()
	writeBlockInto(w, data, order, toks, mainLens, zeros, lenLens, zerosLen, mainCodes, lenCodes, alignedLens, alignedCodes)
	return w.flush()
}

// writeBlockInto writes a single LZX block (VERBATIM if alignedLens/
// alignedCodes are nil, ALIGNED otherwise) into an existing, possibly
// already-in-progress bitWriter w, with codeword lengths delta-coded
// against prevMainLens/prevLenLens -- the *actual* previous block's
// lengths when chaining multiple blocks into one continuous bitstream (LZX
// blocks are not byte-aligned relative to each other; only an UNCOMPRESSED
// block realigns -- see decode.go), or an all-zero baseline for the first
// (or only) block in a chunk.
func writeBlockInto(w *bitWriter, data []byte, order int, toks []token, mainLens, prevMainLens, lenLens, prevLenLens []byte, mainCodes, lenCodes []uint16, alignedLens []byte, alignedCodes []uint16) {
	nMainSyms := numMainSyms(order)
	useAligned := alignedLens != nil

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

	// Codeword length tables, delta-coded against prevMainLens/prevLenLens.
	writeCodewordLens(w, mainLens[:numChars], prevMainLens[:numChars])
	writeCodewordLens(w, mainLens[numChars:], prevMainLens[numChars:nMainSyms])
	writeCodewordLens(w, lenLens, prevLenLens[:lenCodeNumSymbols])

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
