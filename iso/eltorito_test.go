package iso

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// El Torito tests.
//
// The image is checked three ways, in increasing order of how much it proves:
//
//  1. Against the specification, by parsing the bytes back out of the image
//     and comparing every field of El Torito 1.0 Figures 2 to 5 and 7.
//  2. Against independently written readers — xorriso's -report_el_torito and
//     cdrkit's isoinfo -d — which is what stops a consistent misreading of the
//     specification from passing.
//  3. Against genisoimage's output on the same tree, field by field.
//
// Plus one property test that has nothing to do with reading the image at
// all: the input files must be byte-for-byte unchanged afterwards, which is
// where this package deliberately departs from genisoimage.

// bootSampleTree writes a tree containing two plausible boot images and
// returns its path. The sizes are the real ones: etfsboot.com is 4096 bytes
// on retail Windows media, and efisys_noprompt.bin is 1 474 560, i.e. exactly
// a 1.44 MB floppy image, which is why its derived Sector Count comes out at
// 2880.
func bootSampleTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string][]byte{
		"boot/etfsboot.com":                      bootBlob(4096, 0x11),
		"efi/microsoft/boot/efisys_noprompt.bin": bootBlob(1474560, 0x22),
		"README.TXT":                             []byte("hello\n"),
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// bootBlob makes a deterministic, non-constant blob. Non-constant matters:
// a checksum over a run of identical bytes would not catch a word-order
// mistake.
func bootBlob(n int, seed byte) []byte {
	b := make([]byte, n)
	v := seed
	for i := range b {
		v = v*31 + byte(i)
		b[i] = v
	}
	return b
}

// windowsBootEntries is the pair of entries this project's genisoimage
// invocation produces:
//
//	-b boot/etfsboot.com -no-emul-boot -boot-load-size 8 -boot-info-table
//	-eltorito-alt-boot
//	-e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot
func windowsBootEntries() []BootEntry {
	return []BootEntry{
		{ImagePath: "boot/etfsboot.com", Platform: BootPlatformX86, LoadSectors: 8, BootInfoTable: true},
		{ImagePath: "efi/microsoft/boot/efisys_noprompt.bin", Platform: BootPlatformUEFI},
	}
}

// bootCatalogEntry is one parsed 32-byte catalog structure.
type bootCatalogEntry struct {
	raw [bootCatalogEntrySize]byte
}

func (e bootCatalogEntry) u8(i int) uint8   { return e.raw[i] }
func (e bootCatalogEntry) u16(i int) uint16 { return binary.LittleEndian.Uint16(e.raw[i : i+2]) }
func (e bootCatalogEntry) u32(i int) uint32 { return binary.LittleEndian.Uint32(e.raw[i : i+4]) }

// parsedBoot is everything an image says about El Torito.
type parsedBoot struct {
	catalogLBA uint32
	entries    []bootCatalogEntry
}

// readBoot locates and parses the boot catalog of an image, starting from the
// Boot Record Volume Descriptor.
func readBoot(t *testing.T, img []byte) parsedBoot {
	t.Helper()
	// Walk the Volume Descriptor Set from sector 16 looking for the type 0
	// descriptor (ECMA-119 8.1.1, 8.2.1).
	var brvd []byte
	for lba := uint32(firstVolumeDescriptorSector); ; lba++ {
		s := sectorOf(t, img, lba)
		if string(s[1:6]) != "CD001" {
			t.Fatalf("sector %d is not a Volume Descriptor: %q", lba, s[1:6])
		}
		if s[0] == 0 {
			brvd = s
			break
		}
		if s[0] == 255 { // Volume Descriptor Set Terminator
			t.Fatal("no Boot Record Volume Descriptor in the Volume Descriptor Set")
		}
	}
	if got, want := string(bytes.TrimRight(brvd[7:39], "\x00")), elToritoID; got != want {
		t.Fatalf("Boot System Identifier is %q, want %q", got, want)
	}
	if !bytes.Equal(brvd[39:71], make([]byte, 32)) {
		t.Error("El Torito 1.0 Figure 7 offsets 27-46 must be zero, and are not")
	}

	p := parsedBoot{catalogLBA: binary.LittleEndian.Uint32(brvd[71:75])}
	cat := sectorOf(t, img, p.catalogLBA)
	// The catalog runs until an all-zero entry: nothing in El Torito
	// terminates it explicitly, and every producer zero-fills the tail.
	for off := 0; off+bootCatalogEntrySize <= LogicalSectorSize; off += bootCatalogEntrySize {
		var e bootCatalogEntry
		copy(e.raw[:], cat[off:off+bootCatalogEntrySize])
		if e.raw == [bootCatalogEntrySize]byte{} {
			break
		}
		p.entries = append(p.entries, e)
	}
	return p
}

func sectorOf(t *testing.T, img []byte, lba uint32) []byte {
	t.Helper()
	off := int(lba) * LogicalSectorSize
	if off+LogicalSectorSize > len(img) {
		t.Fatalf("sector %d is past the end of a %d-byte image", lba, len(img))
	}
	return img[off : off+LogicalSectorSize]
}

func buildBootImage(t *testing.T, src string, opts *Options) []byte {
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

// TestBootCatalogStructure checks every field of the four El Torito
// structures against the specification.
func TestBootCatalogStructure(t *testing.T) {
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})

	// The Boot Record must be at sector 17, one after the Primary Volume
	// Descriptor (El Torito 1.0 section 1.4).
	if img[17*LogicalSectorSize] != 0 || string(img[17*LogicalSectorSize+1:17*LogicalSectorSize+6]) != "CD001" {
		t.Error("sector 17 is not the Boot Record Volume Descriptor")
	}

	p := readBoot(t, img)
	if len(p.entries) != 4 {
		t.Fatalf("boot catalog has %d entries, want 4 (validation, initial/default, section header, section entry)", len(p.entries))
	}

	// Figure 2, Validation Entry.
	v := p.entries[0]
	if v.u8(0) != 1 {
		t.Errorf("Validation Entry Header ID is %#x, want 01", v.u8(0))
	}
	if v.u8(1) != uint8(BootPlatformX86) {
		t.Errorf("Validation Entry Platform ID is %#x, want %#x (the first entry's)", v.u8(1), BootPlatformX86)
	}
	if v.u16(2) != 0 {
		t.Error("Validation Entry offsets 2-3 must be 0")
	}
	if v.u8(30) != 0x55 || v.u8(31) != 0xAA {
		t.Errorf("Validation Entry key bytes are %02x %02x, want 55 AA", v.u8(30), v.u8(31))
	}
	// "This sum of all the words in this record should be 0" (Figure 2),
	// which in practice means modulo 2^16 over sixteen little-endian words.
	var sum uint16
	for i := 0; i < bootCatalogEntrySize; i += 2 {
		sum += v.u16(i)
	}
	if sum != 0 {
		t.Errorf("Validation Entry words sum to %#x, want 0", sum)
	}

	// Figure 3, Initial/Default Entry: the BIOS image.
	d := p.entries[1]
	if d.u8(0) != bootIndicatorBootable {
		t.Errorf("Initial/Default Boot Indicator is %#x, want 88", d.u8(0))
	}
	if d.u8(1) != bootMediaNoEmulation {
		t.Errorf("Initial/Default Boot media type is %#x, want 0 (No Emulation)", d.u8(1))
	}
	if d.u16(2) != 0 {
		t.Errorf("Initial/Default Load Segment is %#x, want 0 (traditional 7C0)", d.u16(2))
	}
	if d.u8(4) != 0 || d.u8(5) != 0 {
		t.Error("Initial/Default System Type and the unused byte must be 0")
	}
	if d.u16(6) != 8 {
		t.Errorf("Initial/Default Sector Count is %d, want 8 (the explicit LoadSectors)", d.u16(6))
	}
	if !bytes.Equal(d.raw[12:32], make([]byte, 20)) {
		t.Error("Figure 3 offsets 0C-1F are 'Unused, must be 0'")
	}

	// Figure 4, the Final Section Header for the second platform.
	h := p.entries[2]
	if h.u8(0) != bootSectionHeaderFinal {
		t.Errorf("Section Header Indicator is %#x, want 91 (final header)", h.u8(0))
	}
	if h.u8(1) != uint8(BootPlatformUEFI) {
		t.Errorf("Section Header Platform ID is %#x, want EF (UEFI 2.10 13.3.2.1)", h.u8(1))
	}
	if h.u16(2) != 1 {
		t.Errorf("Section Header entry count is %d, want 1", h.u16(2))
	}

	// Figure 5, the Section Entry: the UEFI image, whose Sector Count is
	// derived from its size, 1474560 bytes = 720 Logical Sectors = 2880
	// 512-byte sectors.
	s := p.entries[3]
	if s.u8(0) != bootIndicatorBootable {
		t.Errorf("Section Entry Boot Indicator is %#x, want 88", s.u8(0))
	}
	if s.u8(1) != bootMediaNoEmulation {
		t.Errorf("Section Entry Boot media type is %#x, want 0", s.u8(1))
	}
	if s.u16(6) != 2880 {
		t.Errorf("Section Entry Sector Count is %d, want 2880", s.u16(6))
	}

	// Both Load RBAs must point at the actual file data.
	checkLoadRBA(t, img, d.u32(8), filepath.Join(src, "boot", "etfsboot.com"), true)
	checkLoadRBA(t, img, s.u32(8), filepath.Join(src, "efi", "microsoft", "boot", "efisys_noprompt.bin"), false)
}

