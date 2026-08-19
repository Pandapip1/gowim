package iso

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// UDF validation.
//
// Three layers of checking, in increasing order of how much they prove:
//
//  1. Unit tests of the primitives against the worked examples the standards
//     themselves publish (the CRC of ECMA-167 3/7.2.6 Note 4).
//  2. A parser written here, from the standard's field tables, that walks the
//     image the way a reader would: anchor at 256, Volume Descriptor
//     Sequence, Partition Descriptor, File Set Descriptor, root File Entry,
//     File Identifier Descriptors. It re-derives every extent independently
//     and checks the bytes it lands on against the input files. Its real job
//     is the cross-layer check: that a file's UDF allocation descriptor and
//     its ECMA-119 Directory Record name the *same* extent.
//  3. External tools that have no shared lineage with this package: udfinfo
//     from udftools, and 7z, which prefers the UDF view of a bridge volume
//     over the ISO 9660 one.

/**************** primitives ****************/

// TestCRCITUTWorkedExample checks the CRC against the example ECMA-167
// 3/7.2.6 Note 4 publishes: "the CRC of the three bytes #70 #6A #77 is
// #3299". Getting the CRC subtly wrong (a reflected variant, a non-zero
// initial value) is the classic way to produce descriptors that look right in
// a hex dump and are rejected by every real reader, so this is worth pinning.
func TestCRCITUTWorkedExample(t *testing.T) {
	if got := crcITUT([]byte{0x70, 0x6A, 0x77}); got != 0x3299 {
		t.Errorf("crcITUT(#70 #6A #77) = %#04x, want #3299 (ECMA-167 3/7.2.6 Note 4)", got)
	}
	if got := crcITUT(nil); got != 0 {
		t.Errorf("crcITUT of nothing = %#04x, want 0", got)
	}
}

