package wim

import (
	"crypto/sha1"
	"fmt"
	"math/rand"
	"os"
	"testing"
)

// verifyIntegrityChunksSerial is a byte-for-byte reproduction of the
// pre-parallel VerifyIntegrity loop: a single reused buffer forces read and
// hash to strictly alternate. Kept only so this file can measure, with a
// real benchmark, how much the parallel worker-pool version actually saves
// relative to the code it replaced.
func verifyIntegrityChunksSerial(ra interface {
	ReadAt(p []byte, off int64) (int, error)
}, it *IntegrityTable, start, end uint64) error {
	buf := make([]byte, it.ChunkSize)
	off := start
	for i, want := range it.Hashes {
		chunkLen := uint64(it.ChunkSize)
		if off+chunkLen > end {
			chunkLen = end - off
		}
		if _, err := ra.ReadAt(buf[:chunkLen], int64(off)); err != nil {
			return wrapErr(fmt.Sprintf("verify integrity: read chunk %d", i), err)
		}
		got := Hash(sha1.Sum(buf[:chunkLen]))
		if got != want {
			return fmt.Errorf("wim: integrity check failed at chunk %d (file offset %d, %d bytes)", i, off, chunkLen)
		}
		off += chunkLen
	}
	return nil
}

// buildLargeIntegrityWIM writes a real WIM file (CompressionNone, so
// building it is fast even at real size) with a computed integrity table
// over sizeBytes of content, to a real temp file on disk -- so the
// benchmark below measures real file I/O, not an in-memory buffer -- and
// returns the open file, its Reader, and the IntegrityTable to verify
// against.
func buildLargeIntegrityWIM(b *testing.B, sizeBytes int, integrityChunkSize uint32) (*os.File, *Reader, *IntegrityTable) {
	b.Helper()

	rnd := rand.New(rand.NewSource(1))
	content := make([]byte, sizeBytes)
	rnd.Read(content)

	// Built inline rather than via buildTestImages (which takes a *testing.T
	// for t.Fatalf/t.Helper): a single big file plus a trivial second image
	// is all this benchmark needs, and it's simpler than adapting that
	// helper to a *testing.B.
	bt := &BlobTable{}
	src := MapBlobSource{}
	hash := Hash(sha1.Sum(content))
	bt.Entries = append(bt.Entries, BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
	src[hash] = content

	root := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone, Streams: []Stream{{}}}
	if _, err := root.Add("big.bin", hash); err != nil {
		b.Fatalf("Add: %v", err)
	}
	images := []*ImageMetadata{{Security: &SecurityData{}, Root: root}}
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE></WIM>`}

	wimBytes, err := Assemble(images, bt, xml, src, WriteOptions{
		CompressionType:       CompressionNone,
		ComputeIntegrityTable: true,
		IntegrityChunkSize:    integrityChunkSize,
		GUID:                  GUID{1},
	})
	if err != nil {
		b.Fatalf("Assemble: %v", err)
	}

	f, err := os.CreateTemp(b.TempDir(), "integrity-bench-*.wim")
	if err != nil {
		b.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(wimBytes); err != nil {
		b.Fatalf("Write: %v", err)
	}

	r, err := NewReader(f, int64(len(wimBytes)))
	if err != nil {
		b.Fatalf("NewReader: %v", err)
	}
	hdr := r.Header()
	if hdr.IntegrityTable.IsZero() {
		b.Fatalf("Header.IntegrityTable is zero, want set")
	}
	numCheckedBytes := hdr.BlobTable.OffsetInWIM + hdr.BlobTable.SizeInWIM - uint64(HeaderSize)
	it, err := r.IntegrityTable(numCheckedBytes)
	if err != nil {
		b.Fatalf("IntegrityTable: %v", err)
	}
	return f, r, it
}

func benchVerifyIntegrity(b *testing.B, sizeBytes int, integrityChunkSize uint32) {
	f, r, it := buildLargeIntegrityWIM(b, sizeBytes, integrityChunkSize)
	defer f.Close()

	hdr := r.Header()
	end := hdr.BlobTable.OffsetInWIM + hdr.BlobTable.SizeInWIM

	b.Run("Serial", func(b *testing.B) {
		b.SetBytes(int64(sizeBytes))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := verifyIntegrityChunksSerial(f, it, uint64(HeaderSize), end); err != nil {
				b.Fatalf("verifyIntegrityChunksSerial: %v", err)
			}
		}
	})

	b.Run("Parallel", func(b *testing.B) {
		b.SetBytes(int64(sizeBytes))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := verifyIntegrityChunksParallel(f, it, uint64(HeaderSize), end); err != nil {
				b.Fatalf("verifyIntegrityChunksParallel: %v", err)
			}
		}
	})
}

// BenchmarkVerifyIntegrity exercises real file I/O (a real temp file on
// disk, not an in-memory buffer) over a few hundred MB with a 4 MiB
// integrity chunk size, comparable in scale to a real WIM's integrity
// table, to measure whether the parallel read+hash actually helps once
// real disk I/O -- not just SHA-1 throughput -- is in the loop.
func BenchmarkVerifyIntegrity(b *testing.B) {
	benchVerifyIntegrity(b, 256<<20, 4<<20)
}
