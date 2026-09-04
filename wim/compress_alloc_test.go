package wim

import (
	"testing"

	"github.com/Pandapip1/gowim/lzx"
)

// TestAssembleCompressedPayloadAllocatesExactCapacity is the regression test
// for the audit finding that the final `out` buffer in
// EncodeResourceDataWith (now built by assembleCompressedPayload) was
// allocated with capacity len(table)+uncompressedSize -- i.e. sized for the
// UNCOMPRESSED total -- even though it only ever holds the COMPRESSED
// table+chunk bytes. For well-compressing data that reserves several times
// the memory actually needed, and since encodeBlobsPipeline
// (blob_pipeline.go) keeps up to 2*GOMAXPROCS blobs in flight at once, the
// per-call over-allocation multiplied directly into peak RSS during a WIM
// export.
//
// This asserts two things directly against assembleCompressedPayload's
// result, which the fix's own doc comment promises:
//
//  1. cap(out) == len(out): the buffer's backing array was allocated at
//     exactly the final size, so neither of the two append calls inside
//     assembleCompressedPayload ever had to grow (and therefore copy) it.
//  2. cap(out) is far smaller than len(table)+uncompressedSize (the old,
//     buggy capacity) for data that actually compresses well -- proving
//     the fix's benefit is real and not just a no-op restatement of the
//     old formula.
func TestAssembleCompressedPayloadAllocatesExactCapacity(t *testing.T) {
	// A synthetic, highly-compressible multi-chunk payload: several chunks
	// of a repeated byte pattern, each of which XPRESS collapses to a tiny
	// fraction of its uncompressed size, so the old uncompressedSize-sized
	// capacity would have been dramatically larger than the real one.
	const chunkSize = 32768
	const numChunks = 8
	data := make([]byte, chunkSize*numChunks)
	for i := range data {
		data[i] = byte(i % 4) // trivially compressible, but not empty/all-zero
	}
	uncompressedSize := uint64(len(data))

	chunks, err := compressChunksParallel(data, HdrFlagCompressXPRESS, chunkSize, uncompressedSize, numChunks, lzx.Options{})
	if err != nil {
		t.Fatalf("compressChunksParallel: %v", err)
	}

	out := assembleCompressedPayload(chunks, uncompressedSize)

	if cap(out) != len(out) {
		t.Errorf("cap(out) = %d, len(out) = %d; want equal (no append should have needed to grow the backing array)", cap(out), len(out))
	}

	entrySize := chunkTableEntrySize(uncompressedSize)
	oldCapacity := (numChunks-1)*entrySize + int(uncompressedSize)
	if cap(out) >= oldCapacity {
		t.Errorf("cap(out) = %d did not shrink below the old uncompressedSize-based capacity %d for compressible data", cap(out), oldCapacity)
	}
	t.Logf("uncompressedSize=%d, old (buggy) capacity=%d, new exact capacity=%d (%.1fx smaller)",
		uncompressedSize, oldCapacity, cap(out), float64(oldCapacity)/float64(cap(out)))
}
