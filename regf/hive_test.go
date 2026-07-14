package regf

import "testing"

// TestResolveValueDataIgnoresCoincidentalDBSignature guards against a real
// bug found 2026-07-14 against a real Windows 11 25H2 COMPONENTS hive:
// resolveValueData used to treat any non-inline value cell whose first two
// bytes happened to read "db" as a big-data (data block) key, regardless of
// the value's actual dataSize -- but per libregf's "Windows NT Registry
// File (REGF) format specification" ("Value data": "values larger than
// 16344 bytes are stored in multiple segments"), the "db" cell convention
// only applies once dataSize exceeds DBSegmentMaxSize (16344). A small,
// perfectly ordinary value whose raw bytes happen to start with 0x64 0x62
// ('d','b') was misidentified as a data-block key, and the rest of its
// (much too short) 4-byte cell was misparsed as a data-block key's
// segment-count/segment-list-offset pair -- producing a nonsensical,
// wildly out-of-bounds segment-list offset instead of just returning the
// value's own 4 bytes.
func TestResolveValueDataIgnoresCoincidentalDBSignature(t *testing.T) {
	// One 8-byte cell: 4-byte size prefix (-8, allocated) + 4 bytes of
	// ordinary value data that happens to start with the "db" signature.
	hbins := make([]byte, 8)
	var negEight int32 = -8
	le.PutUint32(hbins[0:4], uint32(negEight))
	copy(hbins[4:8], []byte{'d', 'b', 0xAA, 0xBB})

	got, err := resolveValueData(hbins, 4, 0)
	if err != nil {
		t.Fatalf("resolveValueData: %v", err)
	}
	want := []byte{'d', 'b', 0xAA, 0xBB}
	if string(got) != string(want) {
		t.Errorf("resolveValueData = %v, want %v", got, want)
	}
}

// TestResolveValueDataRealDBCell confirms the other side: a value whose
// dataSize genuinely exceeds DBSegmentMaxSize is still correctly resolved
// through a real data-block key and segment list.
func TestResolveValueDataRealDBCell(t *testing.T) {
	const segLen = 20
	segment := make([]byte, segLen)
	for i := range segment {
		segment[i] = byte(i)
	}

	// Layout: [0] segment data cell, [8+segLen aligned] segment-list cell
	// (one 4-byte offset), [+8] db cell (header only).
	segCellSize := align8(cellSizeFieldSize + segLen)
	var hbins []byte
	segOff := uint32(len(hbins))
	hbins = append(hbins, make([]byte, segCellSize)...)
	le.PutUint32(hbins[segOff:segOff+4], uint32(int32(-int32(segCellSize))))
	copy(hbins[segOff+4:], segment)

	listCellSize := align8(cellSizeFieldSize + 4)
	listOff := uint32(len(hbins))
	hbins = append(hbins, make([]byte, listCellSize)...)
	le.PutUint32(hbins[listOff:listOff+4], uint32(int32(-int32(listCellSize))))
	le.PutUint32(hbins[listOff+4:listOff+8], segOff)

	dbCellSize := align8(cellSizeFieldSize + dbHeaderSize)
	dbOff := uint32(len(hbins))
	hbins = append(hbins, make([]byte, dbCellSize)...)
	le.PutUint32(hbins[dbOff:dbOff+4], uint32(int32(-int32(dbCellSize))))
	copy(hbins[dbOff+4:dbOff+6], []byte{'d', 'b'})
	le.PutUint16(hbins[dbOff+6:dbOff+8], 1) // numSegments
	le.PutUint32(hbins[dbOff+8:dbOff+12], listOff)

	dataSize := uint32(DBSegmentMaxSize) + 1 // force big-data path
	got, err := resolveValueData(hbins, dataSize, dbOff)
	if err != nil {
		t.Fatalf("resolveValueData: %v", err)
	}
	// The segment is shorter than dataSize, so only segLen bytes come back
	// (a real DBSegmentMaxSize+1-byte value would span 2 segments; this
	// test only exercises the db-cell/segment-list plumbing, not multi
	// segment concatenation, which bigdata.go's own tests already cover).
	if len(got) != segLen {
		t.Fatalf("len(got) = %d, want %d", len(got), segLen)
	}
	for i, b := range got {
		if b != byte(i) {
			t.Fatalf("got[%d] = %d, want %d", i, b, i)
		}
	}
}

func align8(n int) int { return (n + 7) &^ 7 }
