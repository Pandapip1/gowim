package wim

import (
	"testing"
)

// benchWriteChunks feeds n bytes to acc in pieces sized like WriteTo's real
// calls: 64 KiB pieces (a typical io.Copy/blob-payload write size), which is
// smaller than the default 10 MiB integrity chunk size, exercising the same
// "many small writes accumulate into one chunk" path as production.
func benchWriteChunks(b *testing.B, totalSize int, writeChunk func([]byte)) {
	const pieceSize = 64 * 1024
	piece := make([]byte, pieceSize)
	for i := range piece {
		piece[i] = byte(i)
	}
	remaining := totalSize
	for remaining > 0 {
		n := pieceSize
		if n > remaining {
			n = remaining
		}
		writeChunk(piece[:n])
		remaining -= n
	}
}

const benchTotalSize = 64 * 1024 * 1024 // 64 MiB, several 10 MiB chunks

func BenchmarkIntegrityAccumulator(b *testing.B) {
	b.ReportAllocs()
	b.SetBytes(int64(benchTotalSize))
	for i := 0; i < b.N; i++ {
		acc := newIntegrityAccumulator(IntegrityChunkSize)
		benchWriteChunks(b, benchTotalSize, acc.write)
		acc.finish()
	}
}
