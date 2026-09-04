package wim

import (
	"testing"
)

// benchResourceData tiles the real, embedded xpress_setup_exe.bin fixture
// (335936 bytes uncompressed, 11 chunks at 32768-byte chunk size -- see
// testdata_test.go) up to a few MiB. Chunks compress completely
// independently (no shared window/Huffman state crosses a chunk boundary,
// see compressChunksParallel's doc), so tiling at whole-copy granularity
// does not change the per-chunk compression ratio the fixture already
// exhibits: this reproduces measured-real data's ~2.17x XPRESS compression
// ratio (see TestRatioProbe-style check during development) at a size
// large enough (several MiB, 100+ chunks) for the chunk-table-building
// loop and the final out allocation in EncodeResourceDataWith to matter,
// and to keep encodeBlobsPipeline's "many blobs in flight" over-allocation
// concern realistic without needing lzx's much slower codec to reach that
// size in reasonable benchmark time.
func benchResourceData(tb testing.TB, targetSize int) []byte {
	tb.Helper()
	base, err := DecodeResourceData(xpressSetupExeResource, HdrFlagCompressXPRESS, 32768, 335936)
	if err != nil {
		tb.Fatalf("DecodeResourceData(embedded xpress setup.exe): %v", err)
	}
	out := make([]byte, 0, targetSize+len(base))
	for len(out) < targetSize {
		out = append(out, base...)
	}
	return out
}

// BenchmarkEncodeResourceDataWith_Xpress4MiB measures EncodeResourceDataWith
// over ~4 MiB of realistically-compressible data (see benchResourceData),
// with -benchmem so bytes/op and allocs/op are reported alongside ns/op.
//
// This benchmark exists specifically to measure the fix for the audit
// finding that `out` in EncodeResourceDataWith was allocated with capacity
// len(table)+uncompressedSize -- i.e. sized for the UNCOMPRESSED total --
// even though `out` only ever holds the COMPRESSED chunk bytes. For data
// compressing at ~2.17x (this fixture's real ratio under XPRESS), that
// over-reserved roughly 2.17x the memory `out` actually needed, and since
// encodeBlobsPipeline keeps up to 2*GOMAXPROCS blobs in flight at once
// (blob_pipeline.go), that per-call over-allocation multiplies directly
// into peak RSS during a WIM export. B/op is the number to compare
// before/after this fix; ns/op should be roughly unchanged since no bytes
// written to `out` changed, only the capacity `out` starts with.
func BenchmarkEncodeResourceDataWith_Xpress4MiB(b *testing.B) {
	data := benchResourceData(b, 4<<20)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	var lastOutLen int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, flags, err := EncodeResourceData(data, HdrFlagCompressXPRESS, 32768)
		if err != nil {
			b.Fatalf("EncodeResourceData: %v", err)
		}
		if flags == 0 {
			b.Fatalf("expected ResFlagCompressed for compressible fixture data, got flags=0")
		}
		lastOutLen = len(out)
	}
	b.StopTimer()
	b.Logf("input=%d bytes, output=%d bytes, ratio=%.2fx", len(data), lastOutLen, float64(len(data))/float64(lastOutLen))
}
