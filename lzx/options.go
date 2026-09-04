package lzx

// Options selects this encoder's speed/compression-ratio tradeoff.
//
// The zero value means "this package's defaults", i.e. exactly what
// Compress does, so CompressWith(data, Options{}) is identical (byte for
// byte, not merely equivalent) to Compress(data). Every numeric field
// follows the same convention: 0 means "use the default", and every
// boolean field is named so that false is the default behavior. That is
// what lets a caller override one knob without having to restate the rest,
// and what lets this struct grow new knobs without changing what existing
// callers get.
//
// Most callers should not set these fields individually: the named presets
// (Fastest, Fast, Balanced, DefaultOptions, Max -- an ordered ladder, each
// a specific measured combination of the fields below) are the intended
// interface, and the individual fields are the escape hatch for tuning
// beyond them. See each preset's own doc for its measured position.
//
// Nothing here changes the format: every setting produces a valid LZX
// chunk that any conforming decoder (this package's Decompress, or
// wimlib's) decodes back to the original bytes. The only thing these knobs
// trade is encoder time against output size.
//
// # The preset ladder
//
// Fastest, Fast, Balanced, DefaultOptions and Max are an ordered ladder of
// measured (speed, size) points, from fastest/largest to slowest/smallest.
// Each is a specific combination of the fields above, chosen by measuring the
// individual knobs against each other rather than by guessing which ones
// would matter -- and, as of the 2026-08-18 remeasurement below, by
// measuring them on the workload this package actually exists to serve.
//
// All numbers below were measured on 2026-08-18 on an otherwise idle
// 24-core x86-64 Linux machine (Go 1.26.5), on real Windows install-image
// data rather than on synthetic or Unix-side samples:
//
//   - "WIM corpus": 29.4 MiB, 1170 chunks, 315 files pulled from a mounted
//     Windows 11 build 26100 image (ProgramData, Program Files, Program
//     Files (x86) and Windows trees), split into 32 KiB chunks exactly the
//     way this package's non-solid resources are chunked -- per file, last
//     chunk short (297 of the 1170 are short). By bytes it is 89.9% PE
//     (150 .dll/.exe/.mui/.sys), 6.9% registry hive, 1.9% manifest/inf/mum
//     text and 1.2% already-compressed (.png/.ttf/.jpg). Files longer than
//     32 chunks contribute an evenly spaced 32-chunk sample, so that one
//     72 MB SOFTWARE hive adds variety rather than volume.
//   - "hetero corpus": 17.5 MiB, 560 chunks, each one a deliberate splice
//     of halves (or quarters) of real chunks from different content
//     categories. This is the case block splitting exists for, and no
//     corpus of whole files exercises it, because a non-solid WIM chunk is
//     only ever heterogeneous where a single file is (a PE straddling
//     .text/.rsrc/.reloc, say). It is an upper bound, not a typical case.
//
// Serial figures are one goroutine over half the WIM corpus (585 chunks,
// 15.0 MiB), best of 3. Parallel figures are all 1170 chunks across all 24
// cores the way the `wim` package drives this encoder, best of 7
// interleaved runs. The parallel column is NOT simply 24x the serial one:
// at the default settings the encoder allocates fast enough (473.8 GB for
// those 29.4 MiB) that GC, not compute, is the binding constraint, which
// is why the alloc column is reported alongside.
//
//	preset      serial   parallel    output      alloc     vs Default
//	Fastest        --*        --*         --*        --*   +2.5% size*
//	Fast         8.17s   20.5 MB/s   10902398    11.6 GB   +1.75% size
//	Balanced     2.07s*   3.2 MB/s   10785178    87.9 GB   +0.66% size
//	Default     11.64s*   0.6 MB/s   10714428   473.8 GB   --
//	Max         44.11s*   0.2 MB/s   10706412  1049.6 GB   -0.07% size
//
// (*) Balanced, Default and Max are far too slow to time over 585 chunks;
// their serial column is a 30-chunk / 818 KiB subset of the same corpus.
// Output and alloc columns are always the full 1170-chunk corpus.
//
// Fastest is the one rung not measured on those corpora: it was added on
// 2026-09-03, when this machine no longer had the mounted Windows image
// those runs came from. Its own doc carries the full measurement it WAS
// made on (matchfinder_test.go's 572 KiB corpus, which this package carries
// and anyone can re-run): ~1.33x Fast's serial time for 0.83% more output.
// The "+2.5% vs Default" above is that 0.83% composed with Fast's own
// +1.75%, i.e. an estimate, not a measurement, and it is marked as such
// rather than quietly presented alongside numbers that are.
//
// The whole table also predates the greedy first pass that every rung got
// on 2026-09-03 (see Options.FullFirstPass): every time in it is now
// pessimistic, by 1.21x at the default settings and 1.06x on Fast, at an
// output size that measured unchanged. The size and alloc columns still
// stand.
//
// The ladder is deliberately not evenly spaced: the honest measured shape
// of this encoder is that nearly all of the DP parser's compression win is
// available at a small fraction of its cost (Balanced is 5x faster than
// Default for 0.66% more output), and the last half-percent is what costs
// the remaining order of magnitude. Every step from Fast down is at least
// 5x wide; see Fast's doc for a rung that was measured, found to be ~5%
// wide, and removed for it. Fastest, the one rung narrower than 5x, is
// there because ~1.33x for 0.83% is still a point a caller can want, and
// because it is the only rung that changes the match finder's structure
// rather than its budget -- see its own doc.
type Options struct {
	// DisableDP skips the bounded multi-state beam DP parser
	// (optimal.go's findMatchesOptimal) entirely, leaving only the
	// bounded-lookahead parser (matcher.go's findMatches). This is by far
	// the largest single lever in this struct: compress() runs both
	// parsers concurrently and keeps whichever encodes smaller, and
	// profiling attributed ~85% of the encoder's cumulative CPU to the DP
	// half. See Fast's doc for the measured cost and ratio loss.
	DisableDP bool

	// BeamWidth bounds how many distinct repeat-offset-queue-state
	// hypotheses the DP parser keeps per position (see optimal.go). It
	// multiplies both the states kept per position and the edges relaxed
	// per position. 0 means defaultBeamWidth (10). Ignored when
	// DisableDP is set.
	BeamWidth int

	// MaxFreshCandidates bounds how many distinct fresh-offset match
	// candidates the DP parser considers per position (see optimal.go);
	// each one becomes an edge per beam state, so this multiplies with
	// BeamWidth. 0 means defaultMaxFreshCandidates (24). Ignored when
	// DisableDP is set.
	MaxFreshCandidates int

	// MaxChainLen bounds the match finder's per-position comparison budget
	// -- the classic zlib-style "search depth" (see matchfinder.go, where
	// it bounds the descent of whichever structure HashChainMatcher
	// selected). Unlike the other match-finder knobs here it is shared by
	// BOTH parsers, so it is the only one that also speeds up a DisableDP
	// run. Note that a given value does NOT mean the same amount of work
	// for both structures: a chain node is far cheaper to visit than a tree
	// node but tells you far less, so the two want different budgets -- see
	// Fastest's doc for the sweep. 0 means defaultMaxChainLen (96).
	MaxChainLen int

	// DPRepeatLengthSamples bounds how many distinct lengths the DP parser
	// tries per repeat-offset candidate (see optimal.go's
	// repeatLengthSamples): 2 tries the full match length plus a midpoint
	// shorter one (letting the parse deliberately under-shoot a long
	// repeat run when a different choice afterward is cheaper), 1 tries
	// only the full length, halving the repeat-edge count. 0 means
	// defaultRepeatLengthSamples (2); values above 2 are clamped to 2,
	// since only those two samples exist. Ignored when DisableDP is set.
	DPRepeatLengthSamples int

	// DisableDPHash2 stops the DP parser from offering length-2 (hash2)
	// fresh-match edges (see matcher.go's hash2Candidate). The
	// bounded-lookahead parser keeps its own hash2 candidates either way;
	// this only affects the DP. False (the default) offers them.
	DisableDPHash2 bool

	// RefinePatience bounds how many *consecutive* non-improving rounds
	// the parse-refinement loop tolerates before giving up (see
	// encode.go's refineParseWith and refinePatience). 1 stops at the
	// first non-improving round. 0 means defaultRefinePatience (2).
	RefinePatience int

	// MaxRefineIters is the absolute ceiling on refinement rounds,
	// independent of RefinePatience (see encode.go's
	// maxRefineItersHardCap). 0 means defaultMaxRefineIters (32).
	MaxRefineIters int

	// HashChainMatcher replaces the bounded-lookahead parser's
	// binary-search-tree match finder with a plain hash chain: one
	// recency-ordered linked list per hash bucket, walked up to MaxChainLen
	// deep (see matchfinder.go's newChainMatcher for the structure and for
	// the two filters it can use that a tree cannot). It affects ONLY that
	// parser, never the DP parser (optimal.go), which needs a whole ranked
	// set of fresh candidates per position rather than a single best one --
	// so on a run with the DP enabled this knob changes only half the work
	// and, since compress() keeps whichever half encodes smaller, has very
	// little effect on output at all. It is meant for DisableDP runs, and
	// Fastest sets it for exactly that reason; see Fastest's doc for the
	// measured numbers.
	//
	// A chain is strictly a weaker search than a tree for the same
	// comparison budget: it walks candidates in recency order instead of
	// discarding whole lexicographic subtrees, so it can miss a longer
	// match the tree would have found. False (the default) keeps the tree.
	HashChainMatcher bool

	// FullFirstPass makes compress()'s pass 1 -- the throwaway parse whose
	// only product is a first Huffman table for the real passes to cost
	// against (see compress() in encode.go) -- use the same full bounded
	// 4-way lookahead parser as every other pass, instead of the greedy
	// parse it uses by default. Each position in that pass then costs four
	// match searches rather than one, since the lookahead's three
	// continuation terms are each a speculative search at a position the
	// parse may not even reach.
	//
	// False (the default) is the greedy pass, and unusually for this struct
	// the default here is the CHEAP option rather than the thorough one.
	// That is because a thorough pass 1 measured as buying nothing: pass 1's
	// tokens are never emitted, so the only thing its parse quality can
	// affect is how good a starting table the later passes get, and across
	// both of this package's corpora and every preset the difference in
	// final output was between -0.068% and +0.019% -- noise in both
	// directions, and on Max, the rung that exists to spend anything for
	// size, the greedy pass came out 0.019% SMALLER on one corpus and
	// 0.013% larger on the other (see TestGreedyFirstPassIsSizeNeutral,
	// which pins this on every rung).
	//
	// What a thorough pass 1 costs, meanwhile, is large and one-sided, most
	// of all at the default settings, where pass 1 happens to be the only
	// part of compress() that is NOT overlapped with anything else: the
	// lookahead and DP halves fork only once it has finished, so its cost is
	// pure serial time in an otherwise two-way-parallel encode. Measured on
	// 9 chunks of matchfinder_test.go's corpus, full versus greedy pass 1:
	// Default 2188.9ms -> 1793.7ms (1.22x), Fast 230.9ms -> 218.5ms
	// (1.06x), Balanced 347.0ms -> 346.5ms and Fastest 164.8ms -> 162.4ms,
	// i.e. nothing on the two rungs whose own parse is cheap enough that
	// pass 1 is a small share of the total either way.
	//
	// The knob is kept, rather than the greedy pass being hard-wired,
	// because "the seed table does not matter" is a measurement on two
	// corpora rather than a theorem: a caller who suspects their data is the
	// exception can turn the thorough pass back on and compare.
	FullFirstPass bool

	// DisableBlockSplit skips both block-splitting trials -- the bounded
	// 2-block midpoint split (encode.go's trySplitChunk) and wimlib's
	// statistics-driven multi-way heuristic (splitstats.go's
	// trySplitChunkStats). Each is an extra full encode (or several) of
	// the token stream, and both run only on the lookahead half of the
	// encoder, so this matters most in a DisableDP run. The whole-chunk
	// VERBATIM and ALIGNED trials are always kept: ALIGNED is a single
	// extra encode of an already-parsed token stream and is not worth a
	// knob.
	//
	// Measured 2026-08-18 on 29.4 MiB of real Windows WIM chunks, setting
	// this on top of DisableDP cost 0.116% of output size (1.222% on
	// deliberately heterogeneous chunks) for no measurable parallel speed
	// win at all -- see Fast's doc. It is kept as a knob because a caller
	// bounded by single-threaded encoder latency can still want the ~3%
	// serial saving, but it is not part of any preset.
	DisableBlockSplit bool
}

