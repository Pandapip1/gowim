package pe

import (
	"errors"
	"fmt"
)

// Optional header magic values, distinguishing PE32 (32-bit) from PE32+
// (64-bit) images. Both are modeled by OptionalHeader; the Magic field
// selects which on-disk layout is used.
const (
	OptionalHeaderMagicPE32     uint16 = 0x10b
	OptionalHeaderMagicPE32Plus uint16 = 0x20b
)

// Fixed-size portion of the optional header preceding the DataDirectory
// array: 96 bytes for PE32 (IMAGE_OPTIONAL_HEADER32), 112 bytes for PE32+
// (IMAGE_OPTIONAL_HEADER64). PE32+ omits BaseOfData and widens ImageBase and
// the stack/heap reserve/commit fields to 8 bytes each.
const (
	optionalHeaderFixedSize32 = 96
	optionalHeaderFixedSize64 = 112
)

// DataDirectorySize is the on-disk size of one IMAGE_DATA_DIRECTORY entry
// (a VirtualAddress/Size pair, 4 bytes each).
const DataDirectorySize = 8

// NumDataDirectories is the conventional number of data directory entries
// (the value of NumberOfRvaAndSizes in virtually all modern images).
const NumDataDirectories = 16

// Data directory indices, in the fixed order defined by the spec. Index into
// OptionalHeader.DataDirectory (bounds-check first: a nonconforming image may
// have fewer than NumDataDirectories entries).
const (
	DirEntryExport        = 0  // IMAGE_DIRECTORY_ENTRY_EXPORT
	DirEntryImport        = 1  // IMAGE_DIRECTORY_ENTRY_IMPORT
	DirEntryResource      = 2  // IMAGE_DIRECTORY_ENTRY_RESOURCE
	DirEntryException     = 3  // IMAGE_DIRECTORY_ENTRY_EXCEPTION
	DirEntrySecurity      = 4  // IMAGE_DIRECTORY_ENTRY_SECURITY (Certificate Table)
	DirEntryBaseReloc     = 5  // IMAGE_DIRECTORY_ENTRY_BASERELOC
	DirEntryDebug         = 6  // IMAGE_DIRECTORY_ENTRY_DEBUG
	DirEntryArchitecture  = 7  // IMAGE_DIRECTORY_ENTRY_ARCHITECTURE (reserved, must be 0)
	DirEntryGlobalPtr     = 8  // IMAGE_DIRECTORY_ENTRY_GLOBALPTR
	DirEntryTLS           = 9  // IMAGE_DIRECTORY_ENTRY_TLS
	DirEntryLoadConfig    = 10 // IMAGE_DIRECTORY_ENTRY_LOAD_CONFIG
	DirEntryBoundImport   = 11 // IMAGE_DIRECTORY_ENTRY_BOUND_IMPORT
	DirEntryIAT           = 12 // IMAGE_DIRECTORY_ENTRY_IAT
	DirEntryDelayImport   = 13 // IMAGE_DIRECTORY_ENTRY_DELAY_IMPORT
	DirEntryCLRRuntimeHdr = 14 // IMAGE_DIRECTORY_ENTRY_COM_DESCRIPTOR (CLR header)
	DirEntryReserved      = 15 // reserved, must be zero
)

// ErrUnknownOptionalHeaderMagic is returned when an optional header's Magic
// field is neither OptionalHeaderMagicPE32 nor OptionalHeaderMagicPE32Plus.
var ErrUnknownOptionalHeaderMagic = errors.New("pe: unknown optional header magic")

// DataDirectory is one entry of the optional header's DataDirectory array
// (IMAGE_DATA_DIRECTORY): an RVA/size pair describing some other part of the
// image.
//
// The Certificate Table entry (index DirEntrySecurity) is a well-known
// exception: unlike every other entry, its VirtualAddress field holds a
// *file offset*, not an RVA, because attribute certificates are not mapped
// into memory as part of the loaded image. See OptionalHeader.SecurityDir.
type DataDirectory struct {
	// VirtualAddress is an RVA for every entry except DirEntrySecurity, where
	// it is a file offset instead.
	VirtualAddress uint32
	// Size is the size in bytes of the directory's target data.
	Size uint32
}

