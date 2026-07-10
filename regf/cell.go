package regf

import "fmt"

// cellSizeFieldSize is the size in bytes of a cell's leading size field. See
// "Hive bin cell": "Cell size | The value contains the 4 bytes of the size
// itself. The value is negative if the cell is allocated or positive if the
// cell is unallocated. The size is 8 byte aligned."
const cellSizeFieldSize = 4

// readCell reads one hive-bin cell from hbins (the hive bins data area,
// starting at its own offset 0) at byte offset off. It returns whether the
// cell is allocated (in use), the cell's data (everything after the size
// field, up to the cell's declared size), and the offset of the next cell.
func readCell(hbins []byte, off uint32) (allocated bool, data []byte, next uint32, err error) {
	o := int(off)
	if o < 0 || o+cellSizeFieldSize > len(hbins) {
		return false, nil, 0, fmt.Errorf("cell at offset %#x out of bounds", off)
	}
	rawSize := int32(le.Uint32(hbins[o : o+cellSizeFieldSize]))
	var size int
	if rawSize < 0 {
		allocated = true
		size = int(-rawSize)
	} else {
		size = int(rawSize)
	}
	if size < cellSizeFieldSize {
		return false, nil, 0, fmt.Errorf("cell at offset %#x has implausible size %d", off, size)
	}
	if size%8 != 0 {
		return false, nil, 0, fmt.Errorf("cell at offset %#x has non-8-aligned size %d", off, size)
	}
	end := o + size
	if end > len(hbins) || end < o {
		return false, nil, 0, fmt.Errorf("cell at offset %#x (size %d) overruns hive bins data", off, size)
	}
	return allocated, hbins[o+cellSizeFieldSize : end], uint32(end), nil
}

// cellArena accumulates newly-allocated cells into a growing hive-bins-data
// buffer, used when serializing a Hive from an in-memory Key tree (see
// hive.go). Offsets returned by alloc are relative to the start of the
// arena, matching on-disk cell-offset semantics once the arena becomes the
// hive bins data area.
type cellArena struct {
	buf []byte
}

// alloc appends one allocated (in-use) cell containing data, padding it to
// an 8-byte boundary, and returns the offset of the cell (its size field),
// relative to the start of the arena.
func (a *cellArena) alloc(data []byte) uint32 {
	off := uint32(len(a.buf))
	size := alignUp8(cellSizeFieldSize + len(data))
	cell := make([]byte, size)
	le.PutUint32(cell[0:4], uint32(-int32(size)))
	copy(cell[4:], data)
	a.buf = append(a.buf, cell...)
	return off
}
