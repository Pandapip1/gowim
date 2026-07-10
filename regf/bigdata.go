package regf

import "fmt"

// dbHeaderSize is the size of a "db" (data block) cell's fixed portion. See
// "Data block key": 2-byte signature, 2-byte segment count, 4-byte segment
// list offset, 4 bytes of padding -- 12 bytes total.
const dbHeaderSize = 12

var dbMagic = [2]byte{'d', 'b'}

// DBSegmentMaxSize is the largest number of value-data bytes this package
// packs into one data-block segment cell. See "Value data": "values larger
// than 16344 bytes are stored in multiple segments", and the "Data block
// data segment > 16344 bytes" corruption scenario, which confirms 16344 is
// the effective per-segment limit ("The Windows implementation will ignore
// any additional data beyond 16344 bytes in a data block data segment").
const DBSegmentMaxSize = 16344

// dbCell is a decoded "db" cell's fixed portion (segment count and the
// offset of its segment-list cell). Resolving/building the segment list and
// segment data cells requires hbins access and is done in hive.go.
type dbCell struct {
	numSegments       uint16
	segmentListOffset uint32
}

func parseDBCell(data []byte) (*dbCell, error) {
	if len(data) < dbHeaderSize {
		return nil, fmt.Errorf("db cell too short: need %d bytes, have %d", dbHeaderSize, len(data))
	}
	if string(data[0:2]) != string(dbMagic[:]) {
		return nil, fmt.Errorf("db cell: bad signature %q", data[0:2])
	}
	return &dbCell{
		numSegments:       le.Uint16(data[2:4]),
		segmentListOffset: le.Uint32(data[4:8]),
	}, nil
}

func (d *dbCell) appendTo(dst []byte) []byte {
	var hdr [dbHeaderSize]byte
	copy(hdr[0:2], dbMagic[:])
	le.PutUint16(hdr[2:4], d.numSegments)
	le.PutUint32(hdr[4:8], d.segmentListOffset)
	// bytes [8:12) are the documented 4-byte padding field; left zero.
	return append(dst, hdr[:]...)
}

// parseSegmentList decodes a data-block segment-list cell's data (a flat
// array of 4-byte cell offsets, one per segment; see "Data block segment
// list").
func parseSegmentList(data []byte, numSegments uint16) ([]uint32, error) {
	need := int(numSegments) * 4
	if len(data) < need {
		return nil, fmt.Errorf("db segment list: need %d bytes for %d segments, have %d", need, numSegments, len(data))
	}
	offsets := make([]uint32, numSegments)
	for i := range offsets {
		offsets[i] = le.Uint32(data[i*4 : i*4+4])
	}
	return offsets, nil
}

func appendSegmentList(dst []byte, offsets []uint32) []byte {
	for _, o := range offsets {
		var buf [4]byte
		le.PutUint32(buf[0:4], o)
		dst = append(dst, buf[:]...)
	}
	return dst
}
