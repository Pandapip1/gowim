package pe

import (
	"fmt"
	"strings"
)

// SectionHeaderSize is the on-disk size of one IMAGE_SECTION_HEADER entry.
const SectionHeaderSize = 40

// SectionNameSize is the size in bytes of the Name field of
// IMAGE_SECTION_HEADER. It is not necessarily NUL-terminated: a name that is
// exactly 8 characters long (e.g. ".textbss" is 8, more realistically a
// long name via a string-table offset in object files) occupies the field
// with no terminator. See SectionHeader.NameString.
const SectionNameSize = 8

// IMAGE_SECTION_HEADER.Characteristics flag bits relevant to identifying
// section contents; not exhaustive.
const (
	SectionCodeFlag              uint32 = 0x00000020
	SectionInitializedDataFlag   uint32 = 0x00000040
	SectionUninitializedDataFlag uint32 = 0x00000080
	SectionMemDiscardable        uint32 = 0x02000000
	SectionMemExecute            uint32 = 0x20000000
	SectionMemRead               uint32 = 0x40000000
	SectionMemWrite              uint32 = 0x80000000
)

// SectionHeader is the in-memory form of IMAGE_SECTION_HEADER, describing one
// entry of the section header table.
type SectionHeader struct {
	// Name is the raw 8-byte section name field. Use NameString for the
	// conventional NUL-trimmed form.
	Name [SectionNameSize]byte
	// VirtualSize is the total size of the section when loaded into memory.
	VirtualSize uint32
	// VirtualAddress is the RVA of the section's first byte when loaded.
	VirtualAddress uint32
	// SizeOfRawData is the size of the section's initialized data on disk.
	SizeOfRawData uint32
	// PointerToRawData is the file offset of the section's initialized data,
	// or 0 if the section has no file-backed data (e.g. pure BSS).
	PointerToRawData uint32
	// PointerToRelocations is the file offset of the section's relocation
	// entries (normally 0 for images; relocations are only in object files).
	PointerToRelocations uint32
	// PointerToLinenumbers is deprecated by Microsoft and normally 0.
	PointerToLinenumbers uint32
	// NumberOfRelocations is normally 0 for images.
	NumberOfRelocations uint16
	// NumberOfLinenumbers is deprecated by Microsoft and normally 0.
	NumberOfLinenumbers uint16
	// Characteristics is a bitwise-OR of the Section* flag constants.
	Characteristics uint32
}

// NameString returns Name with any trailing NUL bytes trimmed.
func (h SectionHeader) NameString() string {
	return strings.TrimRight(string(h.Name[:]), "\x00")
}

// parseSectionHeader decodes a SectionHeader from the first SectionHeaderSize
// bytes of b.
func parseSectionHeader(b []byte) (SectionHeader, error) {
	if len(b) < SectionHeaderSize {
		return SectionHeader{}, fmt.Errorf("%w: need %d bytes for section header, have %d", ErrTruncated, SectionHeaderSize, len(b))
	}
	var h SectionHeader
	copy(h.Name[:], b[0:8])
	h.VirtualSize = le.Uint32(b[8:12])
	h.VirtualAddress = le.Uint32(b[12:16])
	h.SizeOfRawData = le.Uint32(b[16:20])
	h.PointerToRawData = le.Uint32(b[20:24])
	h.PointerToRelocations = le.Uint32(b[24:28])
	h.PointerToLinenumbers = le.Uint32(b[28:32])
	h.NumberOfRelocations = le.Uint16(b[32:34])
	h.NumberOfLinenumbers = le.Uint16(b[34:36])
	h.Characteristics = le.Uint32(b[36:40])
	return h, nil
}

// AppendTo serializes h, appending exactly SectionHeaderSize bytes to dst.
func (h SectionHeader) AppendTo(dst []byte) []byte {
	var b [SectionHeaderSize]byte
	copy(b[0:8], h.Name[:])
	le.PutUint32(b[8:12], h.VirtualSize)
	le.PutUint32(b[12:16], h.VirtualAddress)
	le.PutUint32(b[16:20], h.SizeOfRawData)
	le.PutUint32(b[20:24], h.PointerToRawData)
	le.PutUint32(b[24:28], h.PointerToRelocations)
	le.PutUint32(b[28:32], h.PointerToLinenumbers)
	le.PutUint16(b[32:34], h.NumberOfRelocations)
	le.PutUint16(b[34:36], h.NumberOfLinenumbers)
	le.PutUint32(b[36:40], h.Characteristics)
	return append(dst, b[:]...)
}

// Section pairs a SectionHeader with its raw on-disk contents. RawData is an
// opaque byte range: this package does not disassemble or otherwise interpret
// section contents, mirroring how the sibling wim package treats compressed
// resource payloads as opaque byte ranges it doesn't decode.
type Section struct {
	Header SectionHeader
	// RawData is the section's file-backed bytes, i.e. the SizeOfRawData
	// bytes at file offset PointerToRawData. It is empty if the section has
	// no file-backed data.
	RawData []byte
}