// TestTagChecksum checks the rule of ECMA-167 3/7.2.3, "the sum modulo 256 of
// bytes 0-3 and 5-15 of the tag" — note the deliberate exclusion of byte 4,
// which is the checksum itself.
func TestTagChecksum(t *testing.T) {
	desc := make([]byte, 512)
	for i := range desc {
		desc[i] = byte(i)
	}
	putTag(desc, udfTagAnchorVolumeDescPtr, 256, 512)

	var sum byte
	for i := 0; i < 16; i++ {
		if i != 4 {
			sum += desc[i]
		}
	}
	if desc[4] != sum {
		t.Errorf("tag checksum is %#02x, want %#02x", desc[4], sum)
	}
	if got := binary.LittleEndian.Uint16(desc[0:2]); got != udfTagAnchorVolumeDescPtr {
		t.Errorf("tag identifier is %d, want %d", got, udfTagAnchorVolumeDescPtr)
	}
	// UDF 1.02 2.2.1 fixes the Descriptor Version at 2 for a UDF 1.02 volume,
	// even though ECMA-167 3/7.2.2 would prefer 3 for its own structures.
	if got := binary.LittleEndian.Uint16(desc[2:4]); got != 2 {
		t.Errorf("descriptor version is %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint32(desc[12:16]); got != 256 {
		t.Errorf("tag location is %d, want 256", got)
	}
	if got := binary.LittleEndian.Uint16(desc[10:12]); got != 512-16 {
		t.Errorf("descriptor CRC length is %d, want %d", got, 512-16)
	}
	if got := binary.LittleEndian.Uint16(desc[8:10]); got != crcITUT(desc[16:512]) {
		t.Errorf("descriptor CRC does not cover bytes 16..512")
	}
}

// TestOSTACompressedUnicode checks the two encodings of UDF 1.02 2.1.1 and
// the dstring rules of 2.1.3.
func TestOSTACompressedUnicode(t *testing.T) {
	if got := ostaCS0("AB"); !bytes.Equal(got, []byte{8, 'A', 'B'}) {
		t.Errorf("ostaCS0(%q) = % x, want 08 41 42", "AB", got)
	}
	// One character outside Latin-1 forces the whole identifier to the
	// 16-bit form; there is no per-character switch.
	if got := ostaCS0("Aé€"); !bytes.Equal(got, []byte{16, 0, 'A', 0, 0xE9, 0x20, 0xAC}) {
		t.Errorf("ostaCS0 of a non-Latin-1 string = % x", got)
	}

	// 2.1.3: the number of bytes used is recorded in the last byte of the
	// field, and the count includes the compression-ID byte.
	dst := make([]byte, 8)
	putDString(dst, "AB")
	if !bytes.Equal(dst, []byte{8, 'A', 'B', 0, 0, 0, 0, 3}) {
		t.Errorf("putDString(%q) = % x", "AB", dst)
	}

	// 2.1.3: "A zero length string shall be recorded by setting the entire
	// dstring field to all zeros" — including the length byte, which is why
	// this differs from genisoimage's output.
	putDString(dst, "")
	if !bytes.Equal(dst, make([]byte, 8)) {
		t.Errorf("putDString(\"\") = % x, want all zeros", dst)
	}
}

// TestFileIdentDescLength checks the padding rule of ECMA-167 4/14.4.9,
// "4 x ip((L_FI+L_IU+38+3)/4) - (L_FI+L_IU+38) bytes", and the parent
// descriptor of 4/8.6, which has no File Identifier at all.
func TestFileIdentDescLength(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"", 40},      // 38 + 0, padded to 40
		{"a", 40},     // 38 + 2, padded to 40
		{"abc", 44},   // 38 + 4, padded to 44
		{"abcde", 44}, // 38 + 6, padded to 44
	}
	for _, tc := range cases {
		got, err := udfFIDLen(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("udfFIDLen(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
	// ECMA-167 4/14.4.4 makes Length of File Identifier a Uint8, so a name
	// that needs more than 255 bytes in CS0 has to be rejected rather than
	// truncated: a truncated name could collide with a sibling.
	if _, err := udfFIDLen(strings.Repeat("x", 255)); err == nil {
		t.Error("a 255-character name should overflow the 8-bit L_FI field")
	}
}

/**************** an independent reader ****************/

// udfImage is a minimal UDF reader built from the ECMA-167 field tables, used
// to check the writer's output without going through the writer's own code.
type udfImage struct {
	data           []byte
	partitionStart uint32
	rootEntry      uint32 // partition-relative block of the root File Entry
}

func (u *udfImage) sector(n uint32) []byte {
	off := int(n) * LogicalSectorSize
	if off+LogicalSectorSize > len(u.data) {
		return nil
	}
	return u.data[off : off+LogicalSectorSize]
}

func (u *udfImage) block(n uint32) []byte { return u.sector(u.partitionStart + n) }

// checkTag verifies a descriptor tag the way a reader would: identifier,
// checksum, CRC and self-location.
func checkTag(t *testing.T, what string, desc []byte, wantID uint16, wantLoc uint32) {
	t.Helper()
	if got := binary.LittleEndian.Uint16(desc[0:2]); got != wantID {
		t.Fatalf("%s: tag identifier %d, want %d", what, got, wantID)
	}
	var sum byte
	for i := 0; i < 16; i++ {
		if i != 4 {
			sum += desc[i]
		}
	}
	if sum != desc[4] {
		t.Errorf("%s: tag checksum %#02x, computed %#02x", what, desc[4], sum)
	}
	crcLen := int(binary.LittleEndian.Uint16(desc[10:12]))
	if 16+crcLen > len(desc) {
		t.Fatalf("%s: descriptor CRC length %d runs past the descriptor", what, crcLen)
	}
	if got, want := binary.LittleEndian.Uint16(desc[8:10]), crcITUT(desc[16:16+crcLen]); got != want {
		t.Errorf("%s: descriptor CRC %#04x, computed %#04x over %d bytes", what, got, want, crcLen)
	}
	if got := binary.LittleEndian.Uint32(desc[12:16]); got != wantLoc {
		t.Errorf("%s: tag location %d, want %d", what, got, wantLoc)
	}
}

// openUDF walks the volume structures the way a reader does and returns a
// handle positioned at the root directory.
func openUDF(t *testing.T, data []byte) *udfImage {
	t.Helper()
	u := &udfImage{data: data}

	// ECMA-167 2/9.1: the Volume Recognition Sequence must follow the
	// ECMA-119 descriptors with no gap, or 2/8.3.1 Note 1 truncates it.
	var ids []string
	for lba := uint32(16); ; lba++ {
		s := u.sector(lba)
		if s == nil {
			break
		}
		id := string(s[1:6])
		if id != "CD001" && id != "BEA01" && id != "NSR02" && id != "NSR03" && id != "TEA01" {
			break
		}
		ids = append(ids, id)
	}
	joined := strings.Join(ids, " ")
	if !strings.Contains(joined, "BEA01 NSR02 TEA01") {
		t.Fatalf("volume recognition sequence is %q, want it to contain BEA01 NSR02 TEA01", joined)
	}

	// ECMA-167 3/10.2: the anchor at sector 256 names both Volume Descriptor
	// Sequences.
	avdp := u.sector(udfAnchorSector)
	if avdp == nil {
		t.Fatal("no sector 256; the image is too short to hold an anchor")
	}
	checkTag(t, "anchor at 256", avdp, udfTagAnchorVolumeDescPtr, udfAnchorSector)
	mainLen := binary.LittleEndian.Uint32(avdp[16:20])
	mainLoc := binary.LittleEndian.Uint32(avdp[20:24])
	reserveLoc := binary.LittleEndian.Uint32(avdp[28:32])
	// UDF 1.02 2.2.3.1 requires each sequence extent to be at least 16
	// logical blocks.
	if mainLen < 16*LogicalSectorSize {
		t.Errorf("main volume descriptor sequence is %d bytes, want at least %d",
			mainLen, 16*LogicalSectorSize)
	}
	if mainLoc == reserveLoc {
		t.Error("the main and reserve volume descriptor sequences are the same extent")
	}

	// Both sequences must be independently valid: every descriptor is
	// self-locating (3/7.2.8), so the reserve copy cannot be a memcpy.
	for _, base := range []uint32{mainLoc, reserveLoc} {
		u.scanVDS(t, base, mainLen/LogicalSectorSize)
	}
	u.partitionStart = u.scanVDS(t, mainLoc, mainLen/LogicalSectorSize)

	// ECMA-167 4/14.1: the File Set Descriptor is the first block of the
	// partition, and its tag location is partition-relative (4/7.1).
	fsd := u.block(0)
	checkTag(t, "file set descriptor", fsd, udfTagFileSetDesc, 0)
	u.rootEntry = binary.LittleEndian.Uint32(fsd[404:408])
	return u
}

// scanVDS walks one Volume Descriptor Sequence, checks every descriptor it
// finds and returns the Partition Starting Location.
func (u *udfImage) scanVDS(t *testing.T, base, sectors uint32) uint32 {
	t.Helper()
	var partitionStart uint32
	seen := map[uint16]bool{}
	for i := uint32(0); i < sectors; i++ {
		s := u.sector(base + i)
		id := binary.LittleEndian.Uint16(s[0:2])
		if id == 0 {
			continue
		}
		checkTag(t, fmt.Sprintf("descriptor %d at sector %d", id, base+i), s, id, base+i)
		seen[id] = true
		if id == udfTagPartitionDesc {
			partitionStart = binary.LittleEndian.Uint32(s[188:192])
		}
		if id == udfTagTerminatingDesc {
			break
		}
	}
	for _, want := range []uint16{
		udfTagPrimaryVolumeDesc, udfTagImplUseVolumeDesc, udfTagPartitionDesc,
		udfTagLogicalVolumeDesc, udfTagUnallocatedSpaceDesc, udfTagTerminatingDesc,
	} {
		if !seen[want] {
			t.Errorf("volume descriptor sequence at %d has no descriptor of type %d", base, want)
		}
	}
	return partitionStart
}

// udfEntryInfo is what the reader learns about one file or directory.
type udfEntryInfo struct {
	path    string
	isDir   bool
	size    uint64
	extents []uint32 // absolute Logical Sector Numbers, one per allocation descriptor
}

// walk reads a File Entry and, if it is a directory, its File Identifier
// Descriptors, recursively.
func (u *udfImage) walk(t *testing.T, prefix string, entryBlock uint32, out *[]udfEntryInfo) {
	t.Helper()
	fe := u.block(entryBlock)
	if fe == nil {
		t.Fatalf("%s: File Entry block %d is past the end of the image", prefix, entryBlock)
	}
	checkTag(t, prefix+" file entry", fe, udfTagFileEntry, entryBlock)

	fileType := fe[27]
	size := binary.LittleEndian.Uint64(fe[56:64])
	lad := binary.LittleEndian.Uint32(fe[172:176])
	if lad%8 != 0 {
		t.Fatalf("%s: L_AD is %d, not a whole number of short_ads", prefix, lad)
	}

	info := udfEntryInfo{path: prefix, isDir: fileType == udfFileTypeDirectory, size: size}
	var data []byte
	for off := uint32(udfFileEntryAllocDescOffset); off < udfFileEntryAllocDescOffset+lad; off += 8 {
		length := binary.LittleEndian.Uint32(fe[off : off+4])
		// ECMA-167 4/14.14.1.1: the top two bits are the extent type; type 0
		// means "recorded and allocated".
		if length>>30 != 0 {
			t.Errorf("%s: allocation descriptor has extent type %d, want 0", prefix, length>>30)
		}
		length &= 0x3FFFFFFF
		pos := binary.LittleEndian.Uint32(fe[off+4 : off+8])
		info.extents = append(info.extents, u.partitionStart+pos)
		start := int(u.partitionStart+pos) * LogicalSectorSize
		data = append(data, u.data[start:start+int(length)]...)
	}
	if uint64(len(data)) != size {
		t.Errorf("%s: allocation descriptors cover %d bytes, Information Length says %d",
			prefix, len(data), size)
	}
	*out = append(*out, info)
	if !info.isDir {
		return
	}

	// ECMA-167 4/8.6 and UDF 1.02 3.3.1: a directory's data is a sequence of
	// File Identifier Descriptors, the first of which identifies the parent.
	first := true
	for off := 0; off < len(data); {
		fid := data[off:]
		lenFI := int(fid[19])
		lenIU := int(binary.LittleEndian.Uint16(fid[36:38]))
		total := (38 + lenIU + lenFI + 3) &^ 3
		// The tag location is the block holding the descriptor's first byte
		// (3/7.2.8), counted from the start of the directory's data.
		wantLoc := entryBlock + 1 + uint32(off/LogicalSectorSize)
		checkTag(t, fmt.Sprintf("%s: FID at offset %d", prefix, off), fid[:total],
			udfTagFileIdentDesc, wantLoc)

		chars := fid[18]
		child := binary.LittleEndian.Uint32(fid[24:28])
		if first {
			if chars&udfFileCharParent == 0 {
				t.Errorf("%s: the first File Identifier Descriptor is not the parent "+
					"(UDF 1.02 3.3.1 requires it to be)", prefix)
			}
			first = false
		} else {
			if chars&udfFileCharParent != 0 {
				t.Errorf("%s: a second parent File Identifier Descriptor at offset %d", prefix, off)
			}
			name := decodeCS0(t, fid[38+lenIU:38+lenIU+lenFI])
			u.walk(t, prefix+"/"+name, child, out)
		}
		off += total
	}
}

// decodeCS0 reverses the OSTA Compressed Unicode encoding of UDF 1.02 2.1.1.
func decodeCS0(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) == 0 {
		return ""
	}
	switch b[0] {
	case 8:
		return string(b[1:])
	case 16:
		var sb strings.Builder
		for i := 1; i+1 < len(b); i += 2 {
			sb.WriteRune(rune(uint16(b[i])<<8 | uint16(b[i+1])))
		}
		return sb.String()
	default:
		t.Fatalf("unknown OSTA compression ID %d", b[0])
		return ""
	}
}

/**************** end-to-end structure ****************/

// udfSampleTree writes a tree whose names deliberately do *not* survive
// ECMA-119 mangling, so that a test comparing UDF names against the input
// cannot accidentally be reading the ISO 9660 side.
func udfSampleTree(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"readme.txt":                    "hello udf\n",
		"a long file name with spaces":  "spaces are fine in UDF and not in ECMA-119\n",
		"sub/nested-file.dat":           strings.Repeat("nested ", 500),
		"sub/deeper/three.levels.down":  "multiple dots\n",
		"sub/deeper/empty":              "",
		"crosses/a_sector_boundary.bin": strings.Repeat("A", 5000),
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, files
}

func buildUDFImage(t *testing.T, src string, opts *Options) []byte {
	t.Helper()
	b := New(opts)
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestUDFStructure walks a written image the way a reader would and checks
// that every descriptor is well formed, every name comes back unmangled, and
// every file's bytes are where its allocation descriptors say they are.
func TestUDFStructure(t *testing.T) {
	src, files := udfSampleTree(t)
	data := buildUDFImage(t, src, &Options{
		VolumeID:  "GOWIM_UDF",
		Level:     Level3,
		UDF:       true,
		Timestamp: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	})

	// The whole reason the layout changes when UDF is enabled: sectors up to
	// and including 256 have to be free for UDF's own structures, so the ISO
	// 9660 path tables cannot sit at LBA 18 as they do in a plain image.
	u := openUDF(t, data)
	if u.partitionStart <= udfAnchorSector {
		t.Errorf("partition starts at %d, which is not past the anchor at %d",
			u.partitionStart, udfAnchorSector)
	}

	var got []udfEntryInfo
	u.walk(t, "", u.rootEntry, &got)

	byPath := map[string]udfEntryInfo{}
	for _, e := range got {
		byPath[e.path] = e
	}
	for name, content := range files {
		e, ok := byPath["/"+name]
		if !ok {
			var have []string
			for _, e := range got {
				have = append(have, e.path)
			}
			sort.Strings(have)
			t.Fatalf("UDF does not contain %q; it has:\n  %s", name, strings.Join(have, "\n  "))
		}
		if e.size != uint64(len(content)) {
			t.Errorf("%s: Information Length %d, want %d", name, e.size, len(content))
		}
		if len(content) == 0 {
			continue
		}
		// Read the file the way a reader would: straight from the extent the
		// allocation descriptor names.
		off := int(e.extents[0]) * LogicalSectorSize
		if got := string(data[off : off+len(content)]); got != content {
			t.Errorf("%s: the bytes at its UDF extent are not its contents", name)
		}
	}
}

// TestUDFSharesISOExtents is the core correctness property of the bridge: a
// file's data is written once, and the ECMA-119 Directory Record and the UDF
// File Entry are two descriptions of the same bytes.
//
// It is checked by parsing both filesystems out of the same image with two
// independent readers — the UDF one above, and a small ECMA-119 directory
// walker here — and comparing the extents they arrive at. This is the
// invariant genisoimage gets by construction, since write_udf_file_entries
// reads de->isorec.extent.
func TestUDFSharesISOExtents(t *testing.T) {
	src, _ := udfSampleTree(t)
	data := buildUDFImage(t, src, &Options{VolumeID: "GOWIM_UDF", Level: Level3, UDF: true})

	u := openUDF(t, data)
	var entries []udfEntryInfo
	u.walk(t, "", u.rootEntry, &entries)

	// Index the UDF view by file size and first extent. Names differ between
	// the two filesystems (ECMA-119 mangles them), so the join is on the
	// data, which is exactly what is supposed to be shared.
	udfExtents := map[uint32]uint64{}
	for _, e := range entries {
		if !e.isDir && e.size > 0 {
			udfExtents[e.extents[0]] = e.size
		}
	}
	if len(udfExtents) == 0 {
		t.Fatal("the UDF walk found no files")
	}

	isoExtents := isoFileExtents(t, data)
	if len(isoExtents) != len(udfExtents) {
		t.Fatalf("ISO 9660 describes %d non-empty files, UDF describes %d", len(isoExtents), len(udfExtents))
	}
	for extent, size := range isoExtents {
		udfSize, ok := udfExtents[extent]
		if !ok {
			t.Errorf("ISO 9660 has a file at extent %d that UDF does not describe", extent)
			continue
		}
		if udfSize != size {
			t.Errorf("extent %d: ISO 9660 says %d bytes, UDF says %d", extent, size, udfSize)
		}
	}
}

// isoFileExtents walks the ECMA-119 directory hierarchy from the Primary
// Volume Descriptor and returns each non-empty file's first extent and total
// size. Multi-extent files (9.1.6 bit 7) are folded back into one entry.
func isoFileExtents(t *testing.T, data []byte) map[uint32]uint64 {
	t.Helper()
	pvd := data[16*LogicalSectorSize : 17*LogicalSectorSize]
	if string(pvd[1:6]) != "CD001" || pvd[0] != 1 {
		t.Fatal("no Primary Volume Descriptor at sector 16")
	}
	out := map[uint32]uint64{}
	// 8.4.18 embeds a complete Directory Record for the Root Directory.
	root := pvd[156:190]
	var visit func(extent, length uint32)
	visit = func(extent, length uint32) {
		dir := data[int(extent)*LogicalSectorSize : (int(extent)*LogicalSectorSize)+int(length)]
		var pending uint32 // first extent of a multi-extent file in progress
		var pendingSize uint64
		for off := 0; off < len(dir); {
			lenDR := int(dir[off])
			if lenDR == 0 {
				// 6.8.1.1: the rest of this Logical Sector is unused.
				off = (off/LogicalSectorSize + 1) * LogicalSectorSize
				continue
			}
			rec := dir[off : off+lenDR]
			off += lenDR
			lenFI := int(rec[32])
			id := string(rec[33 : 33+lenFI])
			if id == "\x00" || id == "\x01" {
				continue // "." and ".." (6.8.2.2)
			}
			ext := binary.LittleEndian.Uint32(rec[2:6])
			size := binary.LittleEndian.Uint32(rec[10:14])
			if rec[25]&dirFlag != 0 {
				visit(ext, size)
				continue
			}
			if pending == 0 {
				pending = ext
			}
			pendingSize += uint64(size)
			if rec[25]&multiExtentFlag == 0 {
				if pendingSize > 0 {
					out[pending] = pendingSize
				}
				pending, pendingSize = 0, 0
			}
		}
	}
	visit(binary.LittleEndian.Uint32(root[2:6]), binary.LittleEndian.Uint32(root[10:14]))
	return out
}

// TestUDFLargeFileAllocationDescriptors checks the split UDF imposes on a
// large file, without materialising one.
//
// ECMA-167 4/14.14.1.1 gives a short_ad's Extent Length 30 bits with a 2-bit
// extent type on top, and UDF 1.02 2.3.10 requires every extent but the last
// to be a whole number of Logical Blocks, so the chunk size is 2^30 rounded
// down to a block boundary. A 7 578 075 168-byte file — the real
// sources/install.wim from Win11_25H2_English_x64_v2.iso — needs eight
// descriptors, which is what that image actually records.
func TestUDFLargeFileAllocationDescriptors(t *testing.T) {
	const installWimSize = 7578075168

	l := &layout{b: New(&Options{UDF: true}), udf: &udfLayout{}}
	s := make([]byte, LogicalSectorSize)
	if err := l.setFileEntry(s, 100, 1000, installWimSize, time.Time{}, false, 1, 100); err != nil {
		t.Fatal(err)
	}

	lad := binary.LittleEndian.Uint32(s[172:176])
	if got, want := lad/8, uint32(8); got != want {
		t.Errorf("a %d-byte file used %d allocation descriptors, want %d",
			installWimSize, got, want)
	}
	if got := binary.LittleEndian.Uint64(s[56:64]); got != installWimSize {
		t.Errorf("Information Length is %d, want %d", got, installWimSize)
	}

	var total uint64
	block := uint32(1000)
	for off := uint32(udfFileEntryAllocDescOffset); off < udfFileEntryAllocDescOffset+lad; off += 8 {
		length := binary.LittleEndian.Uint32(s[off : off+4])
		if length>>30 != 0 {
			t.Errorf("allocation descriptor at %d has extent type %d, want 0", off, length>>30)
		}
		length &= 0x3FFFFFFF
		if pos := binary.LittleEndian.Uint32(s[off+4 : off+8]); pos != block {
			t.Errorf("allocation descriptor at %d starts at block %d, want %d", off, pos, block)
		}
		if next := off + 8; next < udfFileEntryAllocDescOffset+lad && length != udfMaxExtentLength {
			t.Errorf("non-final extent is %d bytes, want %d", length, udfMaxExtentLength)
		}
		block += sectorsFor(uint64(length))
		total += uint64(length)
	}
	if total != installWimSize {
		t.Errorf("the allocation descriptors cover %d bytes, want %d", total, installWimSize)
	}
}

// TestLargeFilesUDFOnly checks the representation Microsoft actually ships: a
// file too large for one ECMA-119 File Section gets no Directory Record at
// all, its data is still written, and UDF describes it in full.
//
// MaxSectionSize stands in for the 4 GiB ceiling so that the test costs a few
// kilobytes rather than several gigabytes; the code path is the same one a
// real 7 GiB install.wim takes.
func TestLargeFilesUDFOnly(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("B", 20000)
	small := "small\n"
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte(small), 0o644); err != nil {
		t.Fatal(err)
	}

	data := buildUDFImage(t, dir, &Options{
		VolumeID:          "GOWIM_UDF",
		UDF:               true,
		LargeFilesUDFOnly: true,
		MaxSectionSize:    4096,
	})

	// The ISO 9660 side sees only the small file, exactly as
	// Win11_25H2_English_x64_v2.iso's 112-byte root directory sees only
	// README.TXT.
	isoExtents := isoFileExtents(t, data)
	if len(isoExtents) != 1 {
		t.Errorf("ISO 9660 describes %d files, want 1 (the big one should be UDF-only)", len(isoExtents))
	}
	for _, size := range isoExtents {
		if size != uint64(len(small)) {
			t.Errorf("the one ISO 9660 file is %d bytes, want %d", size, len(small))
		}
	}

	// UDF sees both, and the big one's data is intact.
	u := openUDF(t, data)
	var entries []udfEntryInfo
	u.walk(t, "", u.rootEntry, &entries)
	var found bool
	for _, e := range entries {
		if e.path != "/big.bin" {
			continue
		}
		found = true
		if e.size != uint64(len(big)) {
			t.Errorf("/big.bin: Information Length %d, want %d", e.size, len(big))
		}
		off := int(e.extents[0]) * LogicalSectorSize
		if got := string(data[off : off+len(big)]); got != big {
			t.Error("/big.bin: the bytes at its UDF extent are not its contents")
		}
	}
	if !found {
		t.Error("UDF does not describe /big.bin")
	}

	// Level 1 is the default, and a UDF-only file must not trip the
	// multi-extent check that Levels 1 and 2 impose (ECMA-119 10.1, 10.2):
	// it has no Directory Record, so it has no Directory Record to carry the
	// Multi-Extent flag. The image above was built at Level 1 for exactly
	// this reason; check the same tree is rejected without the option.
	b := New(&Options{UDF: true, MaxSectionSize: 4096})
	if err := b.AddFile("big.bin", MemSource(make([]byte, 20000))); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo(&bytes.Buffer{}); err == nil {
		t.Error("a file needing several File Sections should be rejected at Level 1 " +
			"without LargeFilesUDFOnly")
	}
}

// TestLargeFilesUDFOnlyRequiresUDF checks that the option is rejected rather
// than silently dropping a file out of both filesystems.
func TestLargeFilesUDFOnlyRequiresUDF(t *testing.T) {
	b := New(&Options{LargeFilesUDFOnly: true, MaxSectionSize: 4096})
	if err := b.AddFile("big.bin", MemSource(make([]byte, 9000))); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo(&bytes.Buffer{}); err == nil {
		t.Error("LargeFilesUDFOnly without UDF should be rejected")
	}
}

// TestUDFPadIsAnchors checks that the run-out padding is filled with copies of
// the Anchor Volume Descriptor Pointer rather than zeros, each carrying its
// own sector number, which is what genisoimage's udf_padend_avdp_write does
// and what measurement on nano11go_test10.iso shows (151 consecutive anchors).
func TestUDFPadIsAnchors(t *testing.T) {
	src, _ := udfSampleTree(t)
	data := buildUDFImage(t, src, &Options{VolumeID: "GOWIM_UDF", UDF: true, PadSectors: 8})
	u := openUDF(t, data)

	total := uint32(len(data) / LogicalSectorSize)
	for lba := total - 8; lba < total; lba++ {
		s := u.sector(lba)
		checkTag(t, fmt.Sprintf("trailing anchor at %d", lba), s, udfTagAnchorVolumeDescPtr, lba)
	}
	// UDF 1.02 2.2.3 requires an anchor at the last recorded sector as one of
	// its two mandatory locations.
	if id := binary.LittleEndian.Uint16(u.sector(total - 1)[0:2]); id != udfTagAnchorVolumeDescPtr {
		t.Errorf("the last sector holds descriptor type %d, want an anchor", id)
	}
}

/**************** external tools ****************/

// udfinfoPath finds udftools' udfinfo, which is the only tool on hand that
// reports UDF revision, integrity state and the volume's sector map.
//
// It is not installed by default on this machine; the tests that need it say
// so rather than passing silently. GOWIM_UDFINFO can point at a copy
// extracted from the udftools package without installing it.
func udfinfoPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("GOWIM_UDFINFO"); p != "" {
		return p
	}
	return haveTool(t, "udfinfo")
}