// OptionalHeader is the in-memory form of the PE optional header, covering
// both the PE32 (IMAGE_OPTIONAL_HEADER32) and PE32+ (IMAGE_OPTIONAL_HEADER64)
// on-disk layouts. Which layout is used is selected by Magic; fields that
// differ in width between the two variants are widened to their PE32+ size
// here (e.g. ImageBase is always a uint64, even for a PE32 image where it is
// stored on disk as 32 bits).
type OptionalHeader struct {
	// Magic is OptionalHeaderMagicPE32 or OptionalHeaderMagicPE32Plus,
	// selecting the on-disk layout.
	Magic uint16

	MajorLinkerVersion uint8
	MinorLinkerVersion uint8

	SizeOfCode              uint32
	SizeOfInitializedData   uint32
	SizeOfUninitializedData uint32
	AddressOfEntryPoint     uint32
	BaseOfCode              uint32
	// BaseOfData is present only in the PE32 layout; it is not stored on
	// disk (and is ignored) for a PE32+ image.
	BaseOfData uint32

	// ImageBase is the preferred load address: 32 bits on disk for PE32, 64
	// bits for PE32+.
	ImageBase uint64

	SectionAlignment uint32
	FileAlignment    uint32

	MajorOperatingSystemVersion uint16
	MinorOperatingSystemVersion uint16
	MajorImageVersion           uint16
	MinorImageVersion           uint16
	MajorSubsystemVersion       uint16
	MinorSubsystemVersion       uint16
	Win32VersionValue           uint32

	SizeOfImage   uint32
	SizeOfHeaders uint32
	CheckSum      uint32

	Subsystem          uint16
	DllCharacteristics uint16

	// SizeOfStackReserve, SizeOfStackCommit, SizeOfHeapReserve, and
	// SizeOfHeapCommit are 32 bits on disk for PE32, 64 bits for PE32+.
	SizeOfStackReserve uint64
	SizeOfStackCommit  uint64
	SizeOfHeapReserve  uint64
	SizeOfHeapCommit   uint64

	LoaderFlags uint32

	// DataDirectory holds NumberOfRvaAndSizes entries (conventionally
	// NumDataDirectories = 16). Index it with the DirEntry* constants.
	DataDirectory []DataDirectory
}

// Is32Bit reports whether o uses the PE32 (32-bit) layout.
func (o OptionalHeader) Is32Bit() bool { return o.Magic == OptionalHeaderMagicPE32 }

// Is64Bit reports whether o uses the PE32+ (64-bit) layout.
func (o OptionalHeader) Is64Bit() bool { return o.Magic == OptionalHeaderMagicPE32Plus }

// Directory returns the data directory entry at index i, or the zero
// DataDirectory and false if o has no such entry (a nonconforming image
// declared fewer than i+1 directories).
func (o OptionalHeader) Directory(i int) (DataDirectory, bool) {
	if i < 0 || i >= len(o.DataDirectory) {
		return DataDirectory{}, false
	}
	return o.DataDirectory[i], true
}

// EncodedLen returns the number of bytes AppendTo will write: the fixed
// portion for o.Magic plus 8 bytes per DataDirectory entry.
func (o OptionalHeader) EncodedLen() (int, error) {
	switch o.Magic {
	case OptionalHeaderMagicPE32:
		return optionalHeaderFixedSize32 + DataDirectorySize*len(o.DataDirectory), nil
	case OptionalHeaderMagicPE32Plus:
		return optionalHeaderFixedSize64 + DataDirectorySize*len(o.DataDirectory), nil
	default:
		return 0, fmt.Errorf("%w: %#04x", ErrUnknownOptionalHeaderMagic, o.Magic)
	}
}

