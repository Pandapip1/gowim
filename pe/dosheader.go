package pe

import (
	"bytes"
	"errors"
	"fmt"
)

// DOSHeaderSize is the fixed size in bytes of IMAGE_DOS_HEADER, the MS-DOS
// compatibility header at the start of every PE image.
const DOSHeaderSize = 64

// DOSMagic is the value of the e_magic field ("MZ") that identifies a
// IMAGE_DOS_HEADER.
const DOSMagic uint16 = 0x5A4D

// offELfanew is the byte offset of the e_lfanew field within
// IMAGE_DOS_HEADER: a 4-byte little-endian file offset to the PE signature.
// This is the only DOS-header field this package interprets; the rest of the
// 64-byte header (and the DOS stub program that follows it) is preserved
// verbatim rather than modeled.
const offELfanew = 0x3C

// PESignatureSize is the size in bytes of the PE signature.
const PESignatureSize = 4

// PESignature is the 4-byte value ("PE\0\0") that appears at the file offset
// given by e_lfanew, immediately before the COFF file header.
var PESignature = [PESignatureSize]byte{'P', 'E', 0, 0}

// Sentinel errors returned while locating the DOS header and PE signature.
var (
	ErrNotPE         = errors.New("pe: invalid DOS or PE signature")
	ErrInvalidLfanew = errors.New("pe: e_lfanew out of range")
	ErrTruncated     = errors.New("pe: input truncated")
)

// parseDOSHeader validates the e_magic field, reads e_lfanew, and returns the
// verbatim bytes between the end of IMAGE_DOS_HEADER and e_lfanew (the DOS
// stub program, MS-DOS message and all — this package does not attempt to
// disassemble or regenerate it).
func parseDOSHeader(data []byte) (stub []byte, lfanew uint32, err error) {
	if len(data) < DOSHeaderSize {
		return nil, 0, fmt.Errorf("%w: need at least %d bytes for DOS header, have %d", ErrTruncated, DOSHeaderSize, len(data))
	}
	if magic := le.Uint16(data[0:2]); magic != DOSMagic {
		return nil, 0, fmt.Errorf("%w: e_magic = %#04x, want %#04x", ErrNotPE, magic, DOSMagic)
	}
	lfanew = le.Uint32(data[offELfanew : offELfanew+4])
	if uint64(lfanew) < DOSHeaderSize || uint64(lfanew)+PESignatureSize > uint64(len(data)) {
		return nil, 0, fmt.Errorf("%w: e_lfanew = %#x, file size %d", ErrInvalidLfanew, lfanew, len(data))
	}
	if !bytes.Equal(data[lfanew:lfanew+PESignatureSize], PESignature[:]) {
		return nil, 0, fmt.Errorf("%w: PE signature at %#x is %v, want %v", ErrNotPE, lfanew, data[lfanew:lfanew+PESignatureSize], PESignature)
	}
	stub = append([]byte(nil), data[DOSHeaderSize:lfanew]...)
	return stub, lfanew, nil
}

// appendDOSHeader appends the 64-byte IMAGE_DOS_HEADER (e_magic plus
// e_lfanew, computed as DOSHeaderSize+len(stub); all other DOS header fields
// are written as zero), the verbatim stub bytes, and the 4-byte PE signature.
func appendDOSHeader(dst []byte, stub []byte) []byte {
	var hdr [DOSHeaderSize]byte
	le.PutUint16(hdr[0:2], DOSMagic)
	le.PutUint32(hdr[offELfanew:offELfanew+4], uint32(DOSHeaderSize+len(stub)))
	dst = append(dst, hdr[:]...)
	dst = append(dst, stub...)
	dst = append(dst, PESignature[:]...)
	return dst
}
