package xpress

// Options selects this encoder's compression-search behavior.
//
// The zero value means "this package's defaults", i.e. exactly what
// Compress does, so CompressWith(data, Options{}) is identical (byte for
// byte, not merely equivalent) to Compress(data). This follows the same
// zero-means-default convention as lzx.Options and lzms.Options.
//
// Nothing here changes the format: every Options value produces a valid
// XPRESS LZ77+Huffman stream that any conforming decoder (this package's
// Decompress, or a real WIM decoder's) decodes back to the original bytes.
// The only thing this knob trades is encoder search (and hence time and
// compression ratio) against raw speed.
type Options struct {
	// SkipSearch disables all compression search: no LZ77 match finding
	// (lz77.go's matchFinder/findMatch never run) and no adaptive Huffman
	// tree construction (huffman.go's buildLengths, and the
	// container/heap machinery it uses, never run). Every input byte is
	// emitted as a literal, coded under a fixed flat code -- see flatLens
	// in huffman.go, whose doc proves that code's codeword for literal
	// byte b is exactly b, for every b except 255 (which, along with
	// endOfData, costs 9 bits instead of 8 so the stream can still carry
	// a valid end-of-data marker).
	//
	// This trades away essentially all compression ratio (the output is
	// close to len(data) plus the fixed 256-byte Huffman header, since
	// literals are coded 1:1 into 8-bit codewords) for speed: the encoder
	// does no per-byte search of any kind, only a single linear pass
	// writing each byte's fixed codeword. False (the default) runs the
	// normal greedy/lazy LZ77 parse plus adaptive Huffman code selection.
	SkipSearch bool
}

// None returns Options that skip all compression search (see
// Options.SkipSearch): no LZ77 matching, no adaptive/data-driven Huffman
// tree construction -- just a fixed flat code, identity for every literal
// byte except 255, applied to every input byte as a literal. It is meant
// for callers that value raw encoder speed over ratio (e.g. producing
// throwaway or already-compressed data, or a fast path under time
// pressure), or that want a straightforward reference encoding to compare
// other output against.
//
// The result is a fully spec-compliant XPRESS stream, decodable by this
// package's own Decompress with no changes and by any other conforming
// XPRESS decoder, end-of-data marker included: output size is close to
// len(data) plus the fixed 256-byte Huffman header, and CompressWith with
// this preset is dramatically faster than the default since it does no
// match finding and no Huffman tree construction at all.
func None() Options {
	return Options{SkipSearch: true}
}
