package regf

import "fmt"

// HBinHeaderSize is the size in bytes of a hive bin header. See "Hive bin
// header": a 32-byte header preceding each hive bin's cells.
const HBinHeaderSize = 32

var hbinMagic = [4]byte{'h', 'b', 'i', 'n'}

// HBin is one hive bin: a 32-byte header plus the raw bytes of the cells it
// contains. Cells are not split out here; Hive.Parse walks Cells directly
// using cell offsets relative to the whole hive bins data area (see
// hive.go), since cell offsets and hbin boundaries are independent (a cell
// never spans an hbin boundary, but this package does not need to track
// which cell belongs to which bin once parsed).
type HBin struct {
	// Offset is this bin's own offset (relative to the start of the hive
	// bins data area), as stored in its header. On a well-formed hive this
	// equals the sum of the sizes of all preceding bins.
	Offset uint32
	// Size is the size of this bin (header + cells), in bytes. Always a
	// multiple of 4096 in practice, though the spec does not state that as a
	// hard requirement.
	Size uint32
	// Reserved1/Reserved2 are the two "unknown, 0 most of the time" 4-byte
	// fields at header offsets 12 and 16.
	Reserved1 uint32
	Reserved2 uint32
	// Timestamp is a FILETIME, documented as normally valid only on the
	// first (root) hive bin and zero/remnant otherwise. Preserved verbatim.
	Timestamp uint64
	// Spare is the "unknown spare" field at header offset 28, documented as
	// "value similar to the size".
	Spare uint32
	// Cells holds the raw bytes of this bin's cells (everything after the
	// 32-byte header, i.e. Size-HBinHeaderSize bytes), as decoded by
	// parseHBin. Not used when writing (see writeHeader).
	Cells []byte
}

// parseHBin decodes one hive bin starting at hbins[off]. It returns the
// parsed header/cells and the offset of the next bin.
func parseHBin(hbins []byte, off uint32) (*HBin, uint32, error) {
	o := int(off)
	if o+HBinHeaderSize > len(hbins) {
		return nil, 0, fmt.Errorf("hbin header at offset %#x out of bounds", off)
	}
	h := hbins[o : o+HBinHeaderSize]
	if string(h[0:4]) != string(hbinMagic[:]) {
		return nil, 0, fmt.Errorf("hbin at offset %#x: bad magic %q", off, h[0:4])
	}
	bin := &HBin{
		Offset:    le.Uint32(h[4:8]),
		Size:      le.Uint32(h[8:12]),
		Reserved1: le.Uint32(h[12:16]),
		Reserved2: le.Uint32(h[16:20]),
		Timestamp: le.Uint64(h[20:28]),
		Spare:     le.Uint32(h[28:32]),
	}
	if bin.Size < HBinHeaderSize || bin.Size%8 != 0 {
		return nil, 0, fmt.Errorf("hbin at offset %#x has implausible size %d", off, bin.Size)
	}
	end := o + int(bin.Size)
	if end > len(hbins) || end < o {
		return nil, 0, fmt.Errorf("hbin at offset %#x (size %d) overruns hive bins data", off, bin.Size)
	}
	bin.Cells = cloneBytes(hbins[o+HBinHeaderSize : end])
	return bin, uint32(end), nil
}

// writeHeader encodes the hive bin's 32-byte header into buf[0:32] in
// place. The caller is responsible for placing the bin's cells immediately
// after (see hive.go's AppendTo, which reserves the header's 32 bytes at the
// front of its cell arena so that cell offsets -- relative to the whole hive
// bins data area, header included -- come out correct without adjustment).
func (h *HBin) writeHeader(buf []byte) {
	copy(buf[0:4], hbinMagic[:])
	le.PutUint32(buf[4:8], h.Offset)
	le.PutUint32(buf[8:12], h.Size)
	le.PutUint32(buf[12:16], h.Reserved1)
	le.PutUint32(buf[16:20], h.Reserved2)
	le.PutUint64(buf[20:28], h.Timestamp)
	le.PutUint32(buf[28:32], h.Spare)
}
