// Package cab reads Microsoft Cabinet (.cab) files: the CFHEADER/CFFOLDER/
// CFFILE/CFDATA container format documented in Microsoft's
// "[MS-CAB]: Cabinet File Format" (https://learn.microsoft.com/en-us/previous-versions/bb417343(v=msdn.10),
// fetched and read in full 2026-09-14; also archived as the "cab-sdk.exe"
// Cabinet SDK documentation cited by libmspack, see cablzx.go), plus the
// specific compression method a real Windows Feature-on-Demand/Capability
// package (OpenSSH-Server-Package-amd64.cab) actually uses: LZX with a
// 21-bit (2 MiB) window (see cablzx.go for that decoder, ported from
// libmspack's lzxd.c -- (ii) widely-used open-source reference
// implementation -- rather than from the [MS-CAB] prose, which libmspack's
// own lzxd.c documents as being wrong in several places; see that file's
// header comment).
//
// This package deliberately does not implement:
//   - Writing/creating cabinets.
//   - Multi-cabinet spanning (a folder's data continuing into/from a
//     neighboring .cab in a set) -- CFFOLDER's iFolder 0xFFFD/0xFFFE/0xFFFF
//     markers are detected and rejected with ErrMultiCabinet rather than
//     silently mishandled, since a single Capability .cab (this package's
//     motivating case) is always self-contained.
//   - MSZIP or Quantum compression. Only "stored" (no compression) and LZX
//     are implemented, because those are the only two methods this
//     package's own real test data (a genuine downloaded
//     OpenSSH-Server-Package-amd64.cab, entirely LZX:21 per both this
//     package's own header decode and `7z l -slt`) and Microsoft's FOD
//     documentation exercise. A cabinet using MSZIP/Quantum returns
//     ErrUnsupportedCompression rather than guessing.
package cab

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Errors returned by this package.
var (
	ErrBadMagic               = errors.New("cab: not a cabinet file (bad MSCF signature)")
	ErrUnsupportedVersion     = errors.New("cab: unsupported cabinet format version")
	ErrTruncated              = errors.New("cab: truncated cabinet data")
	ErrMultiCabinet           = errors.New("cab: multi-cabinet spanning is not supported")
	ErrUnsupportedCompression = errors.New("cab: unsupported CFFOLDER compression method")
	ErrBadChecksum            = errors.New("cab: CFDATA checksum mismatch")
	ErrFileNotFound           = errors.New("cab: no such file in cabinet")
)

// Compression method, the low byte of a CFFOLDER's typeCompress field. Per
// [MS-CAB] section 2.4 (TypeCompress).
type CompressionMethod uint16

const (
	CompressNone    CompressionMethod = 0
	CompressMSZIP   CompressionMethod = 1
	CompressQuantum CompressionMethod = 2
	CompressLZX     CompressionMethod = 3
)

func (m CompressionMethod) String() string {
	switch m {
	case CompressNone:
		return "NONE"
	case CompressMSZIP:
		return "MSZIP"
	case CompressQuantum:
		return "QUANTUM"
	case CompressLZX:
		return "LZX"
	default:
		return fmt.Sprintf("unknown(%d)", uint16(m))
	}
}

// header flag bits, [MS-CAB] 2.4 flags field.
const (
	flagPrevCabinet    = 0x0001
	flagNextCabinet    = 0x0002
	flagReservePresent = 0x0004
)

// folderCompressType decodes a CFFOLDER typeCompress field: low byte is the
// CompressionMethod, and for LZX (and Quantum) the high byte is the window
// size in bits (e.g. 21 for a 2 MiB LZX window). This split is documented in
// [MS-CAB] 2.4 and independently confirmed against this package's real test
// cabinet, whose single folder's typeCompress high byte (0x15 = 21) matches
// `7z l -slt`'s independently-reported "Method = LZX:21" exactly.
func folderCompressType(raw uint16) (CompressionMethod, int) {
	return CompressionMethod(raw & 0x0f), int(raw >> 8)
}

// Folder is one CFFOLDER record: a compression "window" shared by one or
// more Files, spanning one or more CFDATA blocks.
type Folder struct {
	dataStart     uint32 // absolute file offset of this folder's first CFDATA block
	numDataBlocks uint16
	Method        CompressionMethod
	WindowBits    int // meaningful only for LZX/Quantum
}

// File is one CFFILE record: a name plus where its bytes live within some
// Folder's decompressed stream.
type File struct {
	Name         string
	Size         uint32
	FolderOffset uint32 // offset of this file's first byte within its folder's decompressed data
	Attributes   uint16
	ModTime      time.Time

	folder *Folder
}

// Reader parses a cabinet's directory (CFHEADER/CFFOLDER/CFFILE records)
// eagerly, but only reads/decompresses CFDATA on demand via Open/Extract.
type Reader struct {
	data    []byte
	Folders []*Folder
	Files   []*File

	cbCFData int // per-CFDATA-block reserved-field size (from CFHEADER's flags/cbCFData)

	decoded map[*Folder][]byte // memoized decompressed folder data, filled lazily by decodeFolder
}