// Package defaults, i.e. what each Options field's zero value resolves to.
// These are the values this package used before Options existed, so
// Compress's output is unchanged by the introduction of these knobs.
const (
	defaultBeamWidth            = 10
	defaultMaxFreshCandidates   = 24
	defaultMaxChainLen          = maxChainLen
	defaultRepeatLengthSamples  = 2
	defaultRefinePatience       = refinePatience
	defaultMaxRefineIters       = maxRefineItersHardCap
	minRepeatLengthSamples      = 1
	maxRepeatLengthSamplesValue = 2
)

// encodeOptions is Options with every "0 means default" resolved to the
// concrete value the encoder actually uses, so no code below this point has
// to know about the zero-value convention (or re-derive it per call site,
// which is how such conventions silently drift apart).
type encodeOptions struct {
	dp                  bool
	beamWidth           int
	maxFreshCandidates  int
	maxChainLen         int
	repeatLengthSamples int
	dpHash2             bool
	refinePatience      int
	maxRefineIters      int
	blockSplit          bool
	hashChain           bool
	greedyPass1         bool

	// greedyParse makes findMatchesWith (matcher.go) score each position's
	// candidates by their own value alone, with no lookahead continuation.
	// It is deliberately not set by resolve(): compress() sets it on the
	// copy of these options it hands to pass 1, and only when greedyPass1
	// is on, so that the knob cannot reach the passes whose parse is
	// actually kept.
	greedyParse bool
}

