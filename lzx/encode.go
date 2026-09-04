package lzx

import "sync"

// zeroMainLens/zeroLenLens are read-only all-zero "previous block" length
// baselines, shared across every encodeBlock/trySplitChunk call instead of
// each allocating its own. writeCodewordLens/codewordLenTokens only ever
// read prevLens (delta() does prevLens[pos]), never write it, and the
// content is always all-zero bytes regardless of nMainSyms/order -- so
// sharing one backing array (safely, since it's never mutated, including
// across the concurrent goroutines in trySplitChunk/compressLookahead) is
// output-identical to each call allocating its own zeroed slice.
// zeroMainLens is sized to the largest possible nMainSyms (256 numChars +
// 50 maxOffsetSlots * 8 numLenHeaders = 656; see numMainSyms).
var (
	zeroMainLens = make([]byte, numChars+maxOffsetSlots*numLenHeaders)
	zeroLenLens  = make([]byte, lenCodeNumSymbols)
)

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
func compress(input []byte, o encodeOptions) []byte {
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

	// Options.Uncompressed (see options.go's None preset) skips everything
	// below -- match-finding, parsing, block-splitting, Huffman tables --
	// going straight from the (already E8-filtered) data to a single raw
	// UNCOMPRESSED block. See uncompressed.go's writeUncompressedBlock.
	if o.uncompressed {
		w := newBitWriterCap(len(data) + 32)
		writeUncompressedBlock(w, data, order)
		return w.flush()
	}

	nMainSyms := numMainSyms(order)

	// prevOcc2 (buildHash2PrevOcc's most-recent-2-byte-occurrence table) is
	// a pure function of data alone -- see its own doc in matcher.go -- so
	// it's computed exactly once per chunk, here, and threaded through
	// every parse call below (pass 1, refineParseWith's initial parse, and
	// each refinement round, across both the lookahead and DP parsers)
	// instead of each one separately rebuilding an identical table.
	prevOcc2 := buildHash2PrevOcc(data)

	// Two-pass parse: pass 1 uses a flat, data-independent cost estimate
	// (costModel{}) to choose among candidate matches, since no real
	// Huffman codeword lengths exist yet. Its resulting token frequencies
	// build a first Huffman table, which is then used as pass 2's cost
	// model -- a refined, this-chunk's-real-code-informed re-parse. This
	// is a standard two-pass technique approximating the joint parse/code
	// optimization a full iterative optimal parser would do (see
	// matcher.go's costModel doc and gowim's own TODO.md for why this
	// package doesn't implement the latter).
	//
	// Pass 1 is parsed greedily unless Options.FullFirstPass says otherwise
	// (see options.go), and that applies ONLY here, on this throwaway pass,
	// via this local copy of the options: pass 1's tokens are never emitted,
	// so the sole thing its parse quality can affect is how good a starting
	// table the later passes get -- a much weaker requirement than the parse
	// that actually gets written, and one a greedy parse measurably meets
	// just as well. Pass 1 is also the only part of compress() that is not
	// overlapped with anything else (the two halves below fork only after
	// it), so it is worth disproportionately more here than its share of the
	// work suggests.
	o1 := o
	o1.greedyParse = o.greedyPass1
	toks1 := findMatchesWith(data, costModel{}, o1, prevOcc2)
	// toks1 is used only here (by buildTables/tokenFreqs), not shared with
	// buildAlignedTable or writeBlockInto for this exact slice, so there is
	// no redundant offsetSlot work to dedup -- slots is still threaded
	// through for signature consistency.
	slots1 := tokenOffsetSlots(toks1)
	mainLens1, lenLens1 := buildTables(toks1, nMainSyms, slots1)
	pass1Model := costModel{mainLens: mainLens1, lenLens: lenLens1}

	// The bounded-lookahead parse (and everything downstream of it:
	// ALIGNED/split trials) and the DP parse are independent given
	// pass1Model, so run them concurrently rather than serially -- the DP
	// parse in particular is the most expensive single step in compress()
	// (see gowim's own TODO.md's measured time cost), making this the
	// highest-value place in the encoder to overlap work.
	// Options.DisableDP (see options.go) skips the DP half entirely: it is
	// the single biggest speed lever this encoder has, since the DP parse
	// dominates compress()'s cost. With it set there is nothing to overlap,
	// so the lookahead parse simply runs on this goroutine.
	if !o.dp {
		return compressLookahead(data, order, nMainSyms, pass1Model, o, prevOcc2)
	}

	var lookaheadBest, optBest []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		lookaheadBest = compressLookahead(data, order, nMainSyms, pass1Model, o, prevOcc2)
	}()
	go func() {
		defer wg.Done()
		optBest = compressOptimal(data, order, nMainSyms, pass1Model, o, prevOcc2)
	}()
	wg.Wait()

	best := lookaheadBest
	if len(optBest) < len(best) {
		best = optBest
	}
	return best
}

