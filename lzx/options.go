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
// Each is a specific combination of the fields above, chosen by measuring
// the individual knobs rather than by guessing which ones would matter.
//
// All numbers below were measured on 2026-08-18 on a 24-core x86-64 Linux
// machine (Go 1.26.5), 32 KiB chunks through CompressWith, on two corpora:
//
//   - "serial": 832 KiB, 26 chunks, compressed one at a time on one
//     goroutine -- 128 KiB each of /usr/bin/bash, libc.so.6,
//     libLLVM.so.18.1, /usr/share/dict/american-english, concatenated
//     Debian copyright text, Go net/http source, plus this package's two
//     testdata chunks. This isolates per-chunk encoder cost.
//   - "parallel": 4 MiB, 128 chunks, compressed concurrently across all 24
//     cores the way the `wim` package drives this encoder -- 2 MiB of
//     libLLVM.so.18.1, 1 MiB of Go runtime+net/http source, 512 KiB of
//     dictionary words, 512 KiB of libc.so.6. This is the number that
//     predicts real WIM-export wall time, and it is NOT simply 24x the
//     serial number: at the default settings the encoder allocates fast
//     enough (measured 9.1 GB/s, 74.7 GB total for those 4 MiB) that GC,
//     not compute, is the binding constraint.
//
// Measured, same input both columns (output bytes / parallel throughput /
// total allocation for the 4 MiB parallel corpus):
//
//	preset      serial     parallel    output      alloc    vs Default
//	Fastest     0.54s      20.2 MB/s   1617878     1.5 GB   +2.91% size
//	Fast        0.80s      13.8 MB/s   1617228     2.2 GB   +2.87% size
//	Balanced    2.33s       2.9 MB/s   1580330    13.2 GB   +0.52% size
//	Default    15.34s      0.51 MB/s   1572132    74.7 GB   --
//	Max        47.84s      0.20 MB/s   1571204   153.8 GB   -0.06% size
//
// (Serial column is the 832 KiB corpus's total time; output/alloc columns
// are the 4 MiB parallel corpus.) For orientation, this package's encoder
// as of commit cfe02d5 -- before the DP parser was refined into the
// default path -- measured 0.94s serial / 8.1 MB/s parallel at 1593160
// bytes, i.e. Fast is measurably faster than that older encoder (13.8 vs
// 8.1 MB/s, for 1.5% more output), and Balanced is smaller than it
// (1580330 vs 1593160) though 2.8x slower.
//
// The ladder is deliberately not evenly spaced: the honest measured shape
// of this encoder is that nearly all of the DP parser's compression win is
// available at a small fraction of its cost (Balanced is 6.6x faster than
// Default for 0.52% more output), and the last half-percent is what costs
// the remaining order of magnitude.
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

// Fastest is the fastest preset: no DP parse, no block-splitting trials, a
// single refinement round, and a quarter of the default match-finder
// search depth. Measured (see the ladder table above) 0.54s serial /
// 20.2 MB/s parallel, 2.91% larger output than Default -- ~40x Default's
// parallel throughput.
//
// MaxChainLen: 16 rather than the default 96 is where the search-depth
// knob stops paying on the measured corpora: with the DP off, 96 -> 16
// measured 0.80s -> 0.74s serial and cost nothing at all in size (265042
// -> 265030 bytes), while going on to 8 (with this preset's other knobs
// also set) bought 0.54s -> 0.51s for 418 more bytes. It is included here because it is the
// only knob that speeds up the lookahead parser, which is all that is left
// once the DP is off.
func Fastest() Options {
	return Options{
		DisableDP:         true,
		MaxChainLen:       16,
		RefinePatience:    1,
		DisableBlockSplit: true,
	}
}

// Fast disables only the DP parser, leaving every other default alone: the
// bounded-lookahead parser with its full refinement loop, block-splitting
// trials, and search depth. Measured 0.80s serial / 13.8 MB/s parallel,
// 2.87% larger output than Default.
//
// This is the single highest-leverage knob in Options: profiling
// attributed ~85% of the encoder's cumulative CPU to the DP half, and
// removing it also removes essentially all of the encoder's allocation
// pressure (74.7 GB -> 2.2 GB on the 4 MiB parallel corpus), which is what
// makes the parallel speedup (27x) larger than the serial one (19x).
//
// Fast rather than Fastest is the right default choice for a caller that
// wants "as fast as reasonable" but has no measurement of its own: the
// extra knobs Fastest turns down buy a further ~1.5x for only 650 bytes in
// 1.6 MB, so they are worth taking only when throughput genuinely
// dominates.
func Fast() Options { return Options{DisableDP: true} }

// Balanced keeps the DP parser but runs it much more cheaply: a beam a
// quarter as wide, a third as many fresh-offset candidates per position,
// only the full-length repeat sample, no DP-side length-2 (hash2) edges,
// and a single refinement round. Measured 2.33s serial / 2.9 MB/s
// parallel, 0.52% larger output than Default.
//
// This rung exists because the measured curve between Fast and Default has
// a very large gap in it (13.8 -> 0.51 MB/s for 2.87% of size), and this
// combination recovers 82% of that size difference for 14% of the time
// difference (parallel corpus; 11% on the serial one). The individual contributions on the serial corpus, each
// measured alone against Default's 15.34s / 255516 bytes: BeamWidth 4 ->
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
// patience 6 rather than 2. Measured 47.84s serial / 0.20 MB/s parallel,
// 0.06% smaller output than Default -- i.e. 3.1x Default's time for 928
// bytes out of 1.57 MB.
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
