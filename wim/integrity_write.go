package wim

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"hash"
)

// ErrNoIntegrityTable is returned by Reader.VerifyIntegrity when the WIM has
// no integrity table to verify against.
var ErrNoIntegrityTable = errors.New("wim: WIM has no integrity table")

// The byte range and chunking an integrity table covers were confirmed
// empirically (2026-07-10) against two real WIMs with integrity tables --
// a Windows 11 23H2 boot.wim (LZX) and install.esd (LZMS), both showing
// "Integrity info" in `wimlib-imagex info`'s Attributes -- by reading each
// file's real IntegrityTable via this package's own Reader, then
// independently recomputing SHA-1 hashes over several candidate byte-range
// hypotheses and checking which recomputed hashes exactly matched the
// stored ones:
//
//   - boot.wim: 69 stored hashes at ChunkSize 10485760. The hypothesis
//     "[HeaderSize, offset of XML data)" (equivalently, since the blob table
//     and XML data resources are stored back-to-back with no gap, "[HeaderSize,
//     end of blob table)") matched with zero mismatches. Every other
//     candidate tried -- through end of XML data, through start/end of the
//     integrity table itself, through end of file -- mismatched on the final
//     chunk, because those ranges include bytes (the XML data and/or
//     integrity table) that are not actually part of what's hashed.
//   - install.esd: same result, 335 stored hashes, only
//     "[HeaderSize, offset of XML data)" (== end of blob table) matched.
//
// This is corroborated by wimlib's source (src/integrity.c,
// calculate_integrity_table/write_integrity_table, and src/write.c's
// finish_write): wimlib computes the table over
// [WIM_HEADER_DISK_SIZE, new_blob_table_end) where new_blob_table_end is the
// offset just past the *blob table* resource -- explicitly not extended to
// include the XML data, even though the integrity table is physically
// appended to the file *after* the XML data (finish_write writes blob table,
// then XML data, then the integrity table last). In other words: the
// integrity table's coverage range and its position in the file are
// independent; wimlib intentionally leaves the XML data (and the integrity
// table itself) unchecked.
//
// WriteTo (see its ComputeIntegrityTable option) and Reader.VerifyIntegrity
// both follow this exact convention.

// integrityAccumulator incrementally hashes a byte stream into
// chunkSize-sized chunks (the last chunk may be shorter), mirroring wimlib's
// calculate_integrity_table chunking. Bytes are fed to it via write() as they
// are produced by the writer, in file order, so no re-reading of the
// already-written file is needed to compute the table; see the doc comment
// above this type for the exact range this covers within WriteTo.
type integrityAccumulator struct {
	chunkSize uint32
	h         hash.Hash
	pos       uint32 // bytes written into h since the last chunk boundary
	hashes    []Hash
}

func newIntegrityAccumulator(chunkSize uint32) *integrityAccumulator {
	if chunkSize == 0 {
		chunkSize = IntegrityChunkSize
	}
	return &integrityAccumulator{chunkSize: chunkSize, h: sha1.New()}
}

func (a *integrityAccumulator) write(p []byte) {
	for len(p) > 0 {
		need := int(a.chunkSize) - int(a.pos)
		n := len(p)
		if n > need {
			n = need
		}
		a.h.Write(p[:n])
		a.pos += uint32(n)
		p = p[n:]
		if a.pos == a.chunkSize {
			a.hashes = append(a.hashes, Hash(a.h.Sum(nil)))
			a.h.Reset()
			a.pos = 0
		}
	}
}

// finish flushes any partial final chunk and returns the complete hash list.
func (a *integrityAccumulator) finish() []Hash {
	if a.pos > 0 {
		a.hashes = append(a.hashes, Hash(a.h.Sum(nil)))
		a.h.Reset()
		a.pos = 0
	}
	return a.hashes
}

// VerifyIntegrity recomputes SHA-1 hashes over the byte range and chunk size
// described by r's stored integrity table (see the doc comment above
// integrityAccumulator for the confirmed convention: chunks of
// IntegrityTable.ChunkSize bytes starting at HeaderSize, up through the end
// of the blob table resource) and compares them against the hashes actually
// stored in the table.
//
// It returns ErrNoIntegrityTable if r's WIM has no integrity table, or an
// error describing the first mismatching chunk if verification fails.
func (r *Reader) VerifyIntegrity() error {
	if r.hdr.IntegrityTable.IsZero() {
		return ErrNoIntegrityTable
	}
	end := r.hdr.BlobTable.OffsetInWIM + r.hdr.BlobTable.SizeInWIM
	if end < uint64(HeaderSize) {
		return fmt.Errorf("%w: blob table ends before header", ErrInvalidHeader)
	}
	numCheckedBytes := end - uint64(HeaderSize)

	it, err := r.IntegrityTable(numCheckedBytes)
	if err != nil {
		return err
	}

	buf := make([]byte, it.ChunkSize)
	off := uint64(HeaderSize)
	for i, want := range it.Hashes {
		chunkLen := uint64(it.ChunkSize)
		if off+chunkLen > end {
			chunkLen = end - off
		}
		if _, err := r.ra.ReadAt(buf[:chunkLen], int64(off)); err != nil {
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