// refinePatience bounds how many *consecutive* non-improving rounds
// refineParseWith tolerates before giving up, rather than a flat round
// count: measured directly on real chunks, most converge (or fail to ever
// improve at all) within the first round or two, but a real minority
// oscillate -- regressing for several consecutive rounds before recovering
// to a new overall best (one sampled real chunk's true best over 12 rounds
// only appeared on round 12, after 3 and then 5 consecutive non-improving
// rounds). A flat round cap either wastes rounds on the common
// fast-converging case or cuts the rare oscillating case off too early;
// patience against the *global* best (not just the immediately preceding
// round) costs ~1 extra round in the common case while still letting a
// slow-recovering chunk keep going. See refineParseWith's own doc for why
// stopping at the first non-improving round (this package's original
// policy) measurably left real gains on the table (see gowim's own
// TODO.md for the 926-byte loss found across just 12 sampled chunks).
//
// 2, not a larger value like 6, after directly measuring the tradeoff on
// the same 12-chunk sample: patience=2 captured 828 of the 964 bytes
// gained going from patience=1 to patience=6 (86%) at only ~1.6x
// patience=1's time cost, whereas patience=6 cost ~4.7x for the remaining
// 14% -- steeply diminishing returns past 2. On the full 398-chunk
// benchmark, patience=6 measured a real full-benchmark wall-time cost of
// 61s -> 234s (~3.8x) for a further 5,436-byte gain over the
// beam-widened baseline -- too steep a trade to keep as the default.
//
// This is now the *default* for Options.RefinePatience (options.go), not a
// hard-wired constant: a caller who wants the patience=6 trade (or the
// patience=1 speed) can ask for it per call.
const refinePatience = 2

// maxRefineItersHardCap is an absolute safety ceiling on refineParseWith's
// total rounds, independent of refinePatience: there is no proof this
// iteration is guaranteed to ever settle into a true fixed point (each
// round's table is built from a discrete re-parse, not a continuous
// process), so an unconditionally patience-only loop could in principle
// run indefinitely on a pathological input that keeps finding ever-more-
// delayed improvements. This bounds worst-case cost regardless -- with
// refinePatience now at 2, this ceiling is rarely relevant in practice
// (it would take a chunk finding a new best every third round, sustained
// for 32 rounds straight, to ever reach it) but is kept generous since it
// costs nothing unless actually hit. Now the default for
// Options.MaxRefineIters (options.go).
const maxRefineItersHardCap = 32

// refineParse runs findMatches through refineParseWith below -- see that
// function's doc for the actual iteration logic. Factored out from
// refineParseWith only so callers that always want findMatches don't need
// to name it explicitly.
func refineParse(data []byte, order, nMainSyms int, initial costModel, o encodeOptions, prevOcc2 []int32) (toks []token, slots []int, mainLens, lenLens []byte, encoded []byte, alignedLens []byte, alignedCodes []uint16) {
	return refineParseWith(data, order, nMainSyms, initial, func(d []byte, m costModel) []token {
		return findMatchesWith(d, m, o, prevOcc2)
	}, o)
}