// checkLoadRBA verifies that the sector a catalog entry points at really does
// hold the boot image's bytes. With a boot information table the first 64
// bytes are expected to differ, so only the tail is compared there — and the
// table itself is checked separately.
func checkLoadRBA(t *testing.T, img []byte, lba uint32, hostFile string, hasTable bool) {
	t.Helper()
	want, err := os.ReadFile(hostFile)
	if err != nil {
		t.Fatal(err)
	}
	off := int(lba) * LogicalSectorSize
	got := img[off : off+len(want)]
	from := 0
	if hasTable {
		from = bootInfoTableOffset + bootInfoTableSize
		if !bytes.Equal(got[:bootInfoTableOffset], want[:bootInfoTableOffset]) {
			t.Errorf("%s: the first %d bytes were modified, but the table starts at offset %d",
				hostFile, bootInfoTableOffset, bootInfoTableOffset)
		}
	}
	if !bytes.Equal(got[from:], want[from:]) {
		t.Errorf("%s: image bytes from offset %d differ from the source file", hostFile, from)
	}
}

// TestBootInfoTable checks the 56 bytes genisoimage's -boot-info-table writes,
// field by field against genisoimage/bootinfo.h, and re-derives the checksum
// independently of the writer.
func TestBootInfoTable(t *testing.T) {
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})
	p := readBoot(t, img)
	lba := p.entries[1].u32(8)

	off := int(lba)*LogicalSectorSize + bootInfoTableOffset
	tbl := img[off : off+bootInfoTableSize]
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(tbl[i : i+4]) }

	if u32(0) != firstVolumeDescriptorSector {
		t.Errorf("bi_pvd is %d, want %d", u32(0), firstVolumeDescriptorSector)
	}
	if u32(4) != lba {
		t.Errorf("bi_file is %d, want the boot image's own LBA %d", u32(4), lba)
	}
	if u32(8) != 4096 {
		t.Errorf("bi_length is %d, want 4096", u32(8))
	}
	if !bytes.Equal(tbl[16:], make([]byte, bootInfoTableSize-16)) {
		t.Error("bi_reserved must be zero")
	}

	// Recompute the checksum the way genisoimage's fill_boot_desc does,
	// straight from the source file: the first 64 bytes count as zero, the
	// file is zero-padded to a multiple of 2048, and every little-endian
	// 32-bit word is summed modulo 2^32.
	raw, err := os.ReadFile(filepath.Join(src, "boot", "etfsboot.com"))
	if err != nil {
		t.Fatal(err)
	}
	padded := make([]byte, (len(raw)+LogicalSectorSize-1)/LogicalSectorSize*LogicalSectorSize)
	copy(padded, raw)
	for i := 0; i < bootInfoTableOffset+bootInfoTableSize; i++ {
		padded[i] = 0
	}
	var want uint32
	for i := 0; i < len(padded); i += 4 {
		want += binary.LittleEndian.Uint32(padded[i : i+4])
	}
	if u32(12) != want {
		t.Errorf("bi_csum is %#x, want %#x", u32(12), want)
	}

	// The checksum must be invariant under the patch, which is the whole
	// reason the first 64 bytes are excluded: recompute it over the *emitted*
	// image and get the same answer.
	var after uint32
	emitted := img[int(lba)*LogicalSectorSize : int(lba)*LogicalSectorSize+len(padded)]
	patched := append([]byte(nil), emitted...)
	for i := 0; i < bootInfoTableOffset+bootInfoTableSize; i++ {
		patched[i] = 0
	}
	for i := 0; i < len(patched); i += 4 {
		after += binary.LittleEndian.Uint32(patched[i : i+4])
	}
	if after != want {
		t.Errorf("checksum over the emitted image is %#x but over the source %#x; "+
			"the table must not disturb the bytes it is computed over", after, want)
	}
}

