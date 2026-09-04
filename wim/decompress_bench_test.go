package wim

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/Pandapip1/gowim/lzms"
	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/xpress"
)

// decodeResourceDataOld is a byte-for-byte reproduction of the pre-optimization
// DecodeResourceData / decompressChunk pair: each chunk is decoded into a
// codec-allocated buffer (Decompress) and then copied a second time into out
// via append, and out itself starts life as make([]byte, 0, uncompressedSize)
// rather than a fully-sized buffer. It exists only so this file can measure,
// with a real benchmark rather than an assertion, how much the DecompressInto
// refactor actually saves relative to the shape of code it replaced.
func decodeResourceDataOld(payload []byte, ctype CompressionType, chunkSize uint32, uncompressedSize uint64) ([]byte, error) {
	if uncompressedSize == 0 {
		return nil, nil
	}
	if chunkSize == 0 {
		return nil, fmt.Errorf("wim: decode resource: chunk size is zero but resource is compressed")
	}
	numChunks := numChunksFor(uncompressedSize, chunkSize)

	var offsets []uint64
	var chunksStart int

	if numChunks > 1 {
		entrySize := chunkTableEntrySize(uncompressedSize)
		tableLen := int(numChunks-1) * entrySize
		if tableLen > len(payload) {
			return nil, fmt.Errorf("wim: decode resource: chunk table (%d bytes) exceeds payload (%d bytes)", tableLen, len(payload))
		}
		offsets = make([]uint64, numChunks)
		offsets[0] = 0
		for i := uint64(0); i < numChunks-1; i++ {
			off := int(i) * entrySize
			var v uint64
			if entrySize == 4 {
				v = uint64(le.Uint32(payload[off : off+4]))
			} else {
				v = le.Uint64(payload[off : off+8])
			}
			offsets[i+1] = v
		}
		chunksStart = tableLen
	} else {
		offsets = []uint64{0}
	}

	out := make([]byte, 0, uncompressedSize)
	for i := uint64(0); i < numChunks; i++ {
		start := chunksStart + int(offsets[i])
		var end int
		if i+1 < numChunks {
			end = chunksStart + int(offsets[i+1])
		} else {
			end = len(payload)
		}
		if start < 0 || end > len(payload) || start > end {
			return nil, fmt.Errorf("wim: decode resource: chunk %d has invalid bounds [%d,%d) in payload of length %d", i, start, end, len(payload))
		}
		chunkData := payload[start:end]
		usize := int(chunkUncompressedSize(i, numChunks, uncompressedSize, chunkSize))

		if len(chunkData) == usize {
			out = append(out, chunkData...)
			continue
		}
		dec, err := decompressChunkOld(ctype, chunkData, usize)
		if err != nil {
			return nil, fmt.Errorf("wim: decode resource: chunk %d: %w", i, err)
		}
		out = append(out, dec...)
	}
	return out, nil
}

// decompressChunkOld is the pre-optimization decompressChunk: it calls each
// codec's allocating Decompress rather than DecompressInto.
func decompressChunkOld(ctype CompressionType, data []byte, uncompressedSize int) ([]byte, error) {
	switch ctype {
	case HdrFlagCompressXPRESS, HdrFlagCompressXPRESS2:
		return xpress.Decompress(data, uncompressedSize)
	case HdrFlagCompressLZX:
		return lzx.Decompress(data, uncompressedSize)
	case HdrFlagCompressLZMS:
		return lzms.Decompress(data, uncompressedSize)
	default:
		return nil, fmt.Errorf("wim: decompress: unrecognized compression type %#x", ctype)
	}
}

// genBenchData produces reasonably compressible pseudo-random data (a mix of
// repeated runs and random bytes, similar in spirit to real filesystem
// content) of exactly n bytes, seeded deterministically for reproducibility.
func genBenchData(n int) []byte {
	r := rand.New(rand.NewSource(1))
	out := make([]byte, n)
	pos := 0
	words := []string{"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog", "wim", "resource", "chunk", "compress"}
	for pos < n {
		if r.Intn(4) == 0 {
			// Random run: low compressibility.
			end := pos + 16 + r.Intn(64)
			if end > n {
				end = n
			}
			for ; pos < end; pos++ {
				out[pos] = byte(r.Intn(256))
			}
		} else {
			// Repeated word: high compressibility.
			w := words[r.Intn(len(words))]
			for i := 0; i < 8 && pos < n; i++ {
				for j := 0; j < len(w) && pos < n; j++ {
					out[pos] = w[j]
					pos++
				}
			}
		}
	}
	return out
}

// benchDecodeResourceData benchmarks both the old (double-copy) and new
// (DecompressInto) DecodeResourceData implementations for one codec, given a
// chunk size and total resource size, and reports allocs/op, bytes/op and
// ns/op for direct before/after comparison.
func benchDecodeResourceData(b *testing.B, ctype CompressionType, chunkSize uint32, totalSize int) {
	data := genBenchData(totalSize)
	payload, _, err := EncodeResourceData(data, ctype, chunkSize)
	if err != nil {
		b.Fatalf("EncodeResourceData: %v", err)
	}
	uncompressedSize := uint64(len(data))

	b.Run("Old", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := decodeResourceDataOld(payload, ctype, chunkSize, uncompressedSize)
			if err != nil {
				b.Fatalf("decodeResourceDataOld: %v", err)
			}
			if len(out) != len(data) {
				b.Fatalf("length mismatch: got %d want %d", len(out), len(data))
			}
		}
	})

	b.Run("New", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := DecodeResourceData(payload, ctype, chunkSize, uncompressedSize)
			if err != nil {
				b.Fatalf("DecodeResourceData: %v", err)
			}
			if len(out) != len(data) {
				b.Fatalf("length mismatch: got %d want %d", len(out), len(data))
			}
		}
	})
}

func BenchmarkDecodeResourceData_XPRESS(b *testing.B) {
	benchDecodeResourceData(b, HdrFlagCompressXPRESS, 32768, 32768*64)
}

func BenchmarkDecodeResourceData_LZX(b *testing.B) {
	benchDecodeResourceData(b, HdrFlagCompressLZX, 32768, 32768*64)
}

func BenchmarkDecodeResourceData_LZMS(b *testing.B) {
	benchDecodeResourceData(b, HdrFlagCompressLZMS, 1<<20, (1<<20)*16)
}
