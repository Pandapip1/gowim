package wim

import (
	"errors"
	"fmt"
	"io"
)

// ErrCompressedResource is returned when a caller asks for the contents of a
// compressed resource. This package handles the WIM container structure but not
// the LZX/XPRESS/LZMS codecs, so only uncompressed resources can be read back
// as data.
var ErrCompressedResource = errors.New("wim: resource is compressed; this package does not implement decompression")

// Reader provides read access to the structural components of a WIM file
// backed by an io.ReaderAt.
type Reader struct {
	ra   io.ReaderAt
	size int64
	hdr  Header
}

// NewReader reads and validates the WIM header from ra and returns a Reader.
// size is the total size of the WIM in bytes (used for header sanity checks and
// integrity-table validation); pass the file size, or 0 if unknown.
//
// For a pipable WIM (PipableMagic), the authoritative header is stored at the
// end of the file; NewReader detects this and re-reads the trailing header when
// size is known.
func NewReader(ra io.ReaderAt, size int64) (*Reader, error) {
	buf := make([]byte, HeaderSize)
	if _, err := ra.ReadAt(buf, 0); err != nil {
		return nil, wrapErr("read header", err)
	}
	fileSize := uint64(0)
	if size > 0 {
		fileSize = uint64(size)
	}
	hdr, err := ParseHeader(buf, fileSize)
	if err != nil {
		return nil, err
	}
	if hdr.Pipable() {
		if size <= 0 {
			return nil, ErrPipableFromByte
		}
		tail := make([]byte, HeaderSize)
		if _, err := ra.ReadAt(tail, size-HeaderSize); err != nil {
			return nil, wrapErr("read pipable trailing header", err)
		}
		hdr, err = ParseHeader(tail, fileSize)
		if err != nil {
			return nil, err
		}
	}
	return &Reader{ra: ra, size: size, hdr: hdr}, nil
}

// Header returns the parsed WIM header.
func (r *Reader) Header() Header { return r.hdr }

// readResourceRaw reads exactly the bytes of a resource as stored in the file
// (SizeInWIM bytes at OffsetInWIM). It does not decompress.
func (r *Reader) readResourceRaw(rh ResourceHeader) ([]byte, error) {
	if rh.IsZero() {
		return nil, nil
	}
	buf := make([]byte, rh.SizeInWIM)
	if _, err := r.ra.ReadAt(buf, int64(rh.OffsetInWIM)); err != nil {
		return nil, wrapErr("read resource", err)
	}
	return buf, nil
}

// resourceData returns the uncompressed contents of a resource. If the resource
// is compressed it returns ErrCompressedResource, since decompression is out of
// scope. Uncompressed resources are returned as-is.
func (r *Reader) resourceData(rh ResourceHeader) ([]byte, error) {
	if rh.IsZero() {
		return nil, nil
	}
	if rh.IsCompressed() {
		return nil, ErrCompressedResource
	}
	if rh.SizeInWIM != rh.UncompressedSize {
		return nil, fmt.Errorf("wim: uncompressed resource has mismatched sizes (in-wim %d, uncompressed %d)",
			rh.SizeInWIM, rh.UncompressedSize)
	}
	return r.readResourceRaw(rh)
}

// BlobTable reads and parses the WIM's blob (lookup) table.
func (r *Reader) BlobTable() (*BlobTable, error) {
	data, err := r.resourceData(r.hdr.BlobTable)
	if err != nil {
		return nil, wrapErr("blob table", err)
	}
	if data == nil {
		return &BlobTable{}, nil
	}
	return ParseBlobTable(data)
}

// XMLData reads and parses the WIM's XML data.
func (r *Reader) XMLData() (*XMLData, error) {
	data, err := r.resourceData(r.hdr.XMLData)
	if err != nil {
		return nil, wrapErr("xml data", err)
	}
	if data == nil {
		return &XMLData{}, nil
	}
	return ParseXMLData(data)
}

// IntegrityTable reads and parses the WIM's integrity table, or returns nil if
// the WIM has none. numCheckedBytes is passed through to ParseIntegrityTable
// for validation; pass 0 to skip the entry-count cross-check.
func (r *Reader) IntegrityTable(numCheckedBytes uint64) (*IntegrityTable, error) {
	if r.hdr.IntegrityTable.IsZero() {
		return nil, nil
	}
	data, err := r.resourceData(r.hdr.IntegrityTable)
	if err != nil {
		return nil, wrapErr("integrity table", err)
	}
	return ParseIntegrityTable(data, numCheckedBytes)
}

// ImageMetadata reads and parses the metadata resource located by the given
// resource header (which must be uncompressed). Metadata resources are found
// via BlobTable().MetadataResources() or via the header's BootMetadata slot.
func (r *Reader) ImageMetadata(rh ResourceHeader) (*ImageMetadata, error) {
	data, err := r.resourceData(rh)
	if err != nil {
		return nil, wrapErr("image metadata", err)
	}
	if data == nil {
		return nil, fmt.Errorf("wim: empty metadata resource")
	}
	return ParseImageMetadata(data)
}

// ResourceReader returns an io.SectionReader over the raw (possibly compressed)
// bytes of a resource, for callers that want to handle the payload themselves.
func (r *Reader) ResourceReader(rh ResourceHeader) *io.SectionReader {
	return io.NewSectionReader(r.ra, int64(rh.OffsetInWIM), int64(rh.SizeInWIM))
}