// TestUDFInfoAcceptsImage validates the image with udftools' udfinfo, an
// implementation with no shared lineage with either this package or
// genisoimage.
func TestUDFInfoAcceptsImage(t *testing.T) {
	udfinfo := udfinfoPath(t)
	if udfinfo == "" {
		t.Skip("udfinfo not installed and GOWIM_UDFINFO unset; cannot externally validate UDF " +
			"(install udftools, or extract udfinfo from the .deb and point GOWIM_UDFINFO at it)")
	}
	src, _ := udfSampleTree(t)
	data := buildUDFImage(t, src, &Options{VolumeID: "GOWIM_UDF", UDF: true, PadSectors: 150})
	path := filepath.Join(t.TempDir(), "udf.iso")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(udfinfo, path).CombinedOutput()
	if err != nil {
		t.Fatalf("udfinfo failed: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{
		"label=GOWIM_UDF",
		"udfrev=1.02",
		"udfwriterev=1.02",
		"integrity=closed",
		"accesstype=readonly",
		"blocksize=2048",
		"impid=*gowim",
		"type=ANCHOR",
		"type=PSPACE",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("udfinfo output missing %q; full output:\n%s", want, text)
		}
	}
	// udfinfo prints the number of files and directories from the Logical
	// Volume Integrity Descriptor's implementation use area (UDF 1.02
	// 2.2.6.4). The sample tree has six files in four directories plus the
	// root.
	for _, want := range []string{"numfiles=6", "numdirs=4"} {
		if !strings.Contains(text, want) {
			t.Errorf("udfinfo output missing %q; full output:\n%s", want, text)
		}
	}
}

