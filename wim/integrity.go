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
// expected to cover (normally the file size minus HeaderSize minus the sizes of
// the integrity table and XML data); it is used to validate num_entries the way
// wimlib does. Pass 0 to skip that cross-check.
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
