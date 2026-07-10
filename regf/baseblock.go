package regf

import (
	"errors"
	"fmt"
)

// BaseBlockSize is the size in bytes of the base block: a fixed 4096-byte
// header block at the start of a primary hive file. See the "File header"
// section: "The file header is stored in a 4096 byte header block."
const BaseBlockSize = 4096

// baseBlockChecksumRange is the number of leading bytes the checksum covers.
// See "File header": "Checksum | XOR-32 of the previous 508 bytes" (the
// field itself sits at offset 508, so it covers bytes [0,508)).
const baseBlockChecksumRange = 508

// magic is the base block's 4-byte signature, "regf".
var baseBlockMagic = [4]byte{'r', 'e', 'g', 'f'}

// File types, from the "File types" section. This package only supports
// FileTypePrimary; the log variants describe transaction log files, which
// this package does not parse (see the package doc's non-goals).
const (
	FileTypePrimary     uint32 = 0
	FileTypeLogVariant1 uint32 = 1
	FileTypeLogVariant2 uint32 = 2
	FileTypeLogVariant6 uint32 = 6
)

// Minor format versions, from the "Format versions" section. Every version
// listed there has major version 1; only the minor version varies. This
// package targets Version1_2 and later (the CM_KEY_NODE/CM_KEY_VALUE cell
// shapes); see the package doc's version-1.1 non-goal.
const (
	Version1_1 = 1 // NT 3.1/3.5, unsupported nk/vk/sk shape.
	Version1_2 = 2 // NT 3.51 and later.
	Version1_3 = 3 // NT 4.0 and later; REG_STANDARD_FORMAT (typically NTUSER.DAT).
	Version1_5 = 5 // Windows XP SP2 and later; REG_LATEST_FORMAT (typically SOFTWARE, SYSTEM).
	Version1_6 = 6 // Windows 10 (2004), Docker delta Registry files only.
)

// BaseBlock is the parsed 4096-byte base block (file header) at the start of
// a primary hive file. Field names and offsets are from the "File header"
// section of the spec.
type BaseBlock struct {
	// PrimarySequence and SecondarySequence should match if the hive was
	// cleanly synchronized (i.e. not in need of transaction-log replay).
	PrimarySequence   uint32
	SecondarySequence uint32
	// LastWritten is a Windows FILETIME (100ns ticks since 1601-01-01 UTC).
	LastWritten uint64
	// MajorVersion/MinorVersion select the on-disk nk/vk/sk cell shape; see
	// the Version1_* constants and the package doc's version-1.1 non-goal.
	MajorVersion uint32
	MinorVersion uint32
	// FileType distinguishes a primary hive from a transaction log variant;
	// see the FileType* constants. This package only serializes/round-trips
	// FileTypePrimary hives.
	FileType uint32
	// FileFormat is documented only as "0x0001 means direct memory load";
	// preserved verbatim.
	FileFormat uint32
	// RootCellOffset is the offset (relative to HBinDataStart) of the root
	// key's nk cell.
	RootCellOffset uint32
	// HiveBinsDataSize is the total size in bytes of the hive bins data area
	// that follows the base block.
	HiveBinsDataSize uint32
	// ClusteringFactor is documented as "logical sector size of the
	// underlying disk in bytes divided by 512"; commonly 1. Preserved
	// verbatim.
	ClusteringFactor uint32
	// FileName holds the raw 64-byte field at offset 48, documented as
	// "sometimes contains the last part of the filename in UTF-16 LE...
	// unused bytes are 0". Preserved verbatim; always exactly 64 bytes.
	FileName [64]byte
	// Reserved holds the raw 396-byte field at offset 112, documented as
	// "can contain remnant data / padding used for the checksum?".
	// Preserved verbatim; always exactly 396 bytes.
	Reserved [396]byte
	// Checksum is the XOR-32 of the first 508 bytes of the base block.
	// ParseBaseBlock validates it; AppendTo recomputes it.
	Checksum uint32
	// Tail holds the raw 3576 "Reserved" bytes at offset 512, documented as
	// possibly containing remnant data. Preserved verbatim; always exactly
	// 3576 bytes.
	Tail [3576]byte
	// BootType and BootRecover are documented as having "no meaning on a
	// disk"; preserved verbatim.
	BootType    uint32
	BootRecover uint32
}

// ErrInvalidBaseBlock is returned by ParseBaseBlock for structurally invalid
// input (bad magic, truncated buffer, or checksum mismatch).
var ErrInvalidBaseBlock = errors.New("regf: invalid base block")

