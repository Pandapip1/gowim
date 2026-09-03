package lzx

import "testing"

// TestOffsetSlotTableMatchesScan verifies, exhaustively for every offset
// covered by offsetSlotTable and at every slot boundary in
// lzxOffsetSlotBase (including well beyond the table, up to the largest
// legal LZX offset), that offsetSlot's table-lookup fast path returns
// exactly what offsetSlotScan's original linear scan would return -- i.e.
// the O(1) table lookup added as a performance optimization cannot change
// which offset slot any offset resolves to.
func TestOffsetSlotTableMatchesScan(t *testing.T) {
	// Exhaustive check over the table's full range, plus a bit beyond it to
	// exercise the scan fallback right at the table boundary.
	for off := uint32(0); off < offsetSlotTableSize+1024; off++ {
		got := offsetSlot(off)
		want := offsetSlotScan(off)
		if got != want {
			t.Fatalf("offsetSlot(%d) = %d, want %d (offsetSlotScan)", off, got, want)
		}
	}

	// Every slot boundary in lzxOffsetSlotBase, and the offsets immediately
	// before/after each, including boundaries far beyond the table's range
	// (up to the largest offset a maxWindowSize window can produce).
	for slot, base := range lzxOffsetSlotBase {
		for _, off := range []int64{int64(base) - 2, int64(base) - 1, int64(base), int64(base) + 1} {
			if off < 0 {
				continue
			}
			o := uint32(off)
			got := offsetSlot(o)
			want := offsetSlotScan(o)
			if got != want {
				t.Fatalf("offsetSlot(%d) [near slot %d boundary] = %d, want %d", o, slot, got, want)
			}
		}
	}

	// The largest legal offset (maxWindowSize - 1) and a spread of values up
	// to it, all beyond the table's range, to exercise the fallback broadly.
	for off := uint32(offsetSlotTableSize); off < maxWindowSize; off += 997 {
		got := offsetSlot(off)
		want := offsetSlotScan(off)
		if got != want {
			t.Fatalf("offsetSlot(%d) = %d, want %d (offsetSlotScan)", off, got, want)
		}
	}
	if got, want := offsetSlot(maxWindowSize-1), offsetSlotScan(maxWindowSize-1); got != want {
		t.Fatalf("offsetSlot(maxWindowSize-1) = %d, want %d", got, want)
	}
}
