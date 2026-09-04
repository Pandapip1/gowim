package wim

import (
	"fmt"
	"sync"
)

// BlobDescriptorSize is the size in bytes of one blob-table entry on disk
// (struct blob_descriptor_disk): a 24-byte resource header, a 2-byte part
// number, a 4-byte reference count, and a 20-byte SHA-1 hash.
const BlobDescriptorSize = ResourceHeaderSize + 2 + 4 + SHA1Size // 50

// BlobDescriptor is one entry of the blob table (struct blob_descriptor_disk).
// Each entry describes either a standalone resource or, when Resource.IsSolid
// is set, participates in describing a solid resource (see BlobTable docs).
type BlobDescriptor struct {
	// Resource is the resource header for this blob.
	Resource ResourceHeader
	// PartNumber is the 1-based split-WIM part that holds this blob.
	PartNumber uint16
	// RefCount is the reference count of the blob across all images.
	RefCount uint32
	// Hash is the SHA-1 digest of the blob's uncompressed data (all zero for
	// a zero-length blob).
	Hash Hash
}

// parseBlobDescriptor decodes one entry from the first BlobDescriptorSize bytes
// of b.
//
// Layout, from struct blob_descriptor_disk:
//
//	+0x00  reshdr        24 bytes
//	+0x18  part_number   le16
//	+0x1a  refcnt        le32
//	+0x1e  hash          20 bytes
func parseBlobDescriptor(b []byte) (BlobDescriptor, error) {
	if len(b) < BlobDescriptorSize {
		return BlobDescriptor{}, fmt.Errorf("blob descriptor: need %d bytes, have %d", BlobDescriptorSize, len(b))
	}
	var d BlobDescriptor
	r, err := parseResourceHeader(b[0:ResourceHeaderSize])
	if err != nil {
		return BlobDescriptor{}, err
	}
	d.Resource = r
	d.PartNumber = le.Uint16(b[24:26])
	d.RefCount = le.Uint32(b[26:30])
	copy(d.Hash[:], b[30:50])
	return d, nil
}

func (d BlobDescriptor) appendTo(dst []byte) ([]byte, error) {
	var err error
	if dst, err = d.Resource.appendTo(dst); err != nil {
		return dst, err
	}
	var tail [2 + 4 + SHA1Size]byte
	le.PutUint16(tail[0:2], d.PartNumber)
	le.PutUint32(tail[2:6], d.RefCount)
	copy(tail[6:], d.Hash[:])
	return append(dst, tail[:]...), nil
}

// BlobTable is the parsed WIM blob (lookup) table: a flat list of blob
// descriptors, one per BlobDescriptorSize-byte on-disk entry.
//
// Solid resources are represented on disk as a run of consecutive entries all
// carrying ResFlagSolid. Within such a run, the entry whose
// Resource.UncompressedSize equals SolidResourceMagic is the "resource spec"
// (its resource header locates the packed region in the file); the other
// entries in the run are the blobs packed into that resource. This type
// preserves the entries verbatim and exposes SolidResources for interpreting
// the runs; it does not unpack them (unpacking requires the LZMS codec, which
// is out of scope).
type BlobTable struct {
	Entries []BlobDescriptor

	// indexMu guards hashIndex/indexedN, which cache a Hash -> index-into-
	// Entries lookup table for ByHash. Entries is an exported field that many
	// callers mutate directly (always by appending -- see ByHash's doc
	// comment and this package's/callers' tests), so there is no setter to
	// hook; instead ensureIndexLocked lazily notices growth and extends the
	// cache incrementally. A mutex (rather than e.g. sync.Once) is needed
	// because ByHash is called concurrently on a single, already-fully-
	// parsed BlobTable during export (see blob_pipeline.go's
	// encodeBlobsPipeline, whose workers all call BlobSource.Blob, which
	// calls ByHash, concurrently on the same source table).
	indexMu   sync.Mutex
	hashIndex map[Hash]int
	indexedN  int // number of leading Entries already reflected in hashIndex
}

// ParseBlobTable decodes a blob table from its (already-decompressed) resource
// bytes. The buffer length must be a whole multiple of BlobDescriptorSize.
func ParseBlobTable(b []byte) (*BlobTable, error) {
	if len(b)%BlobDescriptorSize != 0 {
		return nil, fmt.Errorf("wim: blob table size %d is not a multiple of %d", len(b), BlobDescriptorSize)
	}
	n := len(b) / BlobDescriptorSize
	t := &BlobTable{Entries: make([]BlobDescriptor, 0, n)}
	for i := 0; i < n; i++ {
		d, err := parseBlobDescriptor(b[i*BlobDescriptorSize:])
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("blob table entry %d", i), err)
		}
		t.Entries = append(t.Entries, d)
	}
	return t, nil
}

// AppendTo serializes the blob table, appending len(Entries)*BlobDescriptorSize
// bytes to dst.
func (t *BlobTable) AppendTo(dst []byte) ([]byte, error) {
	var err error
	for i := range t.Entries {
		if dst, err = t.Entries[i].appendTo(dst); err != nil {
			return dst, wrapErr(fmt.Sprintf("blob table entry %d", i), err)
		}
	}
	return dst, nil
}

