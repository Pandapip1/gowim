package iso

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// bootHybridTemplate loads the real a1ive-GRUB boot_hybrid.img this project
// ships against (testdata/boot_hybrid.img, built with -DHYBRID_BOOT --
// see contrib/grub/build-grub.sh in nano11-go), rather than a synthetic
// stand-in, so the patch offset is checked against real compiled bytes.
func bootHybridTemplate(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/boot_hybrid.img")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != mbrSize {
		t.Fatalf("testdata/boot_hybrid.img is %d bytes, want %d", len(b), mbrSize)
	}
	return b
}

// TestLegacyBIOSMBR checks the GRUB2 MBR patch against libisofs's own
// --grub2-mbr algorithm (system_area.c's "Patch MBR for GRUB2" code) and
// against the real boot_hybrid.img template's own compiled bytes.
func TestLegacyBIOSMBR(t *testing.T) {
	template := bootHybridTemplate(t)
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:      "GOWIM_BIOSHYBRID",
		Level:         Level3,
		BootEntries:   windowsBootEntries(),
		LegacyBIOSMBR: template,
	})

	if len(img) < LogicalSectorSize {
		t.Fatalf("image is only %d bytes", len(img))
	}
	mbr := img[:mbrSize]

	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Fatalf("MBR signature is %02x %02x, want 55 AA", mbr[510], mbr[511])
	}

	// Everything outside the 8-byte patch window must be the template's own
	// bytes, untouched: this package does not invent bootstrap code.
	if !bytes.Equal(mbr[:mbrGrub2LBAPatchOffset], template[:mbrGrub2LBAPatchOffset]) {
		t.Error("bytes before the GRUB2 LBA patch offset were altered from the template")
	}
	afterPatch := mbrGrub2LBAPatchOffset + 8
	if !bytes.Equal(mbr[afterPatch:], template[afterPatch:]) {
		t.Error("bytes after the GRUB2 LBA patch field were altered from the template")
	}

	// Cross-check the patched LBA against what the El Torito catalog itself
	// recorded for the BIOS (X86) entry -- windowsBootEntries()'s first
	// entry, the Initial/Default Entry (Figure 3), not a Section Entry.
	boot := readBoot(t, img)
	bios := boot.entries[1]
	wantLBA := uint64(bios.u32(8))*4 + mbrGrub2LBAPatchSkipSectors

	got := binary.LittleEndian.Uint64(mbr[mbrGrub2LBAPatchOffset : mbrGrub2LBAPatchOffset+8])
	if got != wantLBA {
		t.Errorf("patched LBA is %d, want %d (4x the catalog's Load RBA of %d, plus %d to skip cdboot.img)",
			got, wantLBA, bios.u32(8), mbrGrub2LBAPatchSkipSectors)
	}
}

// TestLegacyBIOSMBRRequiresX86Entry checks that LegacyBIOSMBR without any
// BootPlatformX86 entry is rejected rather than silently patching garbage.
func TestLegacyBIOSMBRRequiresX86Entry(t *testing.T) {
	template := bootHybridTemplate(t)
	src := bootSampleTree(t)
	b := New(&Options{
		VolumeID: "GOWIM_BIOSHYBRID",
		Level:    Level3,
		BootEntries: []BootEntry{
			{ImagePath: "efi/microsoft/boot/efisys_noprompt.bin", Platform: BootPlatformUEFI},
		},
		LegacyBIOSMBR: template,
	})
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, err := b.WriteTo(&buf)
	if err == nil {
		t.Fatal("WriteTo succeeded; want an error since no BootPlatformX86 entry is present")
	}
}

// TestLegacyBIOSMBRRequiresRightSize checks that a template of the wrong
// size is rejected rather than silently mis-offsetting the patch.
func TestLegacyBIOSMBRRequiresRightSize(t *testing.T) {
	src := bootSampleTree(t)
	b := New(&Options{
		VolumeID:      "GOWIM_BIOSHYBRID",
		Level:         Level3,
		BootEntries:   windowsBootEntries(),
		LegacyBIOSMBR: make([]byte, 511),
	})
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, err := b.WriteTo(&buf)
	if err == nil {
		t.Fatal("WriteTo succeeded; want an error since the template is the wrong size")
	}
}

// TestLegacyBIOSMBRWithHybridMBR checks that the two patches coexist: the
// GRUB2 LBA field at 0x1B0 and the EFI System Partition entry at 0x1BE
// occupy disjoint byte ranges within the same sector.
func TestLegacyBIOSMBRWithHybridMBR(t *testing.T) {
	template := bootHybridTemplate(t)
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:      "GOWIM_BOTHHYBRID",
		Level:         Level3,
		BootEntries:   windowsBootEntries(),
		HybridMBR:     true,
		LegacyBIOSMBR: template,
	})
	mbr := img[:mbrSize]

	boot := readBoot(t, img)
	bios := boot.entries[1]
	uefi := boot.entries[3]

	wantLBA := uint64(bios.u32(8))*4 + mbrGrub2LBAPatchSkipSectors
	got := binary.LittleEndian.Uint64(mbr[mbrGrub2LBAPatchOffset : mbrGrub2LBAPatchOffset+8])
	if got != wantLBA {
		t.Errorf("patched GRUB2 LBA is %d, want %d", got, wantLBA)
	}

	p := mbr[mbrPartitionTableOffset : mbrPartitionTableOffset+mbrPartitionEntrySize]
	if p[4] != mbrPartitionTypeEFISystem {
		t.Errorf("partition type is %#x, want %#x (EFI System)", p[4], mbrPartitionTypeEFISystem)
	}
	wantPartLBA := uefi.u32(8) * 4
	gotPartLBA := binary.LittleEndian.Uint32(p[8:12])
	if gotPartLBA != wantPartLBA {
		t.Errorf("EFI partition LBA is %d, want %d", gotPartLBA, wantPartLBA)
	}
}