// parseOptionalHeader decodes an OptionalHeader from b, which must be exactly
// the SizeOfOptionalHeader-length slice from the COFF file header.
func parseOptionalHeader(b []byte) (OptionalHeader, error) {
	if len(b) < 2 {
		return OptionalHeader{}, fmt.Errorf("%w: optional header too short for magic", ErrTruncated)
	}
	var o OptionalHeader
	o.Magic = le.Uint16(b[0:2])

	var fixedSize int
	switch o.Magic {
	case OptionalHeaderMagicPE32:
		fixedSize = optionalHeaderFixedSize32
	case OptionalHeaderMagicPE32Plus:
		fixedSize = optionalHeaderFixedSize64
	default:
		return OptionalHeader{}, fmt.Errorf("%w: %#04x", ErrUnknownOptionalHeaderMagic, o.Magic)
	}
	if len(b) < fixedSize {
		return OptionalHeader{}, fmt.Errorf("%w: need %d bytes for optional header fixed portion, have %d", ErrTruncated, fixedSize, len(b))
	}

	o.MajorLinkerVersion = b[2]
	o.MinorLinkerVersion = b[3]
	o.SizeOfCode = le.Uint32(b[4:8])
	o.SizeOfInitializedData = le.Uint32(b[8:12])
	o.SizeOfUninitializedData = le.Uint32(b[12:16])
	o.AddressOfEntryPoint = le.Uint32(b[16:20])
	o.BaseOfCode = le.Uint32(b[20:24])

	var off int
	if o.Is32Bit() {
		o.BaseOfData = le.Uint32(b[24:28])
		o.ImageBase = uint64(le.Uint32(b[28:32]))
		off = 32
	} else {
		o.ImageBase = le.Uint64(b[24:32])
		off = 32
	}

	o.SectionAlignment = le.Uint32(b[off : off+4])
	o.FileAlignment = le.Uint32(b[off+4 : off+8])
	o.MajorOperatingSystemVersion = le.Uint16(b[off+8 : off+10])
	o.MinorOperatingSystemVersion = le.Uint16(b[off+10 : off+12])
	o.MajorImageVersion = le.Uint16(b[off+12 : off+14])
	o.MinorImageVersion = le.Uint16(b[off+14 : off+16])
	o.MajorSubsystemVersion = le.Uint16(b[off+16 : off+18])
	o.MinorSubsystemVersion = le.Uint16(b[off+18 : off+20])
	o.Win32VersionValue = le.Uint32(b[off+20 : off+24])
	o.SizeOfImage = le.Uint32(b[off+24 : off+28])
	o.SizeOfHeaders = le.Uint32(b[off+28 : off+32])
	o.CheckSum = le.Uint32(b[off+32 : off+36])
	o.Subsystem = le.Uint16(b[off+36 : off+38])
	o.DllCharacteristics = le.Uint16(b[off+38 : off+40])
	off += 40

	if o.Is32Bit() {
		o.SizeOfStackReserve = uint64(le.Uint32(b[off : off+4]))
		o.SizeOfStackCommit = uint64(le.Uint32(b[off+4 : off+8]))
		o.SizeOfHeapReserve = uint64(le.Uint32(b[off+8 : off+12]))
		o.SizeOfHeapCommit = uint64(le.Uint32(b[off+12 : off+16]))
		off += 16
	} else {
		o.SizeOfStackReserve = le.Uint64(b[off : off+8])
		o.SizeOfStackCommit = le.Uint64(b[off+8 : off+16])
		o.SizeOfHeapReserve = le.Uint64(b[off+16 : off+24])
		o.SizeOfHeapCommit = le.Uint64(b[off+24 : off+32])
		off += 32
	}

	o.LoaderFlags = le.Uint32(b[off : off+4])
	numRVA := le.Uint32(b[off+4 : off+8])
	off += 8

	if off != fixedSize {
		return OptionalHeader{}, fmt.Errorf("pe: internal error: optional header fixed layout mismatch (%d != %d)", off, fixedSize)
	}

	dirBytes := len(b) - fixedSize
	n := dirBytes / DataDirectorySize
	if uint64(n) > uint64(numRVA) {
		// Tolerate trailing padding/extra declared bytes beyond
		// NumberOfRvaAndSizes; only decode the declared entries.
		n = int(numRVA)
	}
	o.DataDirectory = make([]DataDirectory, n)
	for i := 0; i < n; i++ {
		e := b[fixedSize+i*DataDirectorySize:]
		o.DataDirectory[i] = DataDirectory{
			VirtualAddress: le.Uint32(e[0:4]),
			Size:           le.Uint32(e[4:8]),
		}
	}
	return o, nil
}

