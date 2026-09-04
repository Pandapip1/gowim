package lzms

// Options selects this encoder's speed/compression-ratio tradeoff.
//
// The zero value means "this package's defaults", i.e. exactly what
// Compress does, so CompressWith(data, Options{}) is identical (byte for
// byte, not merely equivalent) to Compress(data). MaxChainLen follows the
// same convention as every numeric field in lzx's Options: 0 means "use the
// default".
//
// Nothing here changes the format: every setting produces a valid LZMS
// stream that any conforming decoder (this package's Decompress, or
// wimlib's) decodes back to the original bytes. The only thing this knob
// trades is encoder time against output size.
type Options struct {
	// MaxChainLen bounds the hash-chain match finder's per-position
	// comparison budget -- the classic zlib-style "search depth" (see
	// encode.go's findMatch). Widening it lets the match finder look
	// further back in the hash chain for a longer or closer match, at the
	// cost of more comparisons per position; narrowing it trades away
	// match quality for encoder speed. 0 means defaultMaxChainLen (64).
	// Ignored when LiteralOnly is set, since no matches are searched for
	// at all in that mode.
	MaxChainLen int

	// LiteralOnly skips match-finding entirely (encode.go's findMatch and
	// the hash-chain insertion loop never run) and encodes every input
	// byte as a literal, in order.
	//
	// This is NOT a raw/stored mode: LZMS has no such block type in its
	// format. The literal alphabet is coded with a mandatory adaptive
	// Huffman code that both encoder and decoder evolve identically via
	// periodic rebuilds (huffman.go's afterDecodeOrEncode/rebuild, every
	// litCodeRebuildFreq symbols -- see decode.go), and each literal is
	// preceded by a range-coded "is this a match?" bit. So even with this
	// flag set, every byte still pays real per-symbol coding cost; the
	// flag only removes the cost of searching for matches, at the cost of
	// coding far more symbols overall when the input actually had matches
	// to find (a match that would have covered many bytes becomes that
	// many separate literal-code calls instead of one match-code call).
	//
	// Measured (literalonly_bench_test.go, go test -bench, otherwise idle
	// i9-12900K, Go 1.26) on two 32 KiB corpora -- "compressible": a
	// repeated multi-sentence phrase; "random": math/rand bytes -- each
	// run's ns/op and output size, default vs LiteralOnly:
	//
	//	corpus        Options{}              LiteralOnly            verdict
	//	compressible  331.7us,    166 B out   647.0us,  20600 B out  1.95x SLOWER,
	//	                                                              124x bigger
	//	random        2412.6us, 33082 B out   1070.9us, 33082 B out  2.25x faster,
	//	                                                              same size
	//
	// LiteralOnly is a genuine win only on data that is already
	// (near-)incompressible: the default match finder there searches hard
	// for matches that mostly don't exist, so skipping that search is
	// close to pure savings, and the output is unchanged because the
	// default encoder was already emitting all-literal output on this
	// corpus anyway (no match ever reached minEncodeMatchLength). On
	// compressible data it is a clear LOSS on both axes: skipping matches
	// means every byte a match would have absorbed becomes its own
	// literal-code call (bit-write + frequency-update + periodic-rebuild
	// check, per huffman.go's afterDecodeOrEncode), which costs more
	// encoder time than the match search it replaces, while also
	// discarding the very thing that made the data compressible.
	//
	// Left as an Options field rather than a named preset (see lzx's
	// Fastest/Fast for that convention) because there is no ladder
	// position where this is the right default choice: WIM chunks are
	// rarely fully incompressible, so a preset a caller reaches for by
	// name would be a trap on the common case. It exists for a caller who
	// has already determined -- from its own data, e.g. already-compressed
	// formats being re-chunked -- that match-finding is wasted effort on
	// what it is about to compress.
	LiteralOnly bool
}

// defaultMaxChainLen is the value MaxChainLen resolves to when left at its
// zero value. This is the value this package used before Options existed,
// so Compress's output is unchanged by the introduction of this knob.
const defaultMaxChainLen = 64

// resolvedOptions is Options with the zero-means-default convention already
// applied, so encode.go's compress() never has to know about that
// convention or re-derive it.
type resolvedOptions struct {
	maxChainLen int
	literalOnly bool
}

// resolve turns a caller-facing Options into the concrete values the
// encoder actually uses, applying the zero-means-default convention and
// clamping nonsensical (e.g. negative) values to a sane minimum rather than
// panicking on them: this is a performance hint, and a caller passing
// MaxChainLen: -1 wants "as little as possible", not a crash in the middle
// of a WIM export.
func (o Options) resolve() resolvedOptions {
	chainLen := defaultMaxChainLen
	if o.MaxChainLen != 0 {
		chainLen = o.MaxChainLen
		if chainLen < 1 {
			chainLen = 1
		}
	}
	return resolvedOptions{
		maxChainLen: chainLen,
		literalOnly: o.LiteralOnly,
	}
}
