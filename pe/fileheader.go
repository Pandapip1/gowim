package pe

import "fmt"

// FileHeaderSize is the on-disk size of IMAGE_FILE_HEADER (the COFF file
// header), which immediately follows the PE signature.
const FileHeaderSize = 20

// Well-known IMAGE_FILE_HEADER.Machine values. Driver binaries are normally
// one of MachineI386, MachineAMD64, or MachineARM64.
const (
	MachineUnknown uint16 = 0x0000
	MachineI386    uint16 = 0x014c
	MachineARM     uint16 = 0x01c0
	MachineARMNT   uint16 = 0x01c4 // ARM Thumb-2
	MachineARM64   uint16 = 0xAA64
	MachineIA64    uint16 = 0x0200
	MachineAMD64   uint16 = 0x8664
)

// IMAGE_FILE_HEADER.Characteristics flag bits.
const (
	FileRelocsStripped    uint16 = 0x0001
	FileExecutableImage   uint16 = 0x0002
	FileLargeAddressAware uint16 = 0x0020
	FileDLL               uint16 = 0x2000
)

// FileHeader is the in-memory form of IMAGE_FILE_HEADER (the COFF file
// header).
type FileHeader struct {
	// Machine identifies the target CPU architecture (one of the Machine*
	// constants).
	Machine uint16
	// NumberOfSections is the number of entries in the section header table.
	// When serialized via (*Image).AppendTo, this is derived from the number
	// of sections in the Image and any value set here is ignored.
	NumberOfSections uint16
	// TimeDateStamp is the low 32 bits of the number of seconds since the
	// Unix epoch, indicating when the file was created by the linker.
	TimeDateStamp uint32
	// PointerToSymbolTable is the file offset of the COFF symbol table, or 0.
	// Deprecated by Microsoft; normally 0 for modern images.
	PointerToSymbolTable uint32
	// NumberOfSymbols is the number of entries in the COFF symbol table.
	// Deprecated by Microsoft; normally 0 for modern images.
	NumberOfSymbols uint32
	// SizeOfOptionalHeader is the size in bytes of the optional header that
	// follows. When serialized via (*Image).AppendTo, this is derived from
	// the encoded size of Image.OptionalHeader and any value set here is
	// ignored.
	SizeOfOptionalHeader uint16
	// Characteristics is a bitwise-OR of the File* flag constants.
	Characteristics uint16
}

// parseFileHeader decodes a FileHeader from the first FileHeaderSize bytes
// of b.
func parseFileHeader(b []byte) (FileHeader, error) {
	if len(b) < FileHeaderSize {
		return FileHeader{}, fmt.Errorf("%w: need %d bytes for file header, have %d", ErrTruncated, FileHeaderSize, len(b))
	}
	return FileHeader{
		Machine:              le.Uint16(b[0:2]),
		NumberOfSections:     le.Uint16(b[2:4]),
		TimeDateStamp:        le.Uint32(b[4:8]),
		PointerToSymbolTable: le.Uint32(b[8:12]),
		NumberOfSymbols:      le.Uint32(b[12:16]),
		SizeOfOptionalHeader: le.Uint16(b[16:18]),
		Characteristics:      le.Uint16(b[18:20]),
	}, nil
}

// AppendTo serializes h, appending exactly FileHeaderSize bytes to dst.
// Fields are written exactly as stored in h; callers using (*Image).AppendTo
// do not need to keep NumberOfSections or SizeOfOptionalHeader in sync
// themselves, since Image derives and overrides them.
func (h FileHeader) AppendTo(dst []byte) []byte {
	var b [FileHeaderSize]byte
	le.PutUint16(b[0:2], h.Machine)
	le.PutUint16(b[2:4], h.NumberOfSections)
	le.PutUint32(b[4:8], h.TimeDateStamp)
	le.PutUint32(b[8:12], h.PointerToSymbolTable)
	le.PutUint32(b[12:16], h.NumberOfSymbols)
	le.PutUint16(b[16:18], h.SizeOfOptionalHeader)
	le.PutUint16(b[18:20], h.Characteristics)
	return append(dst, b[:]...)
}