// resolve turns a caller-facing Options into the encoder-facing
// encodeOptions, applying the zero-means-default convention and clamping
// nonsensical values (negative or absurd) to something the encoder can
// actually run with, rather than panicking on them: these are performance
// hints, and a caller passing BeamWidth: -1 wants "as little as possible",
// not a crash in the middle of a WIM export.
func (o Options) resolve() encodeOptions {
	pick := func(v, def, min int) int {
		if v == 0 {
			return def
		}
		if v < min {
			return min
		}
		return v
	}
	r := encodeOptions{
		dp:                  !o.DisableDP,
		beamWidth:           pick(o.BeamWidth, defaultBeamWidth, 1),
		maxFreshCandidates:  pick(o.MaxFreshCandidates, defaultMaxFreshCandidates, 1),
		maxChainLen:         pick(o.MaxChainLen, defaultMaxChainLen, 1),
		repeatLengthSamples: pick(o.DPRepeatLengthSamples, defaultRepeatLengthSamples, minRepeatLengthSamples),
		dpHash2:             !o.DisableDPHash2,
		refinePatience:      pick(o.RefinePatience, defaultRefinePatience, 1),
		maxRefineIters:      pick(o.MaxRefineIters, defaultMaxRefineIters, 1),
		blockSplit:          !o.DisableBlockSplit,
		hashChain:           o.HashChainMatcher,
		greedyPass1:         !o.FullFirstPass,
	}
	if r.repeatLengthSamples > maxRepeatLengthSamplesValue {
		r.repeatLengthSamples = maxRepeatLengthSamplesValue
	}
	return r
}