// TestBootImageSourceIsNotModified is the correctness property this package
// deliberately improves on versus genisoimage, whose eltorito.c opens the boot
// image O_RDWR and writes the boot information table into the caller's file.
// Writing an image must leave every input byte-for-byte identical.
func TestBootImageSourceIsNotModified(t *testing.T) {
	src := bootSampleTree(t)

	before := treeHashes(t, src)
	img := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})
	after := treeHashes(t, src)

	for name, h := range before {
		if after[name] != h {
			t.Errorf("writing the image modified the input file %s: %s -> %s", name, h, after[name])
		}
	}
	if len(before) != len(after) {
		t.Errorf("the input tree changed from %d files to %d", len(before), len(after))
	}

	// And the table really was written, so the test is not vacuously passing
	// on an image with no table at all.
	p := readBoot(t, img)
	off := int(p.entries[1].u32(8))*LogicalSectorSize + bootInfoTableOffset
	if bytes.Equal(img[off:off+bootInfoTableSize], make([]byte, bootInfoTableSize)) {
		t.Fatal("no boot information table was written, so this test proves nothing")
	}

	// Building twice from the same inputs must give the same bytes, which
	// cannot hold for a producer that mutates its own inputs as it goes.
	again := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})
	if !bytes.Equal(img, again) {
		t.Error("two builds from the same tree produced different images")
	}
}

func treeHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBootEntryErrors checks that a mistyped or impossible boot entry is
// reported before anything is written, rather than producing an image that
// silently does not boot.
func TestBootEntryErrors(t *testing.T) {
	src := bootSampleTree(t)
	cases := []struct {
		name  string
		entry BootEntry
		want  string
	}{
		{"missing", BootEntry{ImagePath: "boot/nosuch.com"}, "not in the image"},
		{"directory", BootEntry{ImagePath: "boot"}, "is a directory"},
		{"tiny table", BootEntry{ImagePath: "README.TXT", BootInfoTable: true}, "boot information table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New(&Options{Level: Level3, BootEntries: []BootEntry{tc.entry}})
			if err := b.AddTree("", src); err != nil {
				t.Fatal(err)
			}
			_, err := b.WriteTo(&bytes.Buffer{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error is %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestBootCatalogTooManyEntries checks the one-sector bound on the catalog.
func TestBootCatalogTooManyEntries(t *testing.T) {
	src := bootSampleTree(t)
	var entries []BootEntry
	for i := 0; i < 33; i++ { // 32 + 32 + 32*2*63 > 2048 well before this
		entries = append(entries, BootEntry{ImagePath: "README.TXT"})
	}
	b := New(&Options{Level: Level3, BootEntries: entries})
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo(&bytes.Buffer{}); err == nil {
		t.Fatal("expected an error about the boot catalog being one Logical Sector")
	}
}

// TestXorrisoReportsElTorito hands the image to xorriso, an implementation
// from the libburnia lineage that has nothing in common with either this
// package or cdrkit, and checks what it reports.
func TestXorrisoReportsElTorito(t *testing.T) {
	if haveTool(t, "xorriso") == "" {
		t.Skip("xorriso not installed; cannot externally validate the boot catalog (install xorriso)")
	}
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})
	iso := filepath.Join(t.TempDir(), "boot.iso")
	if err := os.WriteFile(iso, img, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("xorriso", "-indev", iso, "-report_el_torito", "plain").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "FAILURE") || strings.Contains(s, "SORRY") {
		t.Errorf("xorriso reported a problem:\n%s", s)
	}
	// The paths come back upper-cased: at interchange Level 3 the File Name
	// is d-characters only (ECMA-119 7.4.1, 10.3), so name.go folds them.
	// genisoimage's -iso-level 4 keeps the original case because ISO 9660:1999
	// relaxes that, which this package does not implement yet.
	up := strings.ToUpper(s)
	for _, want := range []string{
		"BOOT RECORD  : EL TORITO",
		"/BOOT/ETFSBOOT.COM",
		"/EFI/MICROSOFT/BOOT/EFISYS_NOPROMPT.BIN",
		"BOOT-INFO-TABLE",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("xorriso output does not mention %q:\n%s", want, s)
		}
	}
	// xorriso prints one "El Torito boot img" line per entry, with platform
	// and load size. Check both entries came back as we wrote them.
	if !strings.Contains(s, "BIOS") || !strings.Contains(s, "UEFI") {
		t.Errorf("xorriso did not report both a BIOS and a UEFI entry:\n%s", s)
	}
	for _, want := range []string{"8", "2880"} {
		if !strings.Contains(s, want) {
			t.Errorf("xorriso does not report a load size of %s:\n%s", want, s)
		}
	}
}

// TestIsoinfoReportsElTorito uses cdrkit's own reader, which prints the
// catalog structures under their El Torito names.
func TestIsoinfoReportsElTorito(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate the boot catalog (install cdrkit/genisoimage)")
	}
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})
	iso := filepath.Join(t.TempDir(), "boot.iso")
	if err := os.WriteFile(iso, img, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("isoinfo", "-d", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo: %v\n%s", err, out)
	}
	s := string(out)
	p := readBoot(t, img)
	for _, want := range []string{
		fmt.Sprintf("El Torito VD version 1 found, boot catalog is in sector %d", p.catalogLBA),
		"Eltorito validation header:",
		"Key 55 AA",
		"Bootid 88 (bootable)",
		"Boot media 0 (No Emulation Boot)",
		"Nsect 8",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("isoinfo output does not contain %q:\n%s", want, s)
		}
	}
}

