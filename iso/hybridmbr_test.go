package iso

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestHybridMBR checks the isohybrid MBR's partition entry against
// utils/isohybrid.c's initialise_mbr (its `mode & EFI` branch) and the UEFI
// 2.10 5.2.1 citations on Options.HybridMBR.
func TestHybridMBR(t *testing.T) {
	src := bootSampleTree(t)
	img := buildBootImage(t, src, &Options{
		VolumeID:    "GOWIM_HYBRID",
		Level:       Level3,
		BootEntries: windowsBootEntries(),
		HybridMBR:   true,
	})

	if len(img) < LogicalSectorSize {
		t.Fatalf("image is only %d bytes", len(img))
	}
	mbr := img[:mbrSize]

	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Fatalf("MBR signature is %02x %02x, want 55 AA", mbr[510], mbr[511])
	}

	p := mbr[mbrPartitionTableOffset : mbrPartitionTableOffset+mbrPartitionEntrySize]
	if p[0] != 0x00 {
		t.Errorf("Boot Indicator is %#x, want 0 (not marked active; UEFI ESP discovery uses the partition type alone)", p[0])
	}
	if p[1] != 0xFE || p[2] != 0xFF || p[3] != 0xFF {
		t.Errorf("starting CHS is %02x %02x %02x, want FE FF FF (LBA-only convention)", p[1], p[2], p[3])
	}
	if p[4] != mbrPartitionTypeEFISystem {
		t.Errorf("partition type is %#x, want %#x (EFI System)", p[4], mbrPartitionTypeEFISystem)
	}
	if p[5] != 0xFE || p[6] != 0xFF || p[7] != 0xFF {
		t.Errorf("ending CHS is %02x %02x %02x, want FE FF FF", p[5], p[6], p[7])
	}

	// Cross-check the partition's LBA/size against what the El Torito boot
	// catalog itself recorded for the UEFI entry, rather than against a
	// second independent computation: the two must describe the same bytes.
	boot := readBoot(t, img)
	// windowsBootEntries()'s second entry (UEFI) becomes the Section Entry,
	// the 4th parsed catalog entry (Validation, Initial/Default, Section
	// Header, Section Entry).
	uefi := boot.entries[3]
	wantLBA := uefi.u32(8) * 4
	wantSectors := uint32(uefi.u16(6))

	gotLBA := binary.LittleEndian.Uint32(p[8:12])
	gotSectors := binary.LittleEndian.Uint32(p[12:16])
	if gotLBA != wantLBA {
		t.Errorf("partition starting LBA is %d, want %d (4x the catalog's Load RBA of %d)", gotLBA, wantLBA, uefi.u32(8))
	}
	if gotSectors != wantSectors {
		t.Errorf("partition sector count is %d, want %d (the catalog's own Sector Count)", gotSectors, wantSectors)
	}
	if gotSectors == 0 {
		t.Error("partition sector count is 0")
	}
}

// TestHybridMBRRequiresUEFIEntry checks that HybridMBR without any
// BootPlatformUEFI entry is rejected rather than silently producing a
// pointless all-zero partition entry.
func TestHybridMBRRequiresUEFIEntry(t *testing.T) {
	src := bootSampleTree(t)
	b := New(&Options{
		VolumeID: "GOWIM_HYBRID",
		Level:    Level3,
		BootEntries: []BootEntry{
			{ImagePath: "boot/etfsboot.com", Platform: BootPlatformX86, LoadSectors: 8},
		},
		HybridMBR: true,
	})
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, err := b.WriteTo(&buf)
	if err == nil {
		t.Fatal("WriteTo succeeded; want an error since no BootPlatformUEFI entry is present")
	}
}
