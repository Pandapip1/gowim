package wim

import (
	"bytes"
	"crypto/sha1"
	"testing"
)

func TestWriteToComputeIntegrityTable(t *testing.T) {
	files := twoImageFixture()
	images, bt, src := buildTestImages(t, files)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	// Use a small integrity chunk size so the fixture (a few hundred KB)
	// actually produces multiple chunks worth checking.
	const testChunkSize = 4096

	wimBytes, err := Assemble(images, bt, xml, src, WriteOptions{
		CompressionType:       HdrFlagCompressXPRESS,
		ChunkSize:             32768,
		ComputeIntegrityTable: true,
		IntegrityChunkSize:    testChunkSize,
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	hdr := r.Header()
	if hdr.IntegrityTable.IsZero() {
		t.Fatalf("Header.IntegrityTable is zero, want set")
	}

	numCheckedBytes := hdr.BlobTable.OffsetInWIM + hdr.BlobTable.SizeInWIM - uint64(HeaderSize)
	it, err := r.IntegrityTable(numCheckedBytes)
	if err != nil {
		t.Fatalf("IntegrityTable: %v", err)
	}
	if it.ChunkSize != testChunkSize {
		t.Fatalf("ChunkSize = %d, want %d", it.ChunkSize, testChunkSize)
	}

	// Independently recompute every hash directly over the raw file bytes
	// and confirm they match what WriteTo stored.
	end := hdr.BlobTable.OffsetInWIM + hdr.BlobTable.SizeInWIM
	off := uint64(HeaderSize)
	for i, want := range it.Hashes {
		chunkLen := uint64(it.ChunkSize)
		if off+chunkLen > end {
			chunkLen = end - off
		}
		got := Hash(sha1.Sum(wimBytes[off : off+chunkLen]))
		if got != want {
			t.Fatalf("chunk %d hash mismatch: got %s want %s", i, got, want)
		}
		off += chunkLen
	}
	if off != end {
		t.Fatalf("recomputed range ended at %d, want %d", off, end)
	}

	// The convenience verifier should agree.
	if err := r.VerifyIntegrity(); err != nil {
		t.Fatalf("VerifyIntegrity: %v", err)
	}

	// Corrupting a byte within the checked range must make VerifyIntegrity
	// fail (sanity check that it isn't a no-op).
	corrupted := make([]byte, len(wimBytes))
	copy(corrupted, wimBytes)
	corrupted[HeaderSize] ^= 0xff
	cr, err := NewReader(bytes.NewReader(corrupted), int64(len(corrupted)))
	if err != nil {
		t.Fatalf("NewReader(corrupted): %v", err)
	}
	if err := cr.VerifyIntegrity(); err == nil {
		t.Fatalf("VerifyIntegrity on corrupted file: want error, got nil")
	}

	// Corrupting a byte in the XML data (outside the checked range) must NOT
	// be caught -- this is the confirmed, deliberate convention (see
	// integrity_write.go's doc comment): the integrity table does not cover
	// XML data.
	corrupted2 := make([]byte, len(wimBytes))
	copy(corrupted2, wimBytes)
	corrupted2[hdr.XMLData.OffsetInWIM] ^= 0xff
	cr2, err := NewReader(bytes.NewReader(corrupted2), int64(len(corrupted2)))
	if err != nil {
		t.Fatalf("NewReader(corrupted2): %v", err)
	}
	if err := cr2.VerifyIntegrity(); err != nil {
		t.Fatalf("VerifyIntegrity on XML-corrupted file: want nil (XML is outside checked range), got %v", err)
	}
}

func TestWriteToNoIntegrityTableByDefault(t *testing.T) {
	files := twoImageFixture()
	images, bt, src := buildTestImages(t, files)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	wimBytes, err := Assemble(images, bt, xml, src, WriteOptions{
		CompressionType: HdrFlagCompressXPRESS,
		ChunkSize:       32768,
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if !r.Header().IntegrityTable.IsZero() {
		t.Fatalf("Header.IntegrityTable is set, want zero when ComputeIntegrityTable is false")
	}
	if err := r.VerifyIntegrity(); err != ErrNoIntegrityTable {
		t.Fatalf("VerifyIntegrity on a WIM with no integrity table: got %v, want ErrNoIntegrityTable", err)
	}
}

func TestWriteToComputeIntegrityTableDefaultChunkSize(t *testing.T) {
	files := twoImageFixture()
	images, bt, src := buildTestImages(t, files)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	wimBytes, err := Assemble(images, bt, xml, src, WriteOptions{
		CompressionType:       HdrFlagCompressXPRESS,
		ChunkSize:             32768,
		ComputeIntegrityTable: true,
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	numCheckedBytes := r.Header().BlobTable.OffsetInWIM + r.Header().BlobTable.SizeInWIM - uint64(HeaderSize)
	it, err := r.IntegrityTable(numCheckedBytes)
	if err != nil {
		t.Fatalf("IntegrityTable: %v", err)
	}
	if it.ChunkSize != IntegrityChunkSize {
		t.Fatalf("ChunkSize = %d, want default %d", it.ChunkSize, IntegrityChunkSize)
	}
	if err := r.VerifyIntegrity(); err != nil {
		t.Fatalf("VerifyIntegrity: %v", err)
	}
}
