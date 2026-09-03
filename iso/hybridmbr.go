package iso

import (
	"errors"
	"fmt"
)

// mbrSize is the size of a Master Boot Record: 512 bytes, regardless of the
// image's own Logical Sector size. It always fits within the System Area
// (systemAreaSectors, ECMA-119 6.2.1), which is where an "isohybrid" image
// stamps it.
const mbrSize = 512

// mbrPartitionTableOffset is where the four 16-byte partition entries begin
// (offset 0x1BE), confirmed against utils/isohybrid.c's initialise_mbr,
// which advances to this same offset ("offset 446") before writing entries.
const mbrPartitionTableOffset = 446

// mbrPartitionEntrySize is the size of one partition table entry.
const mbrPartitionEntrySize = 16

// mbrPartitionTypeEFISystem is the legacy-MBR partition type byte for an EFI
// System Partition (UEFI 2.10 section 5.2.1, "Legacy Master Boot Record
// (MBR)"; also utils/isohybrid.c, which writes this same byte at partition
// entry offset 4 in its `mode & EFI` branch). It is distinct from 0xEE, the
// type a GPT disk's own protective MBR uses.
const mbrPartitionTypeEFISystem = 0xEF

// mbrGrub2LBAPatchOffset is where GRUB's boot_hybrid.img (built with
// -DHYBRID_BOOT; see grub-core/boot/i386/pc/boot.S and Options.LegacyBIOSMBR)
// keeps its 8-byte little-endian kernel_sector/kernel_sector_high pair, and
// where xorriso's own --grub2-mbr support patches the same field
// (libisofs/system_area.c: Libisofs_grub2_mbr_patch_poS = 0x1b0).
const mbrGrub2LBAPatchOffset = 0x1b0

// mbrGrub2LBAPatchSkipSectors is added, in 512-byte sectors, on top of the
// *4-converted LBA of the registered BootPlatformX86 El Torito image before
// it is written into mbrGrub2LBAPatchOffset. It accounts for that image
// being cdboot.img (a fixed 2048 bytes = 4 sectors) followed immediately by
// GRUB's diskboot.img/core.img -- the MBR must jump straight into
// diskboot.img, not cdboot.img (which is El Torito CD-emulation-specific and
// meaningless to a raw MBR boot). Matches libisofs's own
// Libisofs_grub2_mbr_patch_offsT = 4.
const mbrGrub2LBAPatchSkipSectors = 4

// writeSystemArea emits the System Area (ECMA-119 6.2.1, sectors 0 to 15):
// a hybrid MBR in the first sector when Options.HybridMBR or
// Options.LegacyBIOSMBR is set, or all zeros otherwise (the standard leaves
// the content unspecified).
func (l *layout) writeSystemArea(w *sectorWriter) error {
	if !l.b.opts.HybridMBR && l.b.opts.LegacyBIOSMBR == nil {
		return w.zeroSectors(systemAreaSectors)
	}
	mbr, err := l.hybridMBR()
	if err != nil {
		return err
	}
	buf := w.sector()
	copy(buf, mbr)
	if err := w.write(buf); err != nil {
		return err
	}
	return w.zeroSectors(systemAreaSectors - 1)
}

// hybridMBR builds the "isohybrid" Master Boot Record that lets this image,
// when written byte-for-byte to a USB stick (dd, GNOME Disks' "Restore Disk
// Image", etc.), also be recognized as a valid MBR-partitioned disk: with an
// EFI System Partition for UEFI (Options.HybridMBR) and/or a patched GRUB
// boot_hybrid.img for legacy BIOS (Options.LegacyBIOSMBR), each pointing at
// the corresponding El Torito boot image already registered in the catalog.
//
// Without this, USB boot enumeration -- which looks for a GPT/MBR partition
// table with an EFI System Partition, or (BIOS/CSM) just runs the MBR sector
// as ordinary x86 code -- has no idea what an El Torito catalog is. Only
// BIOS CD emulation and a virtual CD-ROM (QEMU/OVMF, a real optical drive)
// understand El Torito on their own.
//
// See Options.HybridMBR and Options.LegacyBIOSMBR for the supporting
// citations for each half of this.
func (l *layout) hybridMBR() ([]byte, error) {
	var mbr []byte
	if l.b.opts.LegacyBIOSMBR != nil {
		if len(l.b.opts.LegacyBIOSMBR) != mbrSize {
			return nil, fmt.Errorf("iso: LegacyBIOSMBR must be %d bytes (a raw boot_hybrid.img), got %d",
				mbrSize, len(l.b.opts.LegacyBIOSMBR))
		}
		if l.boot == nil || !l.boot.biosSet {
			return nil, errors.New("iso: internal error: LegacyBIOSMBR requested but no BootPlatformX86 boot entry was resolved")
		}
		mbr = append([]byte(nil), l.b.opts.LegacyBIOSMBR...)
		blk := uint64(l.boot.biosLBA)*4 + mbrGrub2LBAPatchSkipSectors
		lba := mbr[mbrGrub2LBAPatchOffset : mbrGrub2LBAPatchOffset+8]
		for i := range lba {
			lba[i] = byte(blk >> (i * 8))
		}
	} else {
		mbr = make([]byte, mbrSize)
	}

	if l.b.opts.HybridMBR {
		if l.boot == nil || !l.boot.hybridSet {
			return nil, errors.New("iso: internal error: HybridMBR requested but no UEFI boot entry was resolved")
		}
		p := mbr[mbrPartitionTableOffset : mbrPartitionTableOffset+mbrPartitionEntrySize]
		// Offset 0: Boot Indicator. Left 0 (not 0x80/"active"): an EFI System
		// Partition is identified by its partition type alone, per the UEFI
		// 2.10 5.2.1 citation on Options.HybridMBR, and isohybrid.c agrees
		// (mbr[0] = 0x0 in its EFI branch).
		p[0] = 0x00
		// Offsets 1-3 and 5-7: starting/ending CHS, set to the FE FF FF
		// "overflow" convention isohybrid.c uses for its LBA-addressed EFI
		// partition, since the partition lies far beyond any real CHS
		// geometry.
		p[1], p[2], p[3] = 0xFE, 0xFF, 0xFF
		p[4] = mbrPartitionTypeEFISystem
		p[5], p[6], p[7] = 0xFE, 0xFF, 0xFF
		// Offsets 8-11 and 12-15: starting LBA and size, both in 512-byte
		// sectors (little-endian). The image's own extents are numbered in
		// 2048-byte Logical Sectors, hence *4; the Sector Count already is
		// 512-byte units (see BootEntry.LoadSectors), so it is copied as-is,
		// matching isohybrid.c's `efi_lba * 4` / `efi_count`.
		put731(p[8:12], l.boot.hybridLBA*4)
		put731(p[12:16], uint32(l.boot.hybridSectors))
	}

	mbr[510], mbr[511] = 0x55, 0xAA
	return mbr, nil
}