// NewReader parses the CFHEADER, every CFFOLDER, and every CFFILE record
// in data (the full contents of a .cab file). It does not touch CFDATA.
func NewReader(data []byte) (*Reader, error) {
	if len(data) < 36 {
		return nil, ErrTruncated
	}
	if string(data[0:4]) != "MSCF" {
		return nil, ErrBadMagic
	}
	// reserved1 (u32) at 4:8, cbCabinet (u32) at 8:12, reserved2 (u32) 12:16
	coffFiles := binary.LittleEndian.Uint32(data[16:20])
	// reserved3 (u32) at 20:24
	versionMinor := data[24]
	versionMajor := data[25]
	if versionMajor != 1 || versionMinor != 3 {
		return nil, fmt.Errorf("%w: got %d.%d, want 1.3", ErrUnsupportedVersion, versionMajor, versionMinor)
	}
	cFolders := binary.LittleEndian.Uint16(data[26:28])
	cFiles := binary.LittleEndian.Uint16(data[28:30])
	flags := binary.LittleEndian.Uint16(data[30:32])
	// setID at 32:34, iCabinet at 34:36
	off := 36

	if flags&flagReservePresent != 0 {
		if off+4 > len(data) {
			return nil, ErrTruncated
		}
		cbCFHeader := int(binary.LittleEndian.Uint16(data[off : off+2]))
		cbCFFolder := int(data[off+2])
		cbCFData := int(data[off+3])
		off += 4 + cbCFHeader
		_ = cbCFFolder
		_ = cbCFData
		if off > len(data) {
			return nil, ErrTruncated
		}
		// per-folder/per-data reserve sizes are consumed below while
		// walking CFFOLDER/CFDATA records.
		if flags&flagPrevCabinet != 0 || flags&flagNextCabinet != 0 {
			return nil, ErrMultiCabinet
		}
		r := &Reader{data: data}
		return r.parseFolders(off, cFolders, cFiles, coffFiles, cbCFFolder, cbCFData)
	}
	if flags&flagPrevCabinet != 0 || flags&flagNextCabinet != 0 {
		return nil, ErrMultiCabinet
	}
	r := &Reader{data: data}
	return r.parseFolders(off, cFolders, cFiles, coffFiles, 0, 0)
}

func (r *Reader) parseFolders(off int, cFolders, cFiles uint16, coffFiles uint32, cbCFFolder, cbCFData int) (*Reader, error) {
	data := r.data
	for i := 0; i < int(cFolders); i++ {
		if off+8 > len(data) {
			return nil, ErrTruncated
		}
		coffCabStart := binary.LittleEndian.Uint32(data[off : off+4])
		numDataBlocks := binary.LittleEndian.Uint16(data[off+4 : off+6])
		typeCompress := binary.LittleEndian.Uint16(data[off+6 : off+8])
		off += 8 + cbCFFolder
		method, windowBits := folderCompressType(typeCompress)
		r.Folders = append(r.Folders, &Folder{
			dataStart:     coffCabStart,
			numDataBlocks: numDataBlocks,
			Method:        method,
			WindowBits:    windowBits,
		})
	}

	off = int(coffFiles)
	for i := 0; i < int(cFiles); i++ {
		if off+16 > len(data) {
			return nil, ErrTruncated
		}
		cbFile := binary.LittleEndian.Uint32(data[off : off+4])
		uoffFolderStart := binary.LittleEndian.Uint32(data[off+4 : off+8])
		iFolder := binary.LittleEndian.Uint16(data[off+8 : off+10])
		date := binary.LittleEndian.Uint16(data[off+10 : off+12])
		timeVal := binary.LittleEndian.Uint16(data[off+12 : off+14])
		attribs := binary.LittleEndian.Uint16(data[off+14 : off+16])
		off += 16
		if iFolder >= 0xFFFD {
			return nil, ErrMultiCabinet
		}
		if int(iFolder) >= len(r.Folders) {
			return nil, fmt.Errorf("cab: file references folder %d, only %d folders", iFolder, len(r.Folders))
		}
		nameEnd := off
		for nameEnd < len(data) && data[nameEnd] != 0 {
			nameEnd++
		}
		if nameEnd >= len(data) {
			return nil, ErrTruncated
		}
		name := string(data[off:nameEnd])
		off = nameEnd + 1

		r.Files = append(r.Files, &File{
			Name:         name,
			Size:         cbFile,
			FolderOffset: uoffFolderStart,
			Attributes:   attribs,
			ModTime:      dosDateTime(date, timeVal),
			folder:       r.Folders[iFolder],
		})
	}
	r.cbCFData = cbCFData
	return r, nil
}

func dosDateTime(date, t uint16) time.Time {
	year := int(date>>9) + 1980
	month := int((date >> 5) & 0x0f)
	day := int(date & 0x1f)
	hour := int(t >> 11)
	minute := int((t >> 5) & 0x3f)
	second := int((t & 0x1f) * 2)
	if month == 0 {
		month = 1
	}
	if day == 0 {
		day = 1
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
}
