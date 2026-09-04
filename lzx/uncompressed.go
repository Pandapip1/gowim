package lzx

// writeUncompressedBlock writes a single WIM-flavor LZX UNCOMPRESSED block
// (blockTypeUncompressed) covering the whole of data verbatim -- no
// literal/match tokens, no Huffman tables -- into an existing, possibly
// already-in-progress bitWriter w. This is the entire output path for
// Options.Uncompressed (see options.go's None preset): compress() calls
// straight into this instead of running the match finder(s), the DP
// parser, block splitting, or any Huffman table construction at all.
//
// Layout, ported from wimlib's src/lzx_decompress.c
// LZX_BLOCKTYPE_UNCOMPRESSED case (the authoritative read side this must
// match bit-for-bit is decode.go's own blockTypeUncompressed case):
//
//   - The ordinary 3-bit block-type field (blockTypeUncompressed = 3) and
//     block-size field (the same "1 bit for the 32768 default size, else
//     16 or 24 explicit bits depending on window order" encoding every
//     other block type uses -- see writeBlockInto).
//   - A realignment to the next 16-bit coding-unit boundary (bitWriter.
//     align), which reproduces the format's documented quirk of always
//     discarding a whole extra unit when the stream happens to already be
//     aligned -- see bitReader.align's doc and wimlib's own comment on it.
//   - The 3-entry recent-offsets queue, as three raw little-endian u32
//     values. Written here as this package's own initial queue state,
//     {1, 1, 1} (see decompress's recentOffsets initialization): an
//     UNCOMPRESSED block carries no LZ matches, so there is no real queue
//     state to preserve, and {1, 1, 1} is guaranteed nonzero (decode.go
//     rejects a zero entry) regardless of anything else in the chunk.
//   - The block's bytes verbatim.
//   - One zero pad byte if the block length is odd, keeping whatever
//     follows (the next block's header, if any, or nothing) on a
//     coding-unit boundary.
//
// A single UNCOMPRESSED block always suffices for the whole chunk: the
// block-size field is 16 bits wide exactly when the chunk's window order is
// below 16, which (per windowOrder) only happens for a chunk of at most
// 32768 bytes -- comfortably under the 16-bit field's 65535 maximum -- and
// 24 bits wide otherwise, comfortably covering maxWindowSize (2097152, the
// largest input Compress/CompressWith ever accepts). So, unlike VERBATIM/
// ALIGNED block splitting, there is never a need to split an UNCOMPRESSED
// encoding across more than one block, and this function never does.
func writeUncompressedBlock(w *bitWriter, data []byte, order int) {
	w.writeBits(blockTypeUncompressed, 3)
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

	w.align()
	w.writeRawU32(1)
	w.writeRawU32(1)
	w.writeRawU32(1)

	w.writeRawBytes(data)
	if len(data)&1 != 0 {
		w.writeRawBytes([]byte{0})
	}
}