// refineParseWith runs the given parse function (findMatches' bounded
// lookahead, or findMatchesOptimal's beam DP -- both share the
// `func([]byte, costModel) []token` shape) repeatedly, feeding each
// round's own resulting real Huffman table back in as the next round's
// cost model -- generalizing this package's existing "pass 1 (flat cost)
// -> pass 2 (real cost from pass 1's table)" two-pass technique into a
// fixed-point iteration: pass 2's real table informs a pass 3 re-parse,
// pass 3's table informs pass 4, and so on, each round asking the same
// parser to re-decide every literal/match choice against a progressively
// more self-consistent table, rather than stopping after one refinement.
//
// Originally this only wrapped findMatches (see gowim's own TODO.md for
// why: the "rebuild the real table, don't trust a stale one" lesson from
// an earlier hash2 ex-post-splice attempt generalizes to full matches via
// iterated re-parsing rather than a local candidate-accept step, since
// general match candidates overlap their neighbors arbitrarily). It was
// widened to take the parse function as a parameter after measuring, on the real
// 398-chunk ntoskrnl.exe benchmark, that findMatchesOptimal's DP actually
// wins (produces the smaller encoding) on 346 of 398 chunks -- i.e. most
// of this encoder's real-world output was coming from the *unrefined*
// single-pass DP the whole time, which had never received any of this
// package's cost-model refinements at all. See compressOptimal for where
// this is now applied to the DP path too.
//
// alignedLens, rebuilt from each round's own tokens and fed into the next
// round's cost model (see costModel.offsetExtraCost), lets the *parse
// itself* -- not just the final post-hoc VERBATIM-vs-ALIGNED choice
// encodeBlock's caller already makes -- account for an offset's low bits
// potentially being cheaper under ALIGNED encoding, mirroring a real,
// verified difference against wimlib's own near-optimal parser
// (CONSIDER_ALIGNED_COSTS in its src/lzx_compress.c).
//
// Each round's real encoded byte count is measured directly (encodeBlock,
// not the token-implied estimate). Unlike an earlier version of this
// function, iteration does NOT stop at the first round that fails to beat
// the current best: measured directly on real chunks, the per-round size
// is not monotonic -- it can regress for several consecutive rounds and
// then recover to a new true best later (see refinePatience's own doc for
// the measured example). Every round still chains from the *previous*
// round's own resulting table, whether or not that round was itself an
// improvement -- that chaining, not resetting to the best-so-far's table
// after a bad round, is what lets the sequence recover in the first
// place. The best result seen across all attempted rounds (which may
// not be the last one) is what's returned, keeping the same "try both,
// keep smaller" safety property as everywhere else in this encoder.
func refineParseWith(data []byte, order, nMainSyms int, initial costModel, parse func([]byte, costModel) []token, o encodeOptions) (toks []token, slots []int, mainLens, lenLens []byte, encoded []byte, alignedLens []byte, alignedCodes []uint16) {
	bestToks := parse(data, initial)
	// bestSlots is computed once here and reused below by buildTables
	// (tokenFreqs), encodeBlock (writeBlockInto), and buildAlignedTable --
	// all three run over this exact bestToks slice in this scope.
	bestSlots := tokenOffsetSlots(bestToks)
	bestMainLens, bestLenLens := buildTables(bestToks, nMainSyms, bestSlots)
	bestEncoded := encodeBlock(data, order, bestToks, bestSlots, bestMainLens, bestLenLens,
		canonicalCodewords(bestMainLens, maxMainCodewordLen), canonicalCodewords(bestLenLens, maxLenCodewordLen), nil, nil)

	// seen guards against fixed points and longer cycles: since each
	// round's parse is a pure function of the model handed to it, an
	// exact repeat of any earlier round's token sequence proves every
	// later round will just replay the same cycle forever (a fixed point
	// is simply a cycle of length 1). Stopping the moment a repeat is
	// detected avoids burning the full patience/hard-cap budget on every
	// affected chunk for no possible further benefit.
	//
	// When o.refinePatience == 1 (the Fast preset), this fingerprinting is
	// provably a no-op: the loop below already exits as soon as noImprove
	// reaches 1, i.e. on the very first non-improving round -- there is
	// never a second round available in which a repeat could even be
	// checked, let alone break early. Trace it through: round 1 runs, and
	// either it improves (noImprove stays 0, loop continues to round 2 with
	// no fingerprint check having been needed to stop anything) or it does
	// not (noImprove becomes 1, and the *next* loop condition check
	// `noImprove < o.refinePatience` -- 1 < 1 -- is false, so the loop exits
	// before a round 2 ever runs to be compared against anything). So with
	// patience 1, `seen`/`fp`/`tokensFingerprint` are computed and consulted
	// but their result (the `if seen[fp] { break }`) can never actually fire
	// before the patience-based exit would have stopped the loop anyway --
	// skip the (non-trivial: FNV-1a over every field of every token, into a
	// map) computation entirely in that case. Patience > 1 (Balanced/
	// Default/Max) keeps the existing behavior exactly, since there the
	// fingerprint break is the only thing that can end an oscillating-but-
	// not-yet-patience-exhausted sequence early.
	var seen map[uint64]bool
	if o.refinePatience > 1 {
		seen = map[uint64]bool{tokensFingerprint(bestToks): true}
	}

	// bestAlignedLens/bestAlignedCodes track the aligned-offset table that
	// corresponds to whichever round's tokens are currently bestToks --
	// each round already builds a full buildAlignedTable(nt, ntSlots)
	// below to feed its own resulting cost model, so retaining that exact
	// result here whenever a round becomes the new best lets callers that
	// need the winning round's aligned table (e.g. compressLookahead's
	// own ALIGNED trial) reuse it instead of paying for a second, fully
	// redundant buildAlignedTable call over the same tokens.
	// bestSlots (declared as a named return value) tracks bestToks'
	// tokenOffsetSlots the same way bestAlignedLens/bestAlignedCodes track
	// its aligned table below: each round already computes ntSlots (used by
	// buildTables/buildAlignedTable/encodeBlock above), so retaining it
	// whenever a round becomes the new best lets callers that need the
	// winning round's slots (e.g. compressLookahead) reuse it instead of
	// paying for a second, fully redundant tokenOffsetSlots(toks) call over
	// the same tokens.
	bestAlignedLens, bestAlignedCodes := buildAlignedTable(bestToks, bestSlots)
	model := costModel{mainLens: bestMainLens, lenLens: bestLenLens, alignedLens: bestAlignedLens}
	noImprove := 0
	for i := 0; i < o.maxRefineIters && noImprove < o.refinePatience; i++ {
		nt := parse(data, model)
		var fp uint64
		if o.refinePatience > 1 {
			fp = tokensFingerprint(nt)
		}
		// ntSlots is likewise computed once and reused by buildTables,
		// encodeBlock, and buildAlignedTable for this round's nt slice.
		ntSlots := tokenOffsetSlots(nt)
		nMainLens, nLenLens := buildTables(nt, nMainSyms, ntSlots)
		// Only this round's resulting byte *length* is needed to decide
		// whether it beats bestEncoded -- most rounds don't (this package's
		// own measurements found per-round size non-monotonic, but still
		// usually non-improving after the first round or two), so
		// countBlockBits/countBlockBitsToBytes gets that length without
		// materializing a full bitstream via encodeBlock. Only once a round
		// is confirmed to actually beat the current best (below) is its
		// real encodeBlock output produced -- that round's bytes are the
		// ones actually needed (as bestEncoded, ultimately returned to
		// callers such as compressLookahead as a real output candidate).
		encBytes := countBlockBitsToBytes(countBlockBits(len(data), order, nt, ntSlots, nMainLens, nLenLens, nil))

		nAlignedLens, nAlignedCodes := buildAlignedTable(nt, ntSlots)
		model = costModel{mainLens: nMainLens, lenLens: nLenLens, alignedLens: nAlignedLens}
		if encBytes < len(bestEncoded) {
			enc := encodeBlock(data, order, nt, ntSlots, nMainLens, nLenLens,
				canonicalCodewords(nMainLens, maxMainCodewordLen), canonicalCodewords(nLenLens, maxLenCodewordLen), nil, nil)
			bestToks, bestSlots, bestMainLens, bestLenLens, bestEncoded = nt, ntSlots, nMainLens, nLenLens, enc
			bestAlignedLens, bestAlignedCodes = nAlignedLens, nAlignedCodes
			noImprove = 0
		} else {
			noImprove++
		}
		if o.refinePatience > 1 {
			if seen[fp] {
				break // fixed point or longer cycle detected; further rounds can only repeat it
			}
			seen[fp] = true
		}
	}
	return bestToks, bestSlots, bestMainLens, bestLenLens, bestEncoded, bestAlignedLens, bestAlignedCodes
}

