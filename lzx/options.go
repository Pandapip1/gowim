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
// (Fast, Balanced, DefaultOptions, Max -- an ordered ladder, each a
// specific measured combination of the fields below) are the intended
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
// Fast, Balanced, DefaultOptions and Max are an ordered ladder of measured
// (speed, size) points, from fastest/largest to slowest/smallest. Each is a
// specific combination of the fields above, chosen by measuring the
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
//	Fast         8.17s   20.5 MB/s   10902398    11.6 GB   +1.75% size
//	Balanced     2.07s*   3.2 MB/s   10785178    87.9 GB   +0.66% size
//	Default     11.64s*   0.6 MB/s   10714428   473.8 GB   --
//	Max         44.11s*   0.2 MB/s   10706412  1049.6 GB   -0.07% size
//
// (*) Balanced, Default and Max are far too slow to time over 585 chunks;
// their serial column is a 30-chunk / 818 KiB subset of the same corpus.
// Output and alloc columns are always the full 1170-chunk corpus.
//
// The ladder is deliberately not evenly spaced: the honest measured shape
// of this encoder is that nearly all of the DP parser's compression win is
// available at a small fraction of its cost (Balanced is 5x faster than
// Default for 0.66% more output), and the last half-percent is what costs
// the remaining order of magnitude. Every step in it is at least 5x wide;
// see Fast's doc for the rung that was measured, found to be ~5% wide, and
// removed for it.
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

	// MaxChainLen bounds the binary-tree match finder's per-position
	// comparison budget -- the classic zlib-style "search depth" (see
	// matcher.go's bstSearch/bstInsert). Unlike the other match-finder
	// knobs here it is shared by BOTH parsers, so it is the only one that
	// also speeds up a DisableDP run. 0 means defaultMaxChainLen (96).
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
// That asymmetry is why there is no separate "Fastest" rung any more. The
// preset that set all three knobs existed until 2026-08-18; remeasured on
// real WIM data it was at most 5% faster than this one (and sometimes
// slower), for 2.3x the size cost on whole files and 22x on heterogeneous
// ones. Every other step in this ladder is at least 5x wide, so a rung
// that thin was not a rung. A caller who has measured its own workload and
// genuinely wants that last few percent can still write
// Options{DisableDP: true, MaxChainLen: 16, RefinePatience: 1,
// DisableBlockSplit: true} -- the individual fields are exactly the escape
// hatch for that -- but it should not be reached for by name and by
// default.
func Fast() Options {
	return Options{
		DisableDP:      true,
		MaxChainLen:    16,
		RefinePatience: 1,
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