// defaultEncodeOptions is what the unexported encoder entry points use when
// no Options were threaded in (e.g. this package's own tests calling
// findMatches directly).
func defaultEncodeOptions() encodeOptions { return Options{}.resolve() }

// Fast is the fast rung: no DP parse, a single refinement round, and a
// quarter of the default match-finder search depth -- but block-splitting
// trials KEPT. Measured (see the ladder table above) 8.17s serial /
// 20.5 MB/s parallel, 1.75% larger output than Default, i.e. ~33x
// Default's parallel throughput.
//
// DisableDP is the single highest-leverage knob in Options: profiling
// attributed ~85% of the encoder's cumulative CPU to the DP half, and
// removing it also removes most of the encoder's allocation pressure
// (473.8 GB -> 16.2 GB on the WIM corpus), which is what makes the
// parallel speedup larger than the serial one. Everything below is about
// what to do with the three cheaper knobs on top of it, each measured
// alone against plain Options{DisableDP: true} (13.62s serial /
// 13.8 MB/s parallel / 10892438 bytes) on 2026-08-18:
//
//	knob added              serial  parallel    size cost   size cost
//	                                            WIM corpus  hetero corpus
//	MaxChainLen: 16         11.64s  14.4 MB/s   +0.065%     +0.035%
//	RefinePatience: 1        9.51s  17.3 MB/s   +0.025%     +0.028%
//	DisableBlockSplit       13.16s  12.7 MB/s   +0.116%     +1.222%
//	(all three = the old
//	 Fastest preset)         7.79s  21.5 MB/s   +0.207%     +1.283%
//	MaxChainLen + Patience
//	 (this preset)           8.17s  20.5 MB/s   +0.091%     +0.057%
//
// MaxChainLen: 16 rather than the default 96 is where the search-depth
// knob stops paying: it is the only match-finder knob shared by both
// parsers, hence the only one that still speeds anything up once the DP is
// off. RefinePatience: 1 is the cheapest of the three by size and the
// second largest by speed.
//
// DisableBlockSplit is deliberately NOT set here, and that is the whole
// reason this preset is defined as a combination rather than as "turn
// everything down". Block splitting is nearly free -- adding it to this
// preset measured 8.17s vs 7.79s serial, and in parallel the two are
// within measurement noise of each other (20.5 vs 21.5 MB/s over 7
// interleaved runs; a separate run had this preset FASTER, 22.9 vs 22.5).
// What it buys is the entire fat tail of the size distribution. Alone
// against Options{DisableDP: true} it made 118 of 1170 real WIM chunks
// larger and 0 smaller, 46 of them by more than 1% and 16 by more than 2%,
// worst case +522 bytes (+4.05%) on a 32 KiB chunk of ReachFramework.dll
// straddling a PE section boundary. On the hetero corpus, which is nothing
// but such boundaries, it made 238 of 560 chunks more than 1% larger,
// worst case +878 bytes (+4.19%), for a 1.222% total -- a 10.5x
// amplification of its cost on whole-file data, and still no measurable
// speed win.
//
// That asymmetry is why the "Fastest" rung that existed until 2026-08-18
// was removed. (The Fastest below is a different preset, added on
// 2026-09-03 on a much larger measured speed difference; the two share
// nothing but the name.) That preset set all three knobs; remeasured on
// real WIM data it was at most 5% faster than this one (and sometimes
// slower), for 2.3x the size cost on whole files and 22x on heterogeneous
// ones. Every other step in this ladder is at least 5x wide, so a rung
// that thin was not a rung. A caller who has measured its own workload and
// genuinely wants that last few percent can still write
// Options{DisableDP: true, MaxChainLen: 16, RefinePatience: 1,
// DisableBlockSplit: true} -- the individual fields are exactly the escape
// hatch for that -- but it should not be reached for by name and by
// default.
//
// # A note on the first pass (2026-09-03)
//
// The greedy first pass every rung now gets by default (see
// Options.FullFirstPass) is worth 230.9ms -> 218.5ms, 1.06x, on this
// preset, for a size difference of -0.061% / +0.003% on the two corpora --
// i.e. nothing. The ladder-table serial figure above predates it.
func Fast() Options {
	return Options{
		DisableDP:      true,
		MaxChainLen:    16,
		RefinePatience: 1,
	}
}

