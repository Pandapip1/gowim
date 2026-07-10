package xpress

// Constants describing the shape of the XPRESS LZ77+Huffman alphabet and
// match encoding. These mirror wimlib's xpress_constants.h (see the package
// doc in xpress.go for the exact source commit).
const (
	// numChars is the number of literal-byte symbols (0-255).
	numChars = 256
	// numSymbols is the total Huffman alphabet size: 256 literals plus
	// 256 match-header symbols (256-511).
	numSymbols = 512
	// maxCodewordLen is the maximum length in bits of a Huffman codeword;
	// codeword lengths are transmitted as 4-bit nibbles, so this cannot
	// exceed 15.
	maxCodewordLen = 15

	// endOfData is the symbol conventionally written after the last real
	// item so that decoders that expect an explicit end marker (such as
	// Microsoft's WIMGAPI) are satisfied. See the discussion in xpress.go.
	endOfData = 256

	// minOffset and maxOffset bound the LZ77 match distance. The maximum
	// is fixed at 65535 because the "extra offset bits" mechanism can
	// address at most a 16-bit span (a match's offset is decomposed as
	// 1<<log2Offset plus up to log2Offset extra bits, and log2Offset is
	// itself a 4-bit field with range 0-15).
	minOffset = 1
	maxOffset = 65535

	// minMatchLen and maxMatchLen bound the LZ77 match length. The
	// minimum of 3 is baked into the format (shorter matches are never
	// worth encoding); the maximum follows from the length being encoded
	// as (adjusted length = length-3) fitting in at most 16 bits via the
	// nibble/byte/u16 extension scheme.
	minMatchLen = 3
	maxMatchLen = 65538

	// huffmanHeaderSize is the size in bytes of the fixed Huffman
	// codeword-length table that always begins an XPRESS compressed
	// buffer: 512 symbols packed two per byte as 4-bit lengths.
	huffmanHeaderSize = numSymbols / 2
)
