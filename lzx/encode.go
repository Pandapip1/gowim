package lzx

import "sync"

// compress implements this package's WIM-flavor LZX encoder. Per the scope
// documented in lzx.go, it:
//
//   - emits one or more blocks per call: a single bounded midpoint split
//     (trySplitChunk) and wimlib's own real, statistics-driven multi-way
//     block-splitting heuristic (splitstats.go's trySplitChunkStats) are
//     both tried alongside the whole-chunk VERBATIM/ALIGNED candidates,
//     keeping whichever encodes smallest -- not a from-scratch general
//     block-split search; each block independently VERBATIM or ALIGNED
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

// maxRefineIters bounds refineParse's extra re-parse rounds beyond the
// original pass 2 (see refineParse's own doc for why this is a fixed-point
// iteration expected to converge quickly, not an open-ended search).
// Measured on the real 398-chunk ntoskrnl.exe benchmark (see gowim's own
// TODO.md): chunks use an average of well under 1 of these rounds before
// converging (a round that doesn't measurably help stops iteration
// immediately), so this cap is rarely fully exhausted in practice -- but
// every round, converging or not, costs one more full findMatches call,
// roughly doubling compressLookahead's own real-world time cost end to
// end (since a chunk that doesn't improve still pays for the one round
// that checked).
const maxRefineIters = 4

// refineParse runs findMatches' bounded-lookahead parse repeatedly,
// feeding each round's own resulting real Huffman table back in as the
// next round's cost model -- generalizing this package's existing
// "pass 1 (flat cost) -> pass 2 (real cost from pass 1's table)" two-pass
// technique into a fixed-point iteration: pass 2's real table informs a
// pass 3 re-parse, pass 3's table informs pass 4, and so on, each round
// asking the same match-finder to re-decide every literal/match choice
// against a progressively more self-consistent table, rather than
// stopping after one refinement.
//
// This is the natural generalization of the hash2 greedy pass's
// "rebuild the real table, don't trust a stale one" lesson to *all*
// matches, not just length-2 ones: unlike hash2Greedy's candidate splice
// (safe to do incrementally because a hash2 substitution never overlaps
// an existing match's byte range), a general match candidate can overlap
// arbitrarily with its neighbors, so there is no cheap local "accept one
// candidate, keep the rest" step here -- each round instead re-derives
// the *entire* parse fresh against the latest table, the same shape as
// this package's original pass1->pass2 step, just iterated instead of
// stopping at one. Each round's real encoded byte count is measured
// directly (encodeBlock, not the token-implied estimate), and iteration
// stops as soon as a round fails to beat the previous best -- both
// because further rounds rarely help once the table has converged, and
// because this keeps the same "try both, keep smaller" safety property
// as everywhere else in this encoder: a round that doesn't measurably
// help is simply discarded, never trusted on the strength of its own
// re-parse alone.
func refineParse(data []byte, order, nMainSyms int, initial costModel) (toks []token, mainLens, lenLens []byte, encoded []byte) {
	bestToks := findMatches(data, initial)
	bestMainLens, bestLenLens := buildTables(bestToks, nMainSyms)
	bestEncoded := encodeBlock(data, order, bestToks, bestMainLens, bestLenLens,
		canonicalCodewords(bestMainLens, maxMainCodewordLen), canonicalCodewords(bestLenLens, maxLenCodewordLen), nil, nil)

	model := costModel{mainLens: bestMainLens, lenLens: bestLenLens}
	for i := 0; i < maxRefineIters; i++ {
		nt := findMatches(data, model)
		nMainLens, nLenLens := buildTables(nt, nMainSyms)
		enc := encodeBlock(data, order, nt, nMainLens, nLenLens,
			canonicalCodewords(nMainLens, maxMainCodewordLen), canonicalCodewords(nLenLens, maxLenCodewordLen), nil, nil)

		model = costModel{mainLens: nMainLens, lenLens: nLenLens}
		if len(enc) >= len(bestEncoded) {
			break // this round's re-parse didn't measurably help; converged (or diverging) -- stop.
		}
		bestToks, bestMainLens, bestLenLens, bestEncoded = nt, nMainLens, nLenLens, enc
	}
	return bestToks, bestMainLens, bestLenLens, bestEncoded
}

