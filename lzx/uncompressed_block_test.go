package lzx

import (
	"bytes"
	_ "embed"
	"testing"
)

// realUncompressedBlockChunkCompressed and realUncompressedBlockChunkPlain
// are a real 32768-byte WIM chunk's compressed bytes and known-correct
// plaintext, copied verbatim (2026-07-14) from a real Windows 11 25H2
// install.wim (Windows_11_25H2_EnglishInternational_x64_v2.iso's "Windows
// 11 Pro" edition, sources\install.wim): specifically chunk 60 of
// Windows\WinSxS\amd64_windows-senseclient-service_31bf3856ad364e35_10.0.26100.1_none_a518656df7c165fb\nl7models0804.dll
// (a Windows Defender ML model binary -- large, low-redundancy data, which
// is what triggers a real LZX_BLOCKTYPE_UNCOMPRESSED block in practice; hash
// b2c98a647df4ae654705256b6fa0bb786c3e524c's blob-table entry, extracted
// via a disposable diagnostic program reading the WIM's blob table/resource
// bytes directly). The known-correct plaintext was independently obtained
// via `wimlib-imagex extract` (a real, independent reference decoder) on the
// same file, not derived from this package's own (buggy, at the time)
// output.
//
// This chunk reproduces a real decoder bug found via this exact file
// (2026-07-14): bitReader.align, called at the start of an uncompressed
// block, discarded any bits already buffered but never replicated LZX's
// documented quirk of always throwing away one *additional* whole 16-bit
// coding unit when the bitstream happens to already be aligned (see
// bitReader.align's doc comment, and wimlib's own
// src/lzx_decompress.c comment: "if the stream is *already* aligned, the
// correct thing to do is to throw away the next 16 bits (this is probably
// a mistake in the format)"). Chunk 60 here lands exactly on that
// already-aligned case, desynchronizing every block header read after it
// until align was fixed to call ensure(1) before discarding, matching
// wimlib's own "ensure_bits(is, 1); bitstream_align(is);" sequence exactly.
//
//go:embed testdata/real_uncompressed_block_chunk_compressed.bin
var realUncompressedBlockChunkCompressed []byte

//go:embed testdata/real_uncompressed_block_chunk_plain.bin
var realUncompressedBlockChunkPlain []byte

func TestDecompressRealUncompressedBlockChunk(t *testing.T) {
	got, err := Decompress(realUncompressedBlockChunkCompressed, len(realUncompressedBlockChunkPlain))
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(got, realUncompressedBlockChunkPlain) {
		t.Fatalf("real-data decode mismatch (len got=%d want=%d)", len(got), len(realUncompressedBlockChunkPlain))
	}
}
