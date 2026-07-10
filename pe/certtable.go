package pe

import (
	"errors"
	"fmt"
)

// winCertificateHeaderSize is the on-disk size of the fixed portion of a
// WIN_CERTIFICATE entry (dwLength, wRevision, wCertificateType), preceding
// the variable-length bCertificate payload.
const winCertificateHeaderSize = 8

// WIN_CERTIFICATE.wRevision values.
const (
	CertRevision1_0 uint16 = 0x0100
	CertRevision2_0 uint16 = 0x0200
)

// WIN_CERTIFICATE.wCertificateType values.
const (
	CertTypeX509           uint16 = 0x0001
	CertTypePKCSSignedData uint16 = 0x0002 // Authenticode: a PKCS#7 SignedData blob
	CertTypeReserved1      uint16 = 0x0003
	CertTypeTSStackSigned  uint16 = 0x0004
)

// ErrTruncatedCertEntry is returned when a WIN_CERTIFICATE entry's declared
// dwLength runs past the end of the Attribute Certificate Table.
var ErrTruncatedCertEntry = errors.New("pe: truncated WIN_CERTIFICATE entry")

// Certificate is the in-memory form of one WIN_CERTIFICATE entry of the
// Attribute Certificate Table (the table located by the Security data
// directory, OptionalHeader.DataDirectory[DirEntrySecurity]).
//
// For an Authenticode-signed driver, Type is CertTypePKCSSignedData and Data
// is a raw PKCS#7 SignedData structure (the Authenticode signature). This
// package does not parse that structure; see the sibling package
// github.com/gavin-john/gowim/cat for structural PKCS#7 parsing.
type Certificate struct {
	// Revision is one of the CertRevision* constants (WIN_CERTIFICATE
	// wRevision).
	Revision uint16
	// Type is one of the CertType* constants (WIN_CERTIFICATE
	// wCertificateType).
	Type uint16
	// Data is the raw bCertificate payload, i.e. dwLength-8 bytes. It does
	// not include the zero padding that follows the entry on disk.
	Data []byte
}

// align8 rounds n up to the next multiple of 8.
func align8(n int) int {
	return (n + 7) &^ 7
}

// ParseCertificateTable decodes the sequence of WIN_CERTIFICATE entries in
// raw, which must be exactly the bytes located by the Security data
// directory (raw[dir.VirtualAddress:][:dir.Size], where dir.VirtualAddress is
// a file offset, not an RVA — see DataDirectory).
//
// Each entry's dwLength covers only its own header and bCertificate payload
// (not the padding that follows); on disk, each entry is zero-padded so the
// next entry begins on an 8-byte boundary. This padding convention is a
// well-known ambiguity in third-party PE tooling — some implementations fold
// the pad into dwLength. This package follows the convention used by
// Microsoft's own signtool/imagehlp output and by osslsigncode: dwLength is
// the unpadded length, and the next entry is found at
// align8(offset+dwLength).
func ParseCertificateTable(raw []byte) ([]Certificate, error) {
	var certs []Certificate
	off := 0
	for off+winCertificateHeaderSize <= len(raw) {
		dwLength := le.Uint32(raw[off : off+4])
		if dwLength < winCertificateHeaderSize {
			return nil, fmt.Errorf("pe: WIN_CERTIFICATE dwLength %d smaller than header", dwLength)
		}
		if off+int(dwLength) > len(raw) {
			return nil, fmt.Errorf("%w: entry at offset %d declares length %d, table has %d bytes remaining", ErrTruncatedCertEntry, off, dwLength, len(raw)-off)
		}
		c := Certificate{
			Revision: le.Uint16(raw[off+4 : off+6]),
			Type:     le.Uint16(raw[off+6 : off+8]),
			Data:     append([]byte(nil), raw[off+winCertificateHeaderSize:off+int(dwLength)]...),
		}
		certs = append(certs, c)
		off = align8(off + int(dwLength))
	}
	return certs, nil
}

// AppendCertificateTable serializes certs as a sequence of WIN_CERTIFICATE
// entries, appending to dst. Each entry is zero-padded so the next entry (or
// the end of the table) falls on an 8-byte boundary, per the convention
// documented on ParseCertificateTable.
func AppendCertificateTable(dst []byte, certs []Certificate) []byte {
	for _, c := range certs {
		dwLength := winCertificateHeaderSize + len(c.Data)
		var hdr [winCertificateHeaderSize]byte
		le.PutUint32(hdr[0:4], uint32(dwLength))
		le.PutUint16(hdr[4:6], c.Revision)
		le.PutUint16(hdr[6:8], c.Type)
		dst = append(dst, hdr[:]...)
		dst = append(dst, c.Data...)
		if pad := align8(dwLength) - dwLength; pad > 0 {
			dst = append(dst, make([]byte, pad)...)
		}
	}
	return dst
}

// EncodedLen returns the number of bytes AppendCertificateTable will write
// for certs, including inter-entry padding.
func EncodedCertificateTableLen(certs []Certificate) int {
	n := 0
	for _, c := range certs {
		n += align8(winCertificateHeaderSize + len(c.Data))
	}
	return n
}