// EncodedLen returns the number of bytes AppendTo will write.
func (t *BlobTable) EncodedLen() int { return len(t.Entries) * BlobDescriptorSize }

// ByHash looks up the blob-table entry whose Hash matches h. It returns
// (BlobDescriptor{}, false) if no entry matches. If multiple entries share a
// hash (which the format permits but wimlib avoids by deduplicating on
// write), the first matching entry in table order is returned.
//
// This is an O(1) lookup backed by a lazily-built/incrementally-extended
// hash index (see ensureIndexLocked); it used to be an O(n) linear scan,
// which measurably dominated real-image export/read times (see
// byHashLinearScan, retained only for the A/B benchmark in
// blobtable_bench_test.go).
func (t *BlobTable) ByHash(h Hash) (BlobDescriptor, bool) {
	t.indexMu.Lock()
	t.ensureIndexLocked()
	idx, ok := t.hashIndex[h]
	t.indexMu.Unlock()
	if !ok {
		return BlobDescriptor{}, false
	}
	return t.Entries[idx], true
}

// ensureIndexLocked brings t.hashIndex up to date with the current
// t.Entries, assuming t.indexMu is already held.
//
// Every known mutator of Entries in this codebase (ParseBlobTable,
// RebuildBlobTable, WriteTo, component.Install, and this package's own
// tests) only ever appends -- it never reorders, truncates, or rewrites an
// existing entry's Hash in place. So the common case is a pure length
// increase, handled here by indexing just the new tail (each new entry only
// added to the map if its hash isn't already present, which preserves
// ByHash's "lowest index wins" contract whether the index was built in one
// pass or many). If Entries ever *shrinks* (not something any caller does
// today, but not contractually forbidden either), the cached index is
// discarded and rebuilt from scratch to avoid keeping stale index values
// past the end of the current slice.
func (t *BlobTable) ensureIndexLocked() {
	n := len(t.Entries)
	if t.hashIndex == nil || t.indexedN > n {
		t.hashIndex = make(map[Hash]int, n)
		t.indexedN = 0
	}
	for i := t.indexedN; i < n; i++ {
		h := t.Entries[i].Hash
		if _, exists := t.hashIndex[h]; !exists {
			t.hashIndex[h] = i
		}
	}
	t.indexedN = n
}

// byHashLinearScan is the pre-index O(n) implementation of ByHash, kept only
// so blobtable_bench_test.go can measure the improvement side-by-side; it is
// not used by any non-test code.
func (t *BlobTable) byHashLinearScan(h Hash) (BlobDescriptor, bool) {
	for _, e := range t.Entries {
		if e.Hash == h {
			return e, true
		}
	}
	return BlobDescriptor{}, false
}

// MetadataResources returns the resource headers of all entries flagged as
// metadata (i.e. WIM image metadata resources), in table order.
func (t *BlobTable) MetadataResources() []ResourceHeader {
	var out []ResourceHeader
	for _, e := range t.Entries {
		if e.Resource.IsMetadata() {
			out = append(out, e.Resource)
		}
	}
	return out
}

// SolidResourceRun groups a maximal run of consecutive solid blob-table
// entries. On disk, such a run interleaves two kinds of entries, both carrying
// ResFlagSolid:
//
//   - Resource specs: entries whose Resource.UncompressedSize equals
//     SolidResourceMagic. Each locates one packed (LZMS) region in the file.
//     There may be more than one per run.
//   - Blob entries: the blobs packed into the run's resources. For a blob
//     entry, Resource.OffsetInWIM is an offset into the concatenation of the
//     run's resources' *uncompressed* streams, and Resource.SizeInWIM is the
//     blob's uncompressed length.
//
// Resolving which spec a given blob belongs to requires each spec's true
// uncompressed size, which is stored in the resource's own on-disk header (an
// alt_chunk_table_header) and therefore needs file access plus LZMS handling —
// both out of scope for this package. SolidResourceRun therefore preserves the
// two lists separately without resolving the mapping.
type SolidResourceRun struct {
	// Specs are the resource-spec entries in the run, in table order.
	Specs []ResourceHeader
	// Blobs are the blob entries packed into the run, in table order.
	Blobs []BlobDescriptor
}

// SolidResources scans the table for runs of solid entries and returns one
// SolidResourceRun per run, mirroring wimlib's grouping in read_blob_table.
//
// solidValid should be true only when the WIM version permits solid resources
// (VersionSolid). At VersionDefault wimlib ignores the SOLID flag entirely, so
// pass false and every entry is treated as standalone (no runs are returned).
func (t *BlobTable) SolidResources(solidValid bool) []SolidResourceRun {
	if !solidValid {
		return nil
	}
	var out []SolidResourceRun
	i := 0
	for i < len(t.Entries) {
		if !t.Entries[i].Resource.IsSolid() {
			i++
			continue
		}
		start := i
		for i < len(t.Entries) && t.Entries[i].Resource.IsSolid() {
			i++
		}
		var run SolidResourceRun
		for _, e := range t.Entries[start:i] {
			if e.Resource.UncompressedSize == SolidResourceMagic {
				run.Specs = append(run.Specs, e.Resource)
			} else {
				run.Blobs = append(run.Blobs, e)
			}
		}
		out = append(out, run)
	}
	return out
}