// Fastest is Fast with the match-finder *structure* changed rather than
// just its budget: a hash chain instead of a binary tree
// (HashChainMatcher), walked three times as deep (MaxChainLen 48) to buy
// back most of what a chain gives up at equal depth, plus a greedy first
// pass.
//
// Measured 2026-09-03 on matchfinder_test.go's corpus (18 chunks /
// 585,216 bytes: captured WIM chunks plus synthetic noise, runs, x86-like
// code, records and text), whole-corpus serial encode time, mean of 25 runs
// each, otherwise-idle i9-12900K, every row on top of Fast's other knobs:
//
//	match finder            time    vs BST/16  size       worst chunk
//	BST,   MaxChainLen 16   234.1ms   --       252218     --
//	chain, MaxChainLen 16   157.8ms   1.48x    +1.784%    +13.89%
//	chain, MaxChainLen 32   167.9ms   1.39x    +1.200%     +8.76%
//	chain, MaxChainLen 48   165.9ms   1.41x    +0.834%     +4.37%
//	chain, MaxChainLen 64   178.2ms   1.31x    +0.629%     +3.32%
//	chain, MaxChainLen 96   183.9ms   1.27x    +0.379%     +4.05%
//
// (Those rows isolate the match finder: none of them sets GreedyFirstPass,
// which Fast now also sets, so the tree row is 234.1ms rather than the
// ~220ms Fast itself measures today. Preset against preset, as
// BenchmarkFastPresets runs them, it is 219.3ms vs 166.3ms in one 36-run
// set and 221.1ms vs 164.5ms in another -- 1.32x to 1.34x -- for the
// +0.827% of output the ratio test reports.)
//
// The greedy first pass every rung now gets by default (see
// Options.FullFirstPass) is worth less here than anywhere else -- 162.4ms
// vs 164.8ms mean over 48 runs, i.e. nothing outside the noise -- because a
// chain's parse is cheap enough that pass 1 is a smaller share of the
// total. It is not opted out of, because it costs nothing either.
//
// A chain finds worse matches than a tree at equal depth; that is not in
// dispute, and the first chain row is what it costs. What the sweep shows
// is that a chain node is cheap enough (a prepend rather than a rewiring
// descent, half the per-position memory, and an exact one-byte reject test
// before any comparison -- see matchfinder.go's newChainMatcher) that three
// times the depth still runs 1.41x faster than the tree, and at that depth
// most of the ratio comes back.
//
// Depth 48 rather than 16 for a 5% speed difference is the same judgment
// DisableBlockSplit gets above, and for the same reason: the worst-chunk
// column. Depth 16 more than triples the tail (+13.89% on a single chunk,
// on run-heavy data, where a tree's ordered search is at its most
// valuable), and this package has repeatedly declined to buy a few percent
// of speed with that kind of amplification. Depth 48's +4.37% worst case is
// in the range the ladder's other rungs already span.
//
// This is a separate rung rather than a change to Fast because +0.827% of
// output is a real cost, not a rounding error, on a ladder whose entire
// Fast-to-Default span is 1.75%: a caller who wants Fast's ratio should
// keep getting exactly Fast's ratio. It IS a rung, unlike the old "Fastest"
// preset removed on 2026-08-18 (which was at most 5% faster than Fast for
// 2.3x the size cost on whole files and 22x on heterogeneous ones): 1.33x
// for +0.827% is a genuinely different (speed, size) point.
//
// A 4-byte bucket hash was tried here too, on the theory that the rung
// named "fastest" should take every speed knob going. It does speed up the
// TREE (216ms vs 228ms, 1.06x, for +0.842% size) but not the chain, which
// is what this preset uses: every 4-byte chain configuration measured was
// slower, or larger, or both, than the 3-byte chain/48 this preset ships
// (chain/96 with a 4-byte hash: 173.3ms mean, +0.223% size; chain/128:
// 182.0ms, +0.147%). A chain already rejects a false neighbor in one byte
// compare (see matchfinder.go's newChainMatcher), so it has much less to
// gain from cleaner buckets than a tree, whose whole budget goes into an
// ordered descent that false neighbors misdirect -- while both pay the same
// price, namely that a match whose only candidate is exactly 3 bytes long
// becomes unfindable. It is not kept as an option; see matcher.go's hash
// for the record.
func Fastest() Options {
	return Options{
		DisableDP:        true,
		HashChainMatcher: true,
		MaxChainLen:      48,
		RefinePatience:   1,
	}
}

