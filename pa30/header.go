package pa30

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Hash algorithm IDs a PA30 TargetHashAlgId field may hold (Windows CryptoAPI
// ALG_ID constants; only the ones needed to verify Decode's output are
// listed here).
const (
	CalgMD5    = 0x8003
	CalgSHA1   = 0x8004
	CalgSHA256 = 0x800c
)

// Header holds a PA30 patch's fixed prolog plus the outer bitstream's
// header fields, mirroring GetDeltaInfo's DELTA_HEADER_INFO shape as
// documented in the README this package is specified from (see doc.go).
type Header struct {
	TargetFileTime  uint64
	FileTypeSet     uint32
	FileType        uint32
	Flags           uint32
	TargetSize      uint32
	TargetHashAlgID uint32
	TargetHash      []byte
}

// Decode decodes a PA30 patch file's target buffer. See doc.go for scope
// (null-delta only) and verification status.
func Decode(data []byte) ([]byte, *Header, error) {
	if len(data) < 12 || string(data[0:4]) != "PA30" {
		return nil, nil, fmt.Errorf("pa30: missing PA30 signature")
	}
	h := &Header{
		TargetFileTime: binary.LittleEndian.Uint64(data[4:12]),
	}

	br, err := newBitReader(data[12:])
	if err != nil {
		return nil, nil, err
	}

	var readErr error
	read := func(what string, dst *uint32) {
		if readErr != nil {
			return
		}
		*dst, readErr = br.readNumber()
		if readErr != nil {
			readErr = fmt.Errorf("pa30: %s: %w", what, readErr)
		}
	}
	read("FileTypeSet", &h.FileTypeSet)
	read("FileType", &h.FileType)
	read("Flags", &h.Flags)
	read("TargetSize", &h.TargetSize)
	read("TargetHashAlgID", &h.TargetHashAlgID)
	if readErr != nil {
		return nil, nil, readErr
	}

	targetHash, err := readBuffer(br)
	if err != nil {
		return nil, nil, fmt.Errorf("pa30: TargetHash: %w", err)
	}
	h.TargetHash = targetHash

	preProcess, err := readBuffer(br)
	if err != nil {
		return nil, nil, fmt.Errorf("pa30: preProcessBuffer: %w", err)
	}
	if len(preProcess) > 0 {
		return nil, h, fmt.Errorf("pa30: non-empty preprocessing buffer not supported")
	}

	patchBuf, err := readBuffer(br)
	if err != nil {
		return nil, nil, fmt.Errorf("pa30: patchBuffer: %w", err)
	}

	out, err := parsePatchBuffer(patchBuf, int(h.TargetSize))
	if err != nil {
		return nil, h, err
	}
	if len(out) != int(h.TargetSize) {
		return nil, h, fmt.Errorf("pa30: decoded %d bytes, want TargetSize %d", len(out), h.TargetSize)
	}
	if err := verifyHash(out, h.TargetHashAlgID, h.TargetHash); err != nil {
		return nil, h, err
	}
	return out, h, nil
}

// readBuffer reads a "buffer": a number (size), then padding to the next
// byte boundary, then that many raw content bytes.
func readBuffer(br *bitReader) ([]byte, error) {
	size, err := br.readNumber()
	if err != nil {
		return nil, err
	}
	br.alignToByte()
	return br.readRawBytes(int(size))
}

// verifyHash checks data against want using the hash algorithm identified
// by algID, if recognized; unrecognized algorithm IDs are not verified
// (Decode does not fail merely because it doesn't know how to check a
// given TargetHashAlgId).
func verifyHash(data []byte, algID uint32, want []byte) error {
	var got []byte
	switch algID {
	case CalgMD5:
		sum := md5.Sum(data)
		got = sum[:]
	case CalgSHA1:
		sum := sha1.Sum(data)
		got = sum[:]
	case CalgSHA256:
		sum := sha256.Sum256(data)
		got = sum[:]
	default:
		return nil
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("pa30: target hash mismatch (algorithm 0x%x)", algID)
	}
	return nil
}