// AppendTo serializes o, appending EncodedLen() bytes to dst. The
// NumberOfRvaAndSizes field written is always len(o.DataDirectory), not a
// separately tracked value.
func (o OptionalHeader) AppendTo(dst []byte) ([]byte, error) {
	n, err := o.EncodedLen()
	if err != nil {
		return dst, err
	}
	b := make([]byte, n)
	le.PutUint16(b[0:2], o.Magic)
	b[2] = o.MajorLinkerVersion
	b[3] = o.MinorLinkerVersion
	le.PutUint32(b[4:8], o.SizeOfCode)
	le.PutUint32(b[8:12], o.SizeOfInitializedData)
	le.PutUint32(b[12:16], o.SizeOfUninitializedData)
	le.PutUint32(b[16:20], o.AddressOfEntryPoint)
	le.PutUint32(b[20:24], o.BaseOfCode)

	var off int
	if o.Is32Bit() {
		le.PutUint32(b[24:28], o.BaseOfData)
		le.PutUint32(b[28:32], uint32(o.ImageBase))
		off = 32
	} else {
		le.PutUint64(b[24:32], o.ImageBase)
		off = 32
	}

	le.PutUint32(b[off:off+4], o.SectionAlignment)
	le.PutUint32(b[off+4:off+8], o.FileAlignment)
	le.PutUint16(b[off+8:off+10], o.MajorOperatingSystemVersion)
	le.PutUint16(b[off+10:off+12], o.MinorOperatingSystemVersion)
	le.PutUint16(b[off+12:off+14], o.MajorImageVersion)
	le.PutUint16(b[off+14:off+16], o.MinorImageVersion)
	le.PutUint16(b[off+16:off+18], o.MajorSubsystemVersion)
	le.PutUint16(b[off+18:off+20], o.MinorSubsystemVersion)
	le.PutUint32(b[off+20:off+24], o.Win32VersionValue)
	le.PutUint32(b[off+24:off+28], o.SizeOfImage)
	le.PutUint32(b[off+28:off+32], o.SizeOfHeaders)
	le.PutUint32(b[off+32:off+36], o.CheckSum)
	le.PutUint16(b[off+36:off+38], o.Subsystem)
	le.PutUint16(b[off+38:off+40], o.DllCharacteristics)
	off += 40

	if o.Is32Bit() {
		le.PutUint32(b[off:off+4], uint32(o.SizeOfStackReserve))
		le.PutUint32(b[off+4:off+8], uint32(o.SizeOfStackCommit))
		le.PutUint32(b[off+8:off+12], uint32(o.SizeOfHeapReserve))
		le.PutUint32(b[off+12:off+16], uint32(o.SizeOfHeapCommit))
		off += 16
	} else {
		le.PutUint64(b[off:off+8], o.SizeOfStackReserve)
		le.PutUint64(b[off+8:off+16], o.SizeOfStackCommit)
		le.PutUint64(b[off+16:off+24], o.SizeOfHeapReserve)
		le.PutUint64(b[off+24:off+32], o.SizeOfHeapCommit)
		off += 32
	}

	le.PutUint32(b[off:off+4], o.LoaderFlags)
	le.PutUint32(b[off+4:off+8], uint32(len(o.DataDirectory)))
	off += 8

	for i, d := range o.DataDirectory {
		e := b[off+i*DataDirectorySize:]
		le.PutUint32(e[0:4], d.VirtualAddress)
		le.PutUint32(e[4:8], d.Size)
	}

	return append(dst, b...), nil
}
