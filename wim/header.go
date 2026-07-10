package wim

import (
	"errors"
	"fmt"
)

// HeaderSize is the on-disk size of the WIM header in bytes. wimlib requires
// the header to be exactly this size.
const HeaderSize = 208

// WIM version numbers.
const (
	// VersionDefault is the standard WIM version. Blobs are compressed
	// independently (no solid resources).
	VersionDefault uint32 = 0x10d00
	// VersionSolid is the version introduced with Windows 8 WIMGAPI that
	// permits solid resources and LZMS compression.
	VersionSolid uint32 = 0x0e00
)

// Magic values stored in the first 8 bytes of the header, as little-endian
// uint64s.
const (
	// Magic ("MSWIM\0\0\0") identifies a normal WIM file.
	Magic uint64 = 'M' | 'S'<<8 | 'W'<<16 | 'I'<<24 | 'M'<<32
	// PipableMagic ("WLPWM\0\0\0") identifies a wimlib "pipable" WIM, whose
	// real header is located at the end of the file.
	PipableMagic uint64 = 'W' | 'L'<<8 | 'P'<<16 | 'W'<<24 | 'M'<<32
)

// Header flags (the wim_flags field). These mirror wimlib's WIM_HDR_FLAG_*.
const (
	HdrFlagReserved         uint32 = 0x00000001
	HdrFlagCompression      uint32 = 0x00000002 // WIM may contain compressed resources
	HdrFlagReadonly         uint32 = 0x00000004
	HdrFlagSpanned          uint32 = 0x00000008 // part of a split WIM
	HdrFlagResourceOnly     uint32 = 0x00000010
	HdrFlagMetadataOnly     uint32 = 0x00000020
	HdrFlagWriteInProgress  uint32 = 0x00000040
	HdrFlagRPFix            uint32 = 0x00000080 // default reparse-point fixup behavior
	HdrFlagCompressReserved uint32 = 0x00010000
	HdrFlagCompressXPRESS   uint32 = 0x00020000
	HdrFlagCompressLZX      uint32 = 0x00040000
	HdrFlagCompressLZMS     uint32 = 0x00080000
	HdrFlagCompressXPRESS2  uint32 = 0x00200000
)

// MaxImages is the maximum number of images wimlib will process, chosen to
// bound memory use on malformed inputs.
const MaxImages = 65535

// Sentinel errors returned by ParseHeader. Callers can test with errors.Is.
var (
	ErrNotWIM          = errors.New("wim: invalid magic characters in header")
	ErrPipableFromByte = errors.New("wim: pipable WIM must be read from a seekable file, not a byte slice")
	ErrInvalidHeader   = errors.New("wim: invalid header")
	ErrUnknownVersion  = errors.New("wim: unknown WIM version")
	ErrPartNumber      = errors.New("wim: invalid part number")
	ErrImageCount      = errors.New("wim: invalid image count")
)

// Header is the in-memory form of a WIM header (struct wim_header).
type Header struct {
	// Magic is Magic or PipableMagic.
	Magic uint64
	// Version is VersionDefault or VersionSolid.
	Version uint32
	// Flags is a bitwise-OR of the HdrFlag* constants.
	Flags uint32
	// ChunkSize is the uncompressed chunk size for non-solid compressed
	// resources, or 0 if the WIM is uncompressed.
	ChunkSize uint32
	// GUID uniquely identifies the WIM file.
	GUID GUID
	// PartNumber is this part's 1-based index within a split WIM (1 if not
	// split).
	PartNumber uint16
	// TotalParts is the number of parts of a split WIM (1 if not split).
	TotalParts uint16
	// ImageCount is the number of images in the WIM.
	ImageCount uint32
	// BlobTable locates the WIM's blob (lookup) table.
	BlobTable ResourceHeader
	// XMLData locates the WIM's XML data.
	XMLData ResourceHeader
	// BootMetadata locates the metadata resource of the bootable image, or is
	// zero if no image is bootable.
	BootMetadata ResourceHeader
	// BootIndex is the 1-based index of the bootable image, or 0 if none.
	BootIndex uint32
	// IntegrityTable locates the WIM's integrity table, or is zero if absent.
	IntegrityTable ResourceHeader
}

// Pipable reports whether this is a wimlib pipable WIM.
func (h Header) Pipable() bool { return h.Magic == PipableMagic }

// header field offsets, from struct wim_header_disk.
const (
	offMagic          = 0x00 // le64
	offHdrSize        = 0x08 // le32
	offVersion        = 0x0c // le32
	offFlags          = 0x10 // le32
	offChunkSize      = 0x14 // le32
	offGUID           = 0x18 // 16 bytes
	offPartNumber     = 0x28 // le16
	offTotalParts     = 0x2a // le16
	offImageCount     = 0x2c // le32
	offBlobTable      = 0x30 // reshdr (24)
	offXMLData        = 0x48 // reshdr (24)
	offBootMetadata   = 0x60 // reshdr (24)
	offBootIndex      = 0x78 // le32
	offIntegrityTable = 0x7c // reshdr (24); note: only 4-byte aligned
	// 0x94..0xd0: 60 unused bytes
)