// ComputeChecksum returns the XOR-32 checksum of the first 508 bytes of a
// 4096-byte base block buffer, per the "File header" section: "Checksum |
// XOR-32 of the previous 508 bytes". 508 is a whole number of little-endian
// uint32s (127), so the checksum is the XOR of those 127 dwords.
func ComputeChecksum(buf []byte) (uint32, error) {
	if len(buf) < baseBlockChecksumRange {
		return 0, fmt.Errorf("need at least %d bytes, have %d", baseBlockChecksumRange, len(buf))
	}
	var sum uint32
	for i := 0; i < baseBlockChecksumRange; i += 4 {
		sum ^= le.Uint32(buf[i : i+4])
	}
	return sum, nil
}

// ParseBaseBlock decodes the 4096-byte base block from the start of data.
// data must be at least BaseBlockSize bytes; only the first BaseBlockSize
// bytes are consumed.
func ParseBaseBlock(data []byte) (*BaseBlock, error) {
	if len(data) < BaseBlockSize {
		return nil, wrapErr("base block", fmt.Errorf("%w: need %d bytes, have %d", ErrInvalidBaseBlock, BaseBlockSize, len(data)))
	}
	b := data[:BaseBlockSize]
	if string(b[0:4]) != string(baseBlockMagic[:]) {
		return nil, wrapErr("base block", fmt.Errorf("%w: bad magic %q", ErrInvalidBaseBlock, b[0:4]))
	}

	wantSum, err := ComputeChecksum(b)
	if err != nil {
		return nil, wrapErr("base block checksum", err)
	}
	gotSum := le.Uint32(b[508:512])
	if wantSum != gotSum {
		return nil, wrapErr("base block", fmt.Errorf("%w: checksum mismatch: stored %#08x, computed %#08x", ErrInvalidBaseBlock, gotSum, wantSum))
	}

	bb := &BaseBlock{
		PrimarySequence:   le.Uint32(b[4:8]),
		SecondarySequence: le.Uint32(b[8:12]),
		LastWritten:       le.Uint64(b[12:20]),
		MajorVersion:      le.Uint32(b[20:24]),
		MinorVersion:      le.Uint32(b[24:28]),
		FileType:          le.Uint32(b[28:32]),
		FileFormat:        le.Uint32(b[32:36]),
		RootCellOffset:    le.Uint32(b[36:40]),
		HiveBinsDataSize:  le.Uint32(b[40:44]),
		ClusteringFactor:  le.Uint32(b[44:48]),
		Checksum:          gotSum,
		BootType:          le.Uint32(b[4088:4092]),
		BootRecover:       le.Uint32(b[4092:4096]),
	}
	copy(bb.FileName[:], b[48:112])
	copy(bb.Reserved[:], b[112:508])
	copy(bb.Tail[:], b[512:4088])

	if bb.MinorVersion == Version1_1 {
		return nil, wrapErr("base block", fmt.Errorf("%w: version 1.1 layout is not supported (see package doc)", ErrInvalidBaseBlock))
	}

	return bb, nil
}

// AppendTo serializes b as a 4096-byte base block, appending to dst.
// Checksum is recomputed from the other fields (any value previously set in
// b.Checksum is ignored).
func (b *BaseBlock) AppendTo(dst []byte) ([]byte, error) {
	var buf [BaseBlockSize]byte
	copy(buf[0:4], baseBlockMagic[:])
	le.PutUint32(buf[4:8], b.PrimarySequence)
	le.PutUint32(buf[8:12], b.SecondarySequence)
	le.PutUint64(buf[12:20], b.LastWritten)
	le.PutUint32(buf[20:24], b.MajorVersion)
	le.PutUint32(buf[24:28], b.MinorVersion)
	le.PutUint32(buf[28:32], b.FileType)
	le.PutUint32(buf[32:36], b.FileFormat)
	le.PutUint32(buf[36:40], b.RootCellOffset)
	le.PutUint32(buf[40:44], b.HiveBinsDataSize)
	le.PutUint32(buf[44:48], b.ClusteringFactor)
	copy(buf[48:112], b.FileName[:])
	copy(buf[112:508], b.Reserved[:])

	sum, err := ComputeChecksum(buf[:])
	if err != nil {
		return dst, wrapErr("base block checksum", err)
	}
	le.PutUint32(buf[508:512], sum)

	copy(buf[512:4088], b.Tail[:])
	le.PutUint32(buf[4088:4092], b.BootType)
	le.PutUint32(buf[4092:4096], b.BootRecover)

	return append(dst, buf[:]...), nil
}