// tokensFingerprint returns a compact, order-sensitive FNV-1a hash of toks'
// full content (every field of every token), used by refineParseWith to
// detect an exact repeat of a previous round's parse output cheaply,
// without retaining and comparing full token slices.
func tokensFingerprint(toks []token) uint64 {
	const offset64 = 14695981039346656037
	const prime64 = 1099511628211
	h := uint64(offset64)
	mix := func(v uint64) {
		for k := 0; k < 8; k++ {
			h ^= v & 0xff
			h *= prime64
			v >>= 8
		}
	}
	for _, t := range toks {
		var b uint64
		if t.isMatch {
			b = 1
		}
		mix(b)
		mix(uint64(t.literal))
		mix(uint64(int64(t.offset)))
		mix(uint64(int64(t.length)))
		mix(uint64(int64(t.repeat)))
	}
	return h
}

// compressLookahead runs refineParse's iteratively-refined bounded-lookahead
// parse (which now natively considers length-2 hash2 candidates alongside
// everything else -- see matcher.go's hash2Candidate) and returns the
// smallest of its VERBATIM, ALIGNED, 2-block-split, and splitStats
// encodings -- the non-DP half of compress()'s work, split out so it can
// run concurrently with compressOptimal (see compress above).
func compressLookahead(data []byte, order, nMainSyms int, pass1Model costModel, o encodeOptions, prevOcc2 []int32) []byte {
	toks, toksSlots, mainLens, lenLens, refinedVerbatim, alignedLens, alignedCodes := refineParse(data, order, nMainSyms, pass1Model, o, prevOcc2)
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
	var aligned, split, splitStats []byte
	var wg sync.WaitGroup
	// Options.DisableBlockSplit (options.go) drops both split trials --
	// each is a further full re-encode (trySplitChunkStats potentially
	// several) of the token stream -- keeping only the VERBATIM and
	// ALIGNED candidates.
	nTrials := 3
	if !o.blockSplit {
		nTrials = 1
	}
	wg.Add(nTrials)
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
		// heuristics above. alignedLens/alignedCodes here are the exact
		// buildAlignedTable result already computed by refineParseWith's
		// own winning round (see refineParse above) -- reused as-is
		// rather than recomputed, since toks is that same winning
		// round's bestToks. toksSlots is likewise that same winning
		// round's tokenOffsetSlots result, now threaded through
		// refineParse's/refineParseWith's return values (see bestSlots
		// there) instead of being recomputed here from scratch.
		//
		// Only aligned's resulting byte length matters unless it actually
		// beats verbatim's (already-real) byte length below -- verbatim is
		// always a real candidate regardless, so aligned's own bytes are
		// only ever needed when it wins that comparison. Check the length
		// via countBlockBits first and only materialize aligned's real
		// bytes (via encodeBlock) when it does, leaving aligned nil
		// otherwise -- the final selection below treats a nil aligned as
		// "did not win" rather than as a zero-length candidate.
		alignedBytes := countBlockBitsToBytes(countBlockBits(len(data), order, toks, toksSlots, mainLens, lenLens, alignedLens))
		if alignedBytes < len(verbatim) {
			aligned = encodeBlock(data, order, toks, toksSlots, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes)
		}
	}()
	if o.blockSplit {
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
			split = trySplitChunk(data, order, toks, toksSlots, nMainSyms)
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
			splitStats = trySplitChunkStats(data, order, nMainSyms, toks, toksSlots)
		}()
	}
	wg.Wait()

	best := verbatim
	if aligned != nil && len(aligned) < len(best) {
		best = aligned
	}
	if split != nil && len(split) < len(best) {
		best = split
	}
	if splitStats != nil && len(splitStats) < len(best) {
		best = splitStats
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
func compressOptimal(data []byte, order, nMainSyms int, pass1Model costModel, o encodeOptions, prevOcc2 []int32) []byte {
	_, _, _, _, best, _, _ := refineParseWith(data, order, nMainSyms, pass1Model, func(d []byte, m costModel) []token {
		return findMatchesOptimalWith(d, m, o, prevOcc2)
	}, o)
	return best
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
func trySplitChunk(data []byte, order int, toks []token, toksSlots []int, nMainSyms int) []byte {
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
	zeros := zeroMainLens[:nMainSyms]
	zerosLen := zeroLenLens

	// Decide VERBATIM vs ALIGNED for one half, comparing byte lengths (same
	// comparison the whole-chunk case above already uses) -- neither
	// candidate's actual bytes are needed here, only which is smaller
	// (encoding proper happens once, below, for the half's real winning
	// choice via writeBlockInto), so countBlockBits/countBlockBitsToBytes
	// gets that comparison without ever materializing a bitstream for
	// either candidate.
	// blkSlots must be tokenOffsetSlots(blkToks) for this exact blkToks
	// slice -- shared here across both countBlockBits calls and
	// buildAlignedTable. mainCodes/lenCodes are unused here (only codeword
	// *lengths*, not the codes' numeric values, affect a bit count) but
	// kept as parameters since callers already have them computed for
	// later use.
	chooseAligned := func(blkData []byte, blkToks []token, blkSlots []int, mainLens, lenLens []byte, mainCodes, lenCodes []uint16) (bool, []byte, []uint16) {
		vBytes := countBlockBitsToBytes(countBlockBits(len(blkData), order, blkToks, blkSlots, mainLens, lenLens, nil))
		aLens, aCodes := buildAlignedTable(blkToks, blkSlots)
		aBytes := countBlockBitsToBytes(countBlockBits(len(blkData), order, blkToks, blkSlots, mainLens, lenLens, aLens))
		return aBytes < vBytes, aLens, aCodes
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
	// firstSlots/secondSlots are just the corresponding sub-slices of
	// toksSlots (the caller's already-computed tokenOffsetSlots(toks)):
	// tokenOffsetSlots is elementwise (each output entry depends only on
	// the matching input token), so tokenOffsetSlots(toks[a:b]) always
	// equals tokenOffsetSlots(toks)[a:b] -- no need to recompute from
	// scratch for either half. Reused by buildTables (tokenFreqs),
	// chooseAligned's buildAlignedTable and encodeBlock calls, and the
	// final writeBlockInto call below -- all of which run over the exact
	// same first/second toks slice respectively.
	firstSlots := toksSlots[:splitIdx]
	secondSlots := toksSlots[splitIdx:]

	go func() {
		defer wg.Done()
		mainLens1, lenLens1 = buildTables(first, nMainSyms, firstSlots)
		mainCodes1 = canonicalCodewords(mainLens1, maxMainCodewordLen)
		lenCodes1 = canonicalCodewords(lenLens1, maxLenCodewordLen)
		useAligned1, alignedLens1, alignedCodes1 = chooseAligned(firstData, first, firstSlots, mainLens1, lenLens1, mainCodes1, lenCodes1)
	}()
	go func() {
		defer wg.Done()
		mainLens2, lenLens2 = buildTables(second, nMainSyms, secondSlots)
		mainCodes2 = canonicalCodewords(mainLens2, maxMainCodewordLen)
		lenCodes2 = canonicalCodewords(lenLens2, maxLenCodewordLen)
		useAligned2, alignedLens2, alignedCodes2 = chooseAligned(secondData, second, secondSlots, mainLens2, lenLens2, mainCodes2, lenCodes2)
	}()
	wg.Wait()

	if !useAligned1 {
		alignedLens1, alignedCodes1 = nil, nil
	}
	if !useAligned2 {
		alignedLens2, alignedCodes2 = nil, nil
	}

	w := newBitWriterCap(len(data) + 64)
	writeBlockInto(w, firstData, order, first, firstSlots, mainLens1, zeros, lenLens1, zerosLen, mainCodes1, lenCodes1, alignedLens1, alignedCodes1)
	writeBlockInto(w, secondData, order, second, secondSlots, mainLens2, mainLens1, lenLens2, lenLens1, mainCodes2, lenCodes2, alignedLens2, alignedCodes2)
	return w.flush()
}

// encodeBlock writes a single, standalone LZX block (VERBATIM if
// alignedLens/alignedCodes are nil, ALIGNED otherwise) for toks against the
// given main/length (and, for ALIGNED, aligned-offset) Huffman tables,
// delta-coded against an all-zero "previous block" baseline. This is the
// single-block case (compress() always uses it for whole-chunk encoding);
// see writeBlockInto for the shared multi-block-capable core, used when
// splitting a chunk into more than one block (see trySplitChunk).
// slots must be tokenOffsetSlots(toks) for this exact toks slice.
func encodeBlock(data []byte, order int, toks []token, slots []int, mainLens, lenLens []byte, mainCodes, lenCodes []uint16, alignedLens []byte, alignedCodes []uint16) []byte {
	nMainSyms := numMainSyms(order)
	w := newBitWriterCap(len(data) + 64)
	writeBlockInto(w, data, order, toks, slots, mainLens, zeroMainLens[:nMainSyms], lenLens, zeroLenLens, mainCodes, lenCodes, alignedLens, alignedCodes)
	return w.flush()
}

// countBlockBits computes the exact number of bits writeBlockInto would
// emit for a single, standalone LZX block against the all-zero "previous
// block" baseline (the same shape encodeBlock produces), without ever
// constructing a bitWriter or materializing any output bytes. It mirrors
// writeBlockInto's structure field-for-field: block header bits, the
// aligned-offset length table (if present), the three codeword-length-table
// passes (via countCodewordLensBits below), then each token's main/length/
// extra-offset bits -- so its result, once padded via
// countBlockBitsToBytes, is exactly len(encodeBlock(data, order, toks,
// slots, mainLens, lenLens, <any mainCodes/lenCodes>, alignedLens,
// <any alignedCodes>)) would have been -- see
// TestCountBlockBitsMatchesEncodeBlock. mainCodes/lenCodes/alignedCodes are
// intentionally not parameters here: only codeword *lengths* affect a bit
// count, never the numeric codeword values assigned to them.
//
// This exists for callers that only need encodeBlock's resulting byte
// *length* to compare one candidate encoding against another (VERBATIM vs
// ALIGNED trials, per-segment/per-half split trials, refinement rounds) --
// not the actual bytes, which are only worth producing for whichever
// candidate actually wins such a comparison. slots must be
// tokenOffsetSlots(toks) for this exact toks slice, exactly as
// encodeBlock/writeBlockInto require.
func countBlockBits(dataLen, order int, toks []token, slots []int, mainLens, lenLens []byte, alignedLens []byte) int {
	nMainSyms := numMainSyms(order)
	useAligned := alignedLens != nil

	bits := 3 // block type (3 bits)
	if dataLen == defaultBlockSize {
		bits++ // the single "default size" flag bit
	} else {
		bits++ // the flag bit
		if order >= 16 {
			bits += 24
		} else {
			bits += 16
		}
	}
	if useAligned {
		bits += len(alignedLens) * alignedCodeElementSize
	}

	// Codeword length tables, delta-coded against the all-zero baseline --
	// matching writeBlockInto's exact three calls (against
	// zeroMainLens[:nMainSyms]/zeroLenLens, precisely as encodeBlock passes
	// them to writeBlockInto).
	bits += countCodewordLensBits(mainLens[:numChars], zeroMainLens[:numChars])
	bits += countCodewordLensBits(mainLens[numChars:], zeroMainLens[numChars:nMainSyms])
	bits += countCodewordLensBits(lenLens, zeroLenLens[:lenCodeNumSymbols])

	// Literals and matches -- mirrors writeBlockInto's inner loop exactly,
	// substituting each w.writeBits(value, n) with += n.
	for i, t := range toks {
		if !t.isMatch {
			bits += int(mainLens[t.literal])
			continue
		}
		slot := slots[i]
		lengthField := t.length - minMatchLen
		header := lengthField
		if header > numPrimaryLens {
			header = numPrimaryLens
		}
		mainSym := numChars + slot*numLenHeaders + header
		bits += int(mainLens[mainSym])
		if header == numPrimaryLens {
			lsym := lengthField - numPrimaryLens
			bits += int(lenLens[lsym])
		}
		if t.repeat < 0 {
			extraBits := int(lzxExtraOffsetBits[slot])
			if extraBits > 0 {
				extra := uint32(t.offset) - uint32(lzxOffsetSlotBase[slot])
				if useAligned && slot >= minAlignedOffsetSlot {
					rawBits := extraBits - numAlignedOffsetBits
					if rawBits > 0 {
						bits += rawBits
					}
					asym := extra & (alignedCodeNumSymbols - 1)
					bits += int(alignedLens[asym])
				} else {
					bits += extraBits
				}
			}
		}
	}
	return bits
}

// countBlockBitsToBytes converts a countBlockBits bit count into the padded
// byte length bitWriter.flush() actually produces: bits are packed into
// 16-bit coding units as they're written, and flush() pads any remaining
// partial unit up to a full 16-bit unit with zero low bits (see
// bitwriter.go's flush) -- i.e. 2 bytes per 16-bit unit, rounding the bit
// count up to the next multiple of 16 first. bits == 0 correctly yields 0
// bytes, matching flush() being a no-op when w.nbits == 0.
func countBlockBitsToBytes(bits int) int {
	return 2 * ((bits + 15) / 16)
}

// countCodewordLensBits mirrors writeCodewordLens's exact bit accounting
// (the raw 4-bit-per-symbol precode codeword lengths, then one
// precode-encoded symbol per codewordLenTokens entry -- including the
// nested sym2 symbol for presym 19) without writing anything, returning
// only the total bit count. buildLengths on the precode symbol frequencies
// is unavoidable work either way, exactly as writeCodewordLens itself does
// it: precodeLens (its result) is what determines every bit width counted
// below.
func countCodewordLensBits(lens, prevLens []byte) int {
	toks := codewordLenTokens(lens, prevLens)

	freqs := make([]uint32, precodeNumSymbols)
	for _, t := range toks {
		freqs[t.presym]++
		if t.presym == 19 {
			freqs[t.sym2]++
		}
	}
	precodeLens := buildLengths(freqs, maxPrecodeCodewordLen)

	bits := len(precodeLens) * precodeElementSize
	for _, t := range toks {
		bits += int(precodeLens[t.presym])
		switch t.presym {
		case 17:
			bits += 4
		case 18:
			bits += 5
		case 19:
			bits++
			bits += int(precodeLens[t.sym2])
		}
	}
	return bits
}

// writeBlockInto writes a single LZX block (VERBATIM if alignedLens/
// alignedCodes are nil, ALIGNED otherwise) into an existing, possibly
// already-in-progress bitWriter w, with codeword lengths delta-coded
// against prevMainLens/prevLenLens -- the *actual* previous block's
// lengths when chaining multiple blocks into one continuous bitstream (LZX
// blocks are not byte-aligned relative to each other; only an UNCOMPRESSED
// block realigns -- see decode.go), or an all-zero baseline for the first
// (or only) block in a chunk. slots must be tokenOffsetSlots(toks) for this
// exact toks slice.
func writeBlockInto(w *bitWriter, data []byte, order int, toks []token, slots []int, mainLens, prevMainLens, lenLens, prevLenLens []byte, mainCodes, lenCodes []uint16, alignedLens []byte, alignedCodes []uint16) {
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
	for i, t := range toks {
		if !t.isMatch {
			w.writeBits(uint32(mainCodes[t.literal]), uint(mainLens[t.literal]))
			continue
		}
		slot := slots[i]
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
// fresh matches contribute. slots must be tokenOffsetSlots(toks) for this
// exact toks slice.
func buildAlignedTable(toks []token, slots []int) (lens []byte, codes []uint16) {
	freqs := make([]uint32, alignedCodeNumSymbols)
	for i, t := range toks {
		if !t.isMatch || t.repeat >= 0 {
			continue
		}
		slot := slots[i]
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
// symbol frequencies, per the LZX main/length code alphabets. slots must be
// tokenOffsetSlots(toks) for this exact toks slice.
func buildTables(toks []token, nMainSyms int, slots []int) (mainLens, lenLens []byte) {
	mainFreqs, lenFreqs := tokenFreqs(toks, nMainSyms, slots)
	return buildLengths(mainFreqs, maxMainCodewordLen), buildLengths(lenFreqs, maxLenCodewordLen)
}

// tokenFreqs computes the raw main/length symbol frequency counts toks
// would produce, per the LZX main/length code alphabets -- the shared
// counting logic behind buildTables. slots must be tokenOffsetSlots(toks)
// for this exact toks slice.
func tokenFreqs(toks []token, nMainSyms int, slots []int) (mainFreqs, lenFreqs []uint32) {
	mainFreqs = make([]uint32, nMainSyms)
	lenFreqs = make([]uint32, lenCodeNumSymbols)
	for i, t := range toks {
		if t.isMatch {
			slot := slots[i]
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

// tokenOffsetSlots precomputes, once per distinct toks slice, the exact
// per-token slot value that tokenFreqs/buildAlignedTable/writeBlockInto each
// independently recompute via offsetSlot: for a match token, t.repeat if
// it's a repeat-offset match (>= 0), else offsetSlot(t.offset); for a
// non-match (literal) token, -1 (a sentinel the three consumers never read,
// since they all gate slot use behind t.isMatch or an equivalent check).
//
// A performance audit found offsetSlot -- a bounded but non-trivial linear
// scan over up to maxOffsetSlots (50) table entries -- being called
// redundantly on the same match token by up to 3 separate per-toks-element
// loops (tokenFreqs, buildAlignedTable, writeBlockInto's inner loop) at call
// sites where all three run over the exact same toks slice (see
// refineParseWith, trySplitChunk, trySplitChunkStats). This computes that
// value exactly once per token per distinct toks slice actually needed, so
// callers can thread the result into whichever of those functions consume
// it for that slice instead of each recomputing it. It does not change
// which value is computed, so it cannot change compressed output.
func tokenOffsetSlots(toks []token) []int {
	slots := make([]int, len(toks))
	for i, t := range toks {
		if !t.isMatch {
			slots[i] = -1
			continue
		}
		slot := t.repeat
		if slot < 0 {
			slot = offsetSlot(uint32(t.offset))
		}
		slots[i] = slot
	}
	return slots
}

// offsetSlot returns the offset slot (>= 3) whose range
// [lzxOffsetSlotBase[slot], lzxOffsetSlotBase[slot+1]) contains a *fresh*
// (non-repeat-offset) match offset. Repeat-offset matches (slots 0-2) are
// resolved directly by the matcher/encoder from the recent-offsets queue and
// never go through this function -- see token.repeat and matcher.go.
//
// Despite the "called once per match" claim this comment used to make, a
// performance audit found offsetSlot actually called from multiple sites per
// token (bestFreshCandidate/hash2Candidate in matcher.go, plus
// tokenOffsetSlots below), making it hot enough to matter. For offsets within
// offsetSlotTable's range (the overwhelming majority of real WIM chunk
// offsets: the default chunk/block size is 32768 and even the largest
// supported window is 2097152), this is now an O(1) table lookup instead of
// the linear scan; offsetSlotScan (the original scan logic, unchanged) is
// kept as an exact fallback for offsets beyond the table -- see
// TestOffsetSlotTableMatchesScan for the exhaustive equivalence check.
func offsetSlot(offset uint32) int {
	if offset < offsetSlotTableSize {
		return int(offsetSlotTable[offset])
	}
	return offsetSlotScan(offset)
}

// offsetSlotTableSize bounds offsetSlotTable's direct-lookup range. 65536
// covers every offset within a default 32768-byte WIM chunk/block (and then
// some) at a modest fixed 64KB memory cost; offsets from larger windows
// (up to maxWindowSize, 2097152) beyond this range fall back to
// offsetSlotScan.
const offsetSlotTableSize = 65536

// offsetSlotTable is offsetSlotScan precomputed for every offset in
// [0, offsetSlotTableSize), built once at package init.
var offsetSlotTable = buildOffsetSlotTable()

func buildOffsetSlotTable() [offsetSlotTableSize]uint8 {
	var t [offsetSlotTableSize]uint8
	for off := 0; off < offsetSlotTableSize; off++ {
		t[off] = uint8(offsetSlotScan(uint32(off)))
	}
	return t
}

// offsetSlotScan is offsetSlot's original linear-scan implementation, kept
// as the exact fallback for offsets outside offsetSlotTable's range (and as
// the single source of truth the table is built from, so the two can never
// disagree by construction).
func offsetSlotScan(offset uint32) int {
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
	// At most one token per input position (fewer once runs compress),
	// so this capacity hint is an exact upper bound.
	toks := make([]codewordLenToken, 0, len(lens))
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