// ParseHeader decodes a WIM header from the first HeaderSize bytes of b and
// validates it the way wimlib does.
//
// fileSize, if greater than zero, is the total size of the underlying WIM file
// and is used for a sanity check: the (uncompressed) blob table, XML data, and
// integrity table must not claim to be larger than the whole file. Pass 0 to
// skip that check.
//
// Note: this does not follow the pipable-WIM redirection (a PipableMagic file
// stores its authoritative header at the end). ParseHeader reports the magic
// it finds; a caller reading a seekable pipable WIM should re-read the trailing
// HeaderSize bytes and parse those. See ErrPipableFromByte usage in Reader.
func ParseHeader(b []byte, fileSize uint64) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("%w: need %d bytes, have %d", ErrInvalidHeader, HeaderSize, len(b))
	}
	var h Header
	h.Magic = le.Uint64(b[offMagic:])
	if h.Magic != Magic && h.Magic != PipableMagic {
		return Header{}, ErrNotWIM
	}

	if hs := le.Uint32(b[offHdrSize:]); hs != HeaderSize {
		return Header{}, fmt.Errorf("%w: header size is %d, expected %d", ErrInvalidHeader, hs, HeaderSize)
	}

	h.Version = le.Uint32(b[offVersion:])
	if h.Version != VersionDefault && h.Version != VersionSolid {
		return Header{}, fmt.Errorf("%w: %#x", ErrUnknownVersion, h.Version)
	}

	h.Flags = le.Uint32(b[offFlags:])
	h.ChunkSize = le.Uint32(b[offChunkSize:])
	copy(h.GUID[:], b[offGUID:offGUID+GUIDSize])
	h.PartNumber = le.Uint16(b[offPartNumber:])
	h.TotalParts = le.Uint16(b[offTotalParts:])
	if h.TotalParts == 0 || h.PartNumber == 0 || h.PartNumber > h.TotalParts {
		return Header{}, fmt.Errorf("%w: %d of %d", ErrPartNumber, h.PartNumber, h.TotalParts)
	}

	h.ImageCount = le.Uint32(b[offImageCount:])
	if h.ImageCount > MaxImages {
		return Header{}, fmt.Errorf("%w: %d", ErrImageCount, h.ImageCount)
	}

	var err error
	if h.BlobTable, err = parseResourceHeader(b[offBlobTable:]); err != nil {
		return Header{}, wrapErr("blob table reshdr", err)
	}
	if h.XMLData, err = parseResourceHeader(b[offXMLData:]); err != nil {
		return Header{}, wrapErr("xml data reshdr", err)
	}
	if h.BootMetadata, err = parseResourceHeader(b[offBootMetadata:]); err != nil {
		return Header{}, wrapErr("boot metadata reshdr", err)
	}
	h.BootIndex = le.Uint32(b[offBootIndex:])
	if h.IntegrityTable, err = parseResourceHeader(b[offIntegrityTable:]); err != nil {
		return Header{}, wrapErr("integrity table reshdr", err)
	}

	if fileSize > 0 {
		if h.BlobTable.UncompressedSize > fileSize ||
			h.XMLData.UncompressedSize > fileSize ||
			h.IntegrityTable.UncompressedSize > fileSize {
			return Header{}, fmt.Errorf("%w: a table claims to be larger than the file", ErrInvalidHeader)
		}
	}
	return h, nil
}

// AppendTo serializes the header, appending exactly HeaderSize bytes to dst.
func (h Header) AppendTo(dst []byte) ([]byte, error) {
	var b [HeaderSize]byte
	le.PutUint64(b[offMagic:], h.Magic)
	le.PutUint32(b[offHdrSize:], HeaderSize)
	le.PutUint32(b[offVersion:], h.Version)
	le.PutUint32(b[offFlags:], h.Flags)
	le.PutUint32(b[offChunkSize:], h.ChunkSize)
	copy(b[offGUID:], h.GUID[:])
	le.PutUint16(b[offPartNumber:], h.PartNumber)
	le.PutUint16(b[offTotalParts:], h.TotalParts)
	le.PutUint32(b[offImageCount:], h.ImageCount)

	for _, f := range []struct {
		off int
		r   ResourceHeader
	}{
		{offBlobTable, h.BlobTable},
		{offXMLData, h.XMLData},
		{offBootMetadata, h.BootMetadata},
		{offIntegrityTable, h.IntegrityTable},
	} {
		sub, err := f.r.appendTo(nil)
		if err != nil {
			return dst, err
		}
		copy(b[f.off:], sub)
	}
	le.PutUint32(b[offBootIndex:], h.BootIndex)

	return append(dst, b[:]...), nil
}

// NewHeader returns a Header initialized with sensible defaults for a fresh,
// single-part, uncompressed WIM at VersionDefault.
func NewHeader() Header {
	return Header{
		Magic:      Magic,
		Version:    VersionDefault,
		PartNumber: 1,
		TotalParts: 1,
	}
}