// compressLookahead runs refineParse's iteratively-refined bounded-lookahead
// parse and returns the smallest of its VERBATIM, ALIGNED, 2-block-split,
// splitStats, and hash2 encodings -- the non-DP half of compress()'s work,
// split out so it can run concurrently with compressOptimal (see compress
// above).
func compressLookahead(data []byte, order, nMainSyms int, pass1Model costModel) []byte {
	toks, mainLens, lenLens, refinedVerbatim := refineParse(data, order, nMainSyms, pass1Model)
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
	verbatim := refinedVerbatim // refineParse already measured this exact encoding while converging
	var aligned, split, splitStats, hash2 []byte
	var wg sync.WaitGroup
	wg.Add(4)
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
	go func() {
		defer wg.Done()
		// Also try wimlib's own real, statistics-driven block-splitting
		// heuristic (splitstats.go, ported directly from wimlib's source),
		// which can produce zero, one, or several split points based on
		// actual content shifts rather than trySplitChunk's single
		// midpoint-only attempt. See gowim's own TODO.md for why this
		// (block layout) was tried as a separate lever from this
		// package's several already-tried-and-measured parse/cost-model
		// changes.
		splitStats = trySplitChunkStats(data, order, nMainSyms, toks)
	}()
	go func() {
		defer wg.Done()
		// Also try greedily substituting hash2 (length-2 fresh-offset)
		// matches into toks (hash2greedy.go), scoring each candidate
		// against a main Huffman table rebuilt after every prior
		// acceptance -- unlike the flat/stale pass1Model estimate the
		// main parse above used, this reflects each candidate's real
		// effect (including Kraft-inequality knock-on lengthening of
		// other codewords) before deciding the next one. See
		// hash2greedy.go's own doc for why this is expected to avoid
		// the earlier, static-table hash2 attempt's measured regression
		// (see gowim's own TODO.md), and why the result is still only
		// kept here if it measures smaller than every other candidate,
		// not trusted on the strength of its own scoring alone.
		hash2Toks := greedyApplyHash2(data, toks, nMainSyms)
		if len(hash2Toks) == len(toks) {
			return // no candidate was accepted; identical to verbatim
		}
		hash2MainLens, hash2LenLens := buildTables(hash2Toks, nMainSyms)
		hash2MainCodes := canonicalCodewords(hash2MainLens, maxMainCodewordLen)
		hash2LenCodes := canonicalCodewords(hash2LenLens, maxLenCodewordLen)
		hash2 = encodeBlock(data, order, hash2Toks, hash2MainLens, hash2LenLens, hash2MainCodes, hash2LenCodes, nil, nil)
	}()
	wg.Wait()

	best := verbatim
	if len(aligned) < len(best) {
		best = aligned
	}
	if split != nil && len(split) < len(best) {
		best = split
	}
	if splitStats != nil && len(splitStats) < len(best) {
		best = splitStats
	}
	if hash2 != nil && len(hash2) < len(best) {
		best = hash2
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
	mainFreqs, lenFreqs := tokenFreqs(toks, nMainSyms)
	return buildLengths(mainFreqs, maxMainCodewordLen), buildLengths(lenFreqs, maxLenCodewordLen)
}

// tokenFreqs computes the raw main/length symbol frequency counts toks
// would produce, per the LZX main/length code alphabets -- the shared
// counting logic behind buildTables, also used by greedyApplyHash2
// (hash2greedy.go), which needs to mutate these counts incrementally
// rather than only ever consuming a finished Huffman table built from them.
func tokenFreqs(toks []token, nMainSyms int) (mainFreqs, lenFreqs []uint32) {
	mainFreqs = make([]uint32, nMainSyms)
	lenFreqs = make([]uint32, lenCodeNumSymbols)
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
	return mainFreqs, lenFreqs
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
// compressed run of consecutive *actually unused* (codeword length 0)
// entries using precode symbol 17 (a run of 4-19) or 18 (a run of 20-51),
// or a short run of 4-5 consecutive entries that all resolve to the same
// *nonzero* codeword length using precode symbol 19 (sym2 carries one
// delta value, computed only from the run's first position, itself
// precode-encoded from the same alphabet) -- matching wimlib's real
// lzx_compute_precode_items exactly (src/lzx_compress.c; this package's
// decoder already implements the matching read side -- see decode.go's
// readCodewordLens, which likewise computes/broadcasts a single resolved
// length per run rather than recomputing per position).
//
// The critical, easy-to-get-backwards distinction (a real bug here, found
// and fixed 2026-08-18 -- see gowim's own TODO.md): runs are grouped by
// whether the ACTUAL NEW codeword length (lens[i]) is 0 or repeats, not
// by whether the DELTA against prevLens happens to be 0 or repeats. These
// two groupings coincide only when prevLens is uniformly zero (this
// package's first-block/all-zero baseline), which is why grouping by
// delta equality instead of length equality was never caught until a
// second, non-zero, non-uniform prevLens (i.e. the second block of a
// multi-block split) was actually exercised.
type codewordLenToken struct {
	presym int
	runLen int // meaningful when presym is 17, 18, or 19
	sym2   int // meaningful when presym is 19: the delta value for the run's first position (a plain 0-16 symbol)
}

func codewordLenTokens(lens, prevLens []byte) []codewordLenToken {
	var toks []codewordLenToken
	n := len(lens)

	delta := func(pos int) int {
		d := int(prevLens[pos]) - int(lens[pos])
		if d < 0 {
			d += 17
		}
		return d
	}

	runStart := 0
	for runStart < n {
		l := lens[runStart]
		runEnd := runStart + 1
		for runEnd < n && lens[runEnd] == l {
			runEnd++
		}

		if l == 0 {
			for runEnd-runStart >= 20 {
				rl := runEnd - runStart
				if rl > 51 {
					rl = 51
				}
				toks = append(toks, codewordLenToken{presym: 18, runLen: rl})
				runStart += rl
			}
			if runEnd-runStart >= 4 {
				toks = append(toks, codewordLenToken{presym: 17, runLen: runEnd - runStart})
				runStart = runEnd
			}
		} else {
			for runEnd-runStart >= 4 {
				rl := 5
				if runEnd-runStart < 5 {
					rl = 4
				}
				toks = append(toks, codewordLenToken{presym: 19, runLen: rl, sym2: delta(runStart)})
				runStart += rl
			}
		}

		for runStart < runEnd {
			toks = append(toks, codewordLenToken{presym: delta(runStart)})
			runStart++
		}
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
	toks := codewordLenTokens(lens, prevLens)

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