// Balanced keeps the DP parser but runs it much more cheaply: a beam a
// quarter as wide, a third as many fresh-offset candidates per position,
// only the full-length repeat sample, no DP-side length-2 (hash2) edges,
// and a single refinement round. Measured (see the ladder table above)
// 2.07s serial on the 30-chunk subset / 3.2 MB/s parallel, 0.66% larger
// output than Default.
//
// This rung exists because the measured curve between Fast and Default has
// a very large gap in it (20.5 -> 0.6 MB/s for 1.75% of size), and this
// combination recovers 62% of that size difference for 17% of the time
// difference (parallel column of the ladder table). The individual
// contributions, measured alone on the earlier 832 KiB serial corpus
// against its Default of 15.34s / 255516 bytes: BeamWidth 4 ->
// 5.00s / 255850, BeamWidth 2 -> 2.61s / 256524, RefinePatience 1 ->
// 9.06s / 255662, DisableDPHash2 -> 10.06s / 256006, DPRepeatLengthSamples
// 1 -> 13.98s / 255598, MaxFreshCandidates 8 -> 14.84s / 255528. Beam
// width dominates; the rest are cheap add-ons once it is narrowed.
func Balanced() Options {
	return Options{
		BeamWidth:             4,
		MaxFreshCandidates:    8,
		DPRepeatLengthSamples: 1,
		DisableDPHash2:        true,
		RefinePatience:        1,
	}
}

// DefaultOptions returns the zero Options, i.e. exactly what Compress
// does. It exists so a caller selecting a preset by name has a name for
// this rung too, and so that "the default" can be written the same way as
// the other rungs rather than as a bare struct literal.
func DefaultOptions() Options { return Options{} }

// Max spends substantially more time for a small further size win: a beam
// 1.6x wider, a third more fresh-offset candidates, and refinement
// patience 6 rather than 2. Measured (see the ladder table above) 44.11s
// serial on the 30-chunk subset / 0.2 MB/s parallel, 0.07% smaller output
// than Default -- i.e. 3.8x Default's time for 8016 bytes out of 10.7 MB.
//
// It is offered for callers compressing something small once and keeping
// it forever, where encoder time is genuinely free. It is NOT the default
// precisely because that trade is so steep; see refinePatience's own doc
// (encode.go) for the earlier measurement that first established this.
func Max() Options {
	return Options{
		BeamWidth:          16,
		MaxFreshCandidates: 32,
		RefinePatience:     6,
	}
}