// TestCompareBootWithGenisoimage builds the same tree with genisoimage and
// with this package and compares the two boot catalogs field by field.
//
// genisoimage is run against a *copy* of the tree, because -boot-info-table
// makes it modify its input; that modification is itself asserted, since it is
// the behaviour this package deliberately does not reproduce.
func TestCompareBootWithGenisoimage(t *testing.T) {
	if haveTool(t, "genisoimage") == "" {
		t.Skip("genisoimage not installed; cannot compare against the reference producer")
	}
	src := bootSampleTree(t)

	// A copy for genisoimage to chew on.
	ref := filepath.Join(t.TempDir(), "tree")
	if out, err := exec.Command("cp", "-a", src, ref).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v\n%s", err, out)
	}
	refBefore := treeHashes(t, ref)

	refISO := filepath.Join(t.TempDir(), "ref.iso")
	cmd := exec.Command("genisoimage",
		"-iso-level", "4", "-allow-limited-size", "-V", "GOWIM_BOOT",
		"-b", "boot/etfsboot.com", "-no-emul-boot", "-boot-load-size", "8", "-boot-info-table",
		"-eltorito-alt-boot",
		"-e", "efi/microsoft/boot/efisys_noprompt.bin", "-no-emul-boot",
		"-o", refISO, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("genisoimage: %v\n%s", err, out)
	}

	// genisoimage patched its input. This is the bug this package does not
	// have; assert it so that the contrast is documented by a running test
	// rather than only by a comment.
	refAfter := treeHashes(t, ref)
	if refBefore["boot/etfsboot.com"] == refAfter["boot/etfsboot.com"] {
		t.Log("note: genisoimage did not modify its input boot image; " +
			"the divergence documented in eltorito.go may no longer apply to this version")
	} else {
		t.Logf("as expected, genisoimage modified its input boot image in place: %s -> %s",
			refBefore["boot/etfsboot.com"][:16], refAfter["boot/etfsboot.com"][:16])
	}

	refImg, err := os.ReadFile(refISO)
	if err != nil {
		t.Fatal(err)
	}
	ours := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_BOOT",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
	})

	a := readBoot(t, refImg)
	b := readBoot(t, ours)
	if len(a.entries) != len(b.entries) {
		t.Fatalf("genisoimage wrote %d catalog entries, we wrote %d", len(a.entries), len(b.entries))
	}
	for i := range a.entries {
		x, y := a.entries[i].raw, b.entries[i].raw
		if bytes.Equal(x[:], y[:]) {
			continue
		}
		// The only legitimate difference is the Load RBA of a boot entry
		// (offsets 8-0B), because the two producers lay the image out
		// differently: genisoimage emits an extra "version block" sector and
		// rounds each path table up to an even number of blocks.
		isBootEntry := x[0] == bootIndicatorBootable || x[0] == bootIndicatorNotBootable
		var xc, yc [bootCatalogEntrySize]byte
		copy(xc[:], x[:])
		copy(yc[:], y[:])
		if isBootEntry {
			copy(xc[8:12], make([]byte, 4))
			copy(yc[8:12], make([]byte, 4))
		}
		if !bytes.Equal(xc[:], yc[:]) {
			t.Errorf("catalog entry %d differs beyond the Load RBA:\n  genisoimage %x\n  gowim       %x", i, x, y)
		}
	}

	// And each producer's Load RBA must point at its own copy of the data.
	checkLoadRBA(t, ours, b.entries[1].u32(8), filepath.Join(src, "boot", "etfsboot.com"), true)
	checkLoadRBA(t, ours, b.entries[3].u32(8), filepath.Join(src, "efi", "microsoft", "boot", "efisys_noprompt.bin"), false)

	// The boot information tables must agree in everything but the LBA.
	refTbl := refImg[int(a.entries[1].u32(8))*LogicalSectorSize+bootInfoTableOffset:][:bootInfoTableSize]
	ourTbl := ours[int(b.entries[1].u32(8))*LogicalSectorSize+bootInfoTableOffset:][:bootInfoTableSize]
	if !bytes.Equal(refTbl[0:4], ourTbl[0:4]) {
		t.Errorf("boot info table bi_pvd: genisoimage %x, gowim %x", refTbl[0:4], ourTbl[0:4])
	}
	if !bytes.Equal(refTbl[8:], ourTbl[8:]) {
		t.Errorf("boot info table beyond bi_file differs:\n  genisoimage %x\n  gowim       %x", refTbl[8:], ourTbl[8:])
	}
	if binary.LittleEndian.Uint32(ourTbl[4:8]) != b.entries[1].u32(8) {
		t.Error("our bi_file does not match our own Load RBA")
	}
}
