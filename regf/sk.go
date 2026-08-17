package regf

import "fmt"

// skHeaderSize is the size of the fixed-length portion of a version-1.2+
// security-key (sk) cell, up to and including the descriptor-size field.
// See "Security key - version 1.2 and later".
const skHeaderSize = 20

var skMagic = [2]byte{'s', 'k'}

// skCell is a decoded sk cell. Windows shares sk cells (and their reference
// counts) across keys with identical security descriptors via the
// Previous/Next doubly-linked circular list; this package preserves those
// fields on Parse, and reproduces the same sharing when building a fresh
// hive (see skPool/AppendTo in hive.go) -- the descriptor bytes themselves
// are still preserved opaquely (see the package doc's non-goal on
// descriptor interpretation), just deduplicated by exact byte match.
type skCell struct {
	// unknown is the 2-byte "unknown" field at header offset 2.
	unknown uint16
	// prevOffset/nextOffset point at the neighboring sk cells in the
	// hive's circular list (relative to the hive bins data area).
	prevOffset uint32
	nextOffset uint32
	// refCount is the number of nk cells referencing this sk cell.
	refCount uint32
	// descriptor is the raw self-relative security descriptor bytes,
	// preserved opaquely (see the package doc's non-goals).
	descriptor []byte
}

// parseSKCell decodes an sk cell's data (not including trailing padding).
func parseSKCell(data []byte) (*skCell, error) {
	if len(data) < skHeaderSize {
		return nil, fmt.Errorf("sk cell too short: need %d bytes, have %d", skHeaderSize, len(data))
	}
	if string(data[0:2]) != string(skMagic[:]) {
		return nil, fmt.Errorf("sk cell: bad signature %q", data[0:2])
	}
	size := le.Uint32(data[16:20])
	if skHeaderSize+int(size) > len(data) {
		return nil, fmt.Errorf("sk cell: descriptor (size %d) overruns cell", size)
	}
	return &skCell{
		unknown:    le.Uint16(data[2:4]),
		prevOffset: le.Uint32(data[4:8]),
		nextOffset: le.Uint32(data[8:12]),
		refCount:   le.Uint32(data[12:16]),
		descriptor: cloneBytes(data[skHeaderSize : skHeaderSize+int(size)]),
	}, nil
}

// appendTo serializes the sk cell's fixed portion and descriptor, without
// padding.
func (s *skCell) appendTo(dst []byte) []byte {
	var hdr [skHeaderSize]byte
	copy(hdr[0:2], skMagic[:])
	le.PutUint16(hdr[2:4], s.unknown)
	le.PutUint32(hdr[4:8], s.prevOffset)
	le.PutUint32(hdr[8:12], s.nextOffset)
	le.PutUint32(hdr[12:16], s.refCount)
	le.PutUint32(hdr[16:20], uint32(len(s.descriptor)))
	dst = append(dst, hdr[:]...)
	dst = append(dst, s.descriptor...)
	return dst
}