// Test7zReadsUDFTree checks the image with 7z, which prefers the UDF view of a
// bridge volume over the ISO 9660 one — so an extraction that comes back with
// the original long, lower-case, space-containing names came through UDF and
// not through ECMA-119.
func Test7zReadsUDFTree(t *testing.T) {
	if haveTool(t, "7z") == "" {
		t.Skip("7z not installed; cannot cross-validate the UDF filesystem")
	}
	src, files := udfSampleTree(t)
	data := buildUDFImage(t, src, &Options{VolumeID: "GOWIM_UDF", UDF: true})
	dir := t.TempDir()
	path := filepath.Join(dir, "udf.iso")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("7z", "l", "-slt", path).CombinedOutput()
	if err != nil {
		t.Fatalf("7z l failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Type = Udf") {
		t.Fatalf("7z did not identify the image as UDF:\n%s", out)
	}

	extracted := filepath.Join(dir, "x")
	if out, err := exec.Command("7z", "x", "-y", "-o"+extracted, path).CombinedOutput(); err != nil {
		t.Fatalf("7z x failed: %v\n%s", err, out)
	}
	for name, content := range files {
		got, err := os.ReadFile(filepath.Join(extracted, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("7z did not extract %q through UDF: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s: extracted %d bytes, want %d", name, len(got), len(content))
		}
	}
}
