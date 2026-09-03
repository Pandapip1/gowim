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
	MaxChainLen int
}

// defaultMaxChainLen is the value MaxChainLen resolves to when left at its
// zero value. This is the value this package used before Options existed,
// so Compress's output is unchanged by the introduction of this knob.
const defaultMaxChainLen = 64

// resolve turns a caller-facing Options into the concrete maxChainLen value
// the encoder actually uses, applying the zero-means-default convention and
// clamping nonsensical (e.g. negative) values to a sane minimum rather than
// panicking on them: this is a performance hint, and a caller passing
// MaxChainLen: -1 wants "as little as possible", not a crash in the middle
// of a WIM export.
func (o Options) resolve() int {
	if o.MaxChainLen == 0 {
		return defaultMaxChainLen
	}
	if o.MaxChainLen < 1 {
		return 1
	}
	return o.MaxChainLen
}
