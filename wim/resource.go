package wim

import "fmt"

// ResourceHeaderSize is the size in bytes of a resource header on disk
// (struct wim_reshdr_disk).
const ResourceHeaderSize = 24

// Resource header flags (the flags byte of a resource header). These mirror
// wimlib's WIM_RESHDR_FLAG_* values.
const (
	// ResFlagFree has an unknown meaning and is ignored by wimlib.
	ResFlagFree uint8 = 0x01
	// ResFlagMetadata marks the resource as a WIM image metadata resource,
	// or as the blob table or XML data.
	ResFlagMetadata uint8 = 0x02
	// ResFlagCompressed marks a non-solid resource compressed with the WIM's
	// default compression type.
	ResFlagCompressed uint8 = 0x04
	// ResFlagSpanned has an unknown meaning and is ignored by wimlib.
	ResFlagSpanned uint8 = 0x08
	// ResFlagSolid marks a solid resource that may pack multiple blobs
	// compressed together. Only valid at WIM version VersionSolid.
	ResFlagSolid uint8 = 0x10
)

// SolidResourceMagic is the sentinel stored in the uncompressed-size field of
// the blob-table entry that begins a solid resource.
const SolidResourceMagic uint64 = 0x100000000

// maxReshdrSize is the largest value representable in the 7-byte size field.
const maxReshdrSize = (uint64(1) << 56) - 1

// ResourceHeader is the in-memory form of a WIM resource header
// (struct wim_reshdr). A resource is a standalone, possibly compressed region
// of the WIM file.
type ResourceHeader struct {
	// SizeInWIM is the size of the resource as stored in the file. For a
	// compressed resource this is the compressed size including any chunk
	// table overhead. Stored on disk as a 7-byte little-endian integer.
	SizeInWIM uint64
	// Flags is a bitwise-OR of the ResFlag* constants.
	Flags uint8
	// OffsetInWIM is the byte offset of the resource from the start of the
	// WIM file.
	OffsetInWIM uint64
	// UncompressedSize is the size of the resource's data after
	// decompression. For solid-resource blob-table entries this field
	// carries SolidResourceMagic rather than a real size.
	UncompressedSize uint64
}

// IsCompressed reports whether the resource is stored compressed (either a
// normal compressed resource or a solid resource).
func (r ResourceHeader) IsCompressed() bool {
	return r.Flags&(ResFlagCompressed|ResFlagSolid) != 0
}

// IsMetadata reports whether the resource is a metadata resource.
func (r ResourceHeader) IsMetadata() bool { return r.Flags&ResFlagMetadata != 0 }

// IsSolid reports whether the resource is a solid resource.
func (r ResourceHeader) IsSolid() bool { return r.Flags&ResFlagSolid != 0 }

// IsZero reports whether the header is entirely zero, which the format uses to
// mean "absent" for the optional header slots (boot metadata, integrity table).
func (r ResourceHeader) IsZero() bool {
	return r.SizeInWIM == 0 && r.Flags == 0 && r.OffsetInWIM == 0 && r.UncompressedSize == 0
}

// parseResourceHeader decodes a resource header from exactly the first
// ResourceHeaderSize bytes of b.
//
// Layout (little-endian), from struct wim_reshdr_disk:
//
//	+0x00  size_in_wim        7 bytes
//	+0x07  flags              1 byte
//	+0x08  offset_in_wim      8 bytes
//	+0x10  uncompressed_size  8 bytes
func parseResourceHeader(b []byte) (ResourceHeader, error) {
	if len(b) < ResourceHeaderSize {
		return ResourceHeader{}, fmt.Errorf("resource header: need %d bytes, have %d", ResourceHeaderSize, len(b))
	}
	var r ResourceHeader
	r.SizeInWIM = uint64(b[0]) |
		uint64(b[1])<<8 |
		uint64(b[2])<<16 |
		uint64(b[3])<<24 |
		uint64(b[4])<<32 |
		uint64(b[5])<<40 |
		uint64(b[6])<<48
	r.Flags = b[7]
	r.OffsetInWIM = le.Uint64(b[8:16])
	r.UncompressedSize = le.Uint64(b[16:24])
	return r, nil
}

// appendTo serializes the resource header, appending ResourceHeaderSize bytes
// to dst. It returns an error if SizeInWIM does not fit in the 7-byte field.
func (r ResourceHeader) appendTo(dst []byte) ([]byte, error) {
	if r.SizeInWIM > maxReshdrSize {
		return dst, fmt.Errorf("resource header: size_in_wim %d exceeds 56-bit maximum", r.SizeInWIM)
	}
	var b [ResourceHeaderSize]byte
	b[0] = byte(r.SizeInWIM)
	b[1] = byte(r.SizeInWIM >> 8)
	b[2] = byte(r.SizeInWIM >> 16)
	b[3] = byte(r.SizeInWIM >> 24)
	b[4] = byte(r.SizeInWIM >> 32)
	b[5] = byte(r.SizeInWIM >> 40)
	b[6] = byte(r.SizeInWIM >> 48)
	b[7] = r.Flags
	le.PutUint64(b[8:16], r.OffsetInWIM)
	le.PutUint64(b[16:24], r.UncompressedSize)
	return append(dst, b[:]...), nil
}
