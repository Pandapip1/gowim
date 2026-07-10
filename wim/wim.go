// Package wim implements parsing, serialization, and the high-level structure
// of the WIM (Windows Imaging Format) container.
//
// It is a Go reimplementation of the on-disk format handling in wimlib
// (https://wimlib.net, https://github.com/ebiggers/wimlib), covering the parts
// of the format needed to read and write the container skeleton: the header,
// the blob (lookup) table, resource descriptors, image metadata resources
// (security data + directory-entry tree), and the XML data.
//
// Scope: this package handles the *structure* of a WIM, but it is no longer a
// leaf module -- it now depends on the sibling gowim/xpress, gowim/lzx, and
// gowim/lzms modules to actually read and write compressed resources. That is
// a deliberate architectural change (see go.mod's require/replace entries,
// matching the pattern already used by e.g. driver/go.mod for its sibling
// dependencies), not an accident: compression support genuinely needs those
// codecs, and pulling them in here is the correct place to wire them up.
//
// Compressed, *non-solid* resources (ResourceHeader.Flags with
// ResFlagCompressed set but not ResFlagSolid: XPRESS, LZX, and LZMS alike)
// are fully supported for both reading and writing:
//
//   - DecodeResourceData parses the chunk-table framing a compressed
//     resource uses on disk (see its doc comment for the exact layout) and
//     dispatches each chunk to xpress.Decompress, lzx.Decompress, or
//     lzms.Decompress as appropriate; Reader.resourceData calls it
//     transparently, so Reader.XMLData, Reader.BlobTable,
//     Reader.ImageMetadata, and the path-based Reader.ReadFile all just work
//     on compressed WIMs and ESD-adjacent files without their callers
//     needing to know or care that decompression happened.
//   - EncodeResourceData is the write-side counterpart: given a resource's
//     full uncompressed bytes, a compression type, and a chunk size, it
//     produces the correctly chunk-table-framed on-disk payload (compressing
//     each chunk with xpress.Compress/lzx.Compress/lzms.Compress and falling
//     back to storing a chunk, or the whole resource, raw when compression
//     does not shrink it -- mirroring wimlib's own writer behavior; see its
//     doc comment for the exact rules) plus the ResFlag* bits that belong on
//     that resource's ResourceHeader.
//
// Solid resources (ResourceHeader.IsSolid, ResFlagSolid) remain explicitly
// out of scope: they pack multiple blobs into one shared compressed stream
// (see BlobTable.SolidResourceRun), which is a separate, larger piece of
// container-level complexity than per-resource chunk framing. Reading or
// writing a solid resource as data returns ErrCompressedResource; the parsed
// container structure around them (BlobTable.SolidResources) is still fully
// supported, just not unpacking the packed stream itself.
//
// Assembling an entire multi-resource, multi-image WIM file (header + blob
// table + XML data + one metadata resource per image, with correct offsets
// throughout) is directly supported: WriteTo (and its in-memory convenience
// wrapper, Assemble) takes one *ImageMetadata per image, a *BlobTable whose
// entries already have correct Hash/RefCount/PartNumber (only Resource is
// filled in by the writer), a *XMLData, and a BlobSource for the raw content
// bytes of every blob the table references; it lays everything out, calls
// EncodeResourceData once per blob and once per image's metadata resource,
// and patches the header in once every resource's final offset is known --
// see write_test.go's buildMinimalWIM for the original, single-image,
// test-only version of this same technique, which WriteTo generalizes into
// real package API. Solid resources, an integrity table, and multi-part
// (split) WIMs remain out of scope for the writer, same as for the rest of
// this package: WriteTo never emits ResFlagSolid, always leaves
// Header.IntegrityTable zero (absent, which is valid), and always writes
// PartNumber/TotalParts as 1/1. Building each image's directory-entry
// tree/security data, computing blob reference counts, and producing the
// XML document's content remain the caller's job, exactly as they already
// are for driver.Install.
//
// Filesystem capture/apply remain out of scope too.
//
// It also provides path-based operations over a DirEntry tree, generalizing
// the path-walking/case-insensitive-child-lookup logic that callers like
// driver/install.go would otherwise hand-roll themselves: DirEntry.Lookup and
// DirEntry.Child resolve '/'- or '\'-separated paths case-insensitively (see
// ErrNotFound); DirEntry.Add and DirEntry.Remove create or delete a path's
// entry (and, for Remove, its subtree), creating intervening directories as
// Add needs them; DirEntry.Rename moves an entry; DirEntry.ReadDir lists a
// directory's children; and MatchName does DOS-style '*'/'?' glob matching
// over a single name component. As with the rest of this package, these stay
// within the structural layer: Reader.ReadFile resolves a path down to bytes
// but still returns ErrCompressedResource unmodified for a compressed blob
// rather than decompressing it, Add takes a Hash rather than raw content
// bytes (getting those bytes into a BlobTable is the caller's job, as it
// already is for driver.Install), and Remove does not adjust any BlobTable's
// reference counts for streams a removed subtree stops referencing.
package wim

import (
	"encoding/binary"
	"fmt"
)

// SHA1Size is the length in bytes of a SHA-1 message digest, used throughout
// the format to identify blob contents.
const SHA1Size = 20

// Hash is a SHA-1 message digest of a blob's uncompressed contents. The
// all-zero Hash is the conventional value for zero-length data.
type Hash [SHA1Size]byte

// IsZero reports whether h is the all-zero hash (used for empty blobs).
func (h Hash) IsZero() bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

// String returns the lowercase hex encoding of the hash.
func (h Hash) String() string {
	const hexdigits = "0123456789abcdef"
	var buf [SHA1Size * 2]byte
	for i, b := range h {
		buf[i*2] = hexdigits[b>>4]
		buf[i*2+1] = hexdigits[b&0x0f]
	}
	return string(buf[:])
}

// GUIDSize is the length in bytes of a WIM GUID.
const GUIDSize = 16

// GUID is the globally unique identifier stored in the WIM header. It is just
// 16 bytes of (normally random) data; the format assigns no internal structure.
type GUID [GUIDSize]byte

// byte order used everywhere in the WIM on-disk format.
var le = binary.LittleEndian

// wrapErr is a small helper for adding context to parse errors without pulling
// in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wim: %s: %w", what, err)
}
