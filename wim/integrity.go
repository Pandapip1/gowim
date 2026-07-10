package wim

import "fmt"

// IntegrityTableHeaderSize is the size of the fixed integrity-table header
// (size + num_entries + chunk_size).
const IntegrityTableHeaderSize = 12

// IntegrityChunkSize is the default size of each integrity-checked chunk
// (10 MiB), as used by wimlib.
const IntegrityChunkSize uint32 = 10485760

// IntegrityMinChunkSize is the smallest chunk size wimlib will reuse from an
// existing integrity table.
const IntegrityMinChunkSize uint32 = 4096

// IntegrityTable is the optional table of SHA-1 digests over fixed-size chunks
// of the WIM file (starting at byte HeaderSize). It lets a reader verify file
// integrity without decompressing anything.
type IntegrityTable struct {
	// ChunkSize is the number of file bytes covered by each digest.
	ChunkSize uint32
	// Hashes holds one SHA-1 digest per chunk, in file order.
	Hashes []Hash
}

// ParseIntegrityTable decodes an integrity table from its (uncompressed)
// resource bytes.
//
// numCheckedBytes, if nonzero, is the number of file bytes the table is
// expected to cover; it is used to validate num_entries the way wimlib does.
// Pass 0 to skip that cross-check.
//
// The real convention, confirmed empirically (2026-07-10) against two real
// WIMs with integrity tables (a Windows 11 23H2 boot.wim and install.esd, both
// showing "Integrity info" in `wimlib-imagex info`'s Attributes) and
// corroborated by wimlib's source (src/integrity.c's
// calculate_integrity_table/write_integrity_table and src/write.c's
// finish_write): the table covers exactly
// [HeaderSize, offset of the blob table + size of the blob table) -- i.e.
// HeaderSize bytes in, through the end of the blob table resource, and
// *excludes* the XML data and the integrity table itself, even though the
// integrity table is physically written to the file *after* the XML data.
// numCheckedBytes should therefore be computed as
// (Header.BlobTable.OffsetInWIM + Header.BlobTable.SizeInWIM) - HeaderSize,
// not derived from the overall file size. See integrity_write.go's doc
// comment on integrityAccumulator for the full evidence (the specific byte
// ranges tried and their match/mismatch results) and Reader.VerifyIntegrity
// for the read-side counterpart that applies this same convention.
//
// Layout, from struct integrity_table:
//
//	+0x00  size         le32  (total table size, == 12 + num_entries*20)
//	+0x04  num_entries  le32
//	+0x08  chunk_size   le32
//	+0x0c  hashes[num_entries]  20 bytes each
func ParseIntegrityTable(b []byte, numCheckedBytes uint64) (*IntegrityTable, error) {
	if len(b) < IntegrityTableHeaderSize {
		return nil, fmt.Errorf("%w: integrity table truncated", ErrInvalidHeader)
	}
	size := uint64(le.Uint32(b[0:4]))
	numEntries := uint64(le.Uint32(b[4:8]))
	chunkSize := le.Uint32(b[8:12])

	if size != uint64(len(b)) {
		return nil, fmt.Errorf("%w: integrity table size %d != resource size %d", ErrInvalidHeader, size, len(b))
	}
	if size != numEntries*SHA1Size+IntegrityTableHeaderSize {
		return nil, fmt.Errorf("%w: integrity table size inconsistent with num_entries", ErrInvalidHeader)
	}
	if chunkSize == 0 {
		return nil, fmt.Errorf("%w: integrity table chunk_size is zero", ErrInvalidHeader)
	}
	if numCheckedBytes != 0 {
		want := divRoundUp(numCheckedBytes, uint64(chunkSize))
		if numEntries != want {
			return nil, fmt.Errorf("%w: integrity table has %d entries, expected %d", ErrInvalidHeader, numEntries, want)
		}
	}

	t := &IntegrityTable{ChunkSize: chunkSize, Hashes: make([]Hash, numEntries)}
	off := IntegrityTableHeaderSize
	for i := uint64(0); i < numEntries; i++ {
		copy(t.Hashes[i][:], b[off:off+SHA1Size])
		off += SHA1Size
	}
	return t, nil
}

// AppendTo serializes the integrity table, appending its bytes to dst.
func (t *IntegrityTable) AppendTo(dst []byte) []byte {
	size := uint32(IntegrityTableHeaderSize + len(t.Hashes)*SHA1Size)
	var head [IntegrityTableHeaderSize]byte
	le.PutUint32(head[0:4], size)
	le.PutUint32(head[4:8], uint32(len(t.Hashes)))
	le.PutUint32(head[8:12], t.ChunkSize)
	dst = append(dst, head[:]...)
	for i := range t.Hashes {
		dst = append(dst, t.Hashes[i][:]...)
	}
	return dst
}

// EncodedLen returns the number of bytes AppendTo will write.
func (t *IntegrityTable) EncodedLen() int {
	return IntegrityTableHeaderSize + len(t.Hashes)*SHA1Size
}

func divRoundUp(a, b uint64) uint64 { return (a + b - 1) / b }
