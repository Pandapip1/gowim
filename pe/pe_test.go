package pe

import (
	"bytes"
	"testing"
)

// buildPE64Fixture hand-constructs a minimal but structurally valid PE32+
// image: DOS header/stub, COFF file header, PE32+ optional header with 16
// data directories, one ".text" section, zero header padding out to
// SizeOfHeaders, one fake WIN_CERTIFICATE entry referenced by the Security
// data directory, and a small trailing overlay (Tail).
func buildPE64Fixture(t *testing.T) *Image {
	t.Helper()

	dosStub := bytes.Repeat([]byte{0xCC}, 16) // 64 + 16 = 0x50 == e_lfanew

	sectionData := bytes.Repeat([]byte{0x90}, 64) // fake .text bytes (NOPs)
	const sectionOff = 0x200                      // == SizeOfHeaders, no gap

	certData := []byte("FAKE-PKCS7-SIGNED-DATA-BLOB-CONTENT") // 36 bytes, not 8-aligned
	certs := []Certificate{{Revision: CertRevision2_0, Type: CertTypePKCSSignedData, Data: certData}}
	certTableLen := EncodedCertificateTableLen(certs)
	const certOff = sectionOff + 64 // == 0x240, already 8-aligned

	dirs := make([]DataDirectory, NumDataDirectories)
	dirs[DirEntrySecurity] = DataDirectory{VirtualAddress: certOff, Size: uint32(certTableLen)}

	var sectionName [8]byte
	copy(sectionName[:], ".text")

	// 64 (DOS header) + 16 (stub) + 4 (PE sig) + 20 (file header)
	// + 240 (PE32+ optional header: 112 fixed + 16*8 directories)
	// + 40 (one section header) = 384 = 0x180; pad to SizeOfHeaders (0x200).
	headerPadding := make([]byte, 0x200-0x180)

	return &Image{
		DOSStub: dosStub,
		FileHeader: FileHeader{
			Machine:         MachineAMD64,
			TimeDateStamp:   0x5F5F5F5F,
			Characteristics: FileExecutableImage | FileLargeAddressAware,
		},
		OptionalHeader: OptionalHeader{
			Magic:                       OptionalHeaderMagicPE32Plus,
			MajorLinkerVersion:          14,
			MinorLinkerVersion:          10,
			SizeOfCode:                  0x1000,
			SizeOfInitializedData:       0x2000,
			AddressOfEntryPoint:         0x1000,
			BaseOfCode:                  0x1000,
			ImageBase:                   0x0000000140000000,
			SectionAlignment:            0x1000,
			FileAlignment:               0x200,
			MajorOperatingSystemVersion: 6,
			MajorImageVersion:           1,
			MajorSubsystemVersion:       6,
			SizeOfImage:                 0x3000,
			SizeOfHeaders:               0x200,
			CheckSum:                    0xDEADBEEF,
			Subsystem:                   1, // IMAGE_SUBSYSTEM_NATIVE (kernel driver)
			SizeOfStackReserve:          0x40000,
			SizeOfStackCommit:           0x2000,
			SizeOfHeapReserve:           0x100000,
			SizeOfHeapCommit:            0x1000,
			DataDirectory:               dirs,
		},
		Sections: []Section{
			{
				Header: SectionHeader{
					Name:             sectionName,
					VirtualSize:      0x1000,
					VirtualAddress:   0x1000,
					SizeOfRawData:    uint32(len(sectionData)),
					PointerToRawData: sectionOff,
					Characteristics:  SectionCodeFlag | SectionMemExecute | SectionMemRead,
				},
				RawData: sectionData,
			},
		},
		Certificates:  certs,
		HeaderPadding: headerPadding,
		Tail:          []byte("OVLY"),
	}
}

func TestImage64RoundTrip(t *testing.T) {
	orig := buildPE64Fixture(t)

	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	got, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	b2, err := got.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (2nd generation): %v", err)
	}
	if !bytes.Equal(b, b2) {
		t.Fatalf("round trip not byte-identical:\n first:  % x\n second: % x", b, b2)
	}

	// Individual field checks.
	if got.OptionalHeader.Magic != OptionalHeaderMagicPE32Plus || !got.OptionalHeader.Is64Bit() {
		t.Fatalf("expected PE32+ magic, got %#04x", got.OptionalHeader.Magic)
	}
	if got.FileHeader.Machine != MachineAMD64 {
		t.Fatalf("Machine = %#04x, want MachineAMD64", got.FileHeader.Machine)
	}
	if got.FileHeader.TimeDateStamp != 0x5F5F5F5F {
		t.Fatalf("TimeDateStamp = %#x, want 0x5F5F5F5F", got.FileHeader.TimeDateStamp)
	}
	if got.OptionalHeader.CheckSum != 0xDEADBEEF {
		t.Fatalf("CheckSum = %#x, want 0xDEADBEEF", got.OptionalHeader.CheckSum)
	}
	if got.OptionalHeader.ImageBase != 0x0000000140000000 {
		t.Fatalf("ImageBase = %#x, want 0x140000000", got.OptionalHeader.ImageBase)
	}
	if got.FileHeader.NumberOfSections != 1 {
		t.Fatalf("NumberOfSections = %d, want 1", got.FileHeader.NumberOfSections)
	}
	if len(got.OptionalHeader.DataDirectory) != NumDataDirectories {
		t.Fatalf("DataDirectory count = %d, want %d", len(got.OptionalHeader.DataDirectory), NumDataDirectories)
	}
	if len(got.Sections) != 1 {
		t.Fatalf("Sections count = %d, want 1", len(got.Sections))
	}
	if name := got.Sections[0].Header.NameString(); name != ".text" {
		t.Fatalf("section name = %q, want %q", name, ".text")
	}
	if !bytes.Equal(got.Sections[0].RawData, sectionDataFixture()) {
		t.Fatalf("section raw data mismatch")
	}
	if !bytes.Equal(got.Tail, []byte("OVLY")) {
		t.Fatalf("Tail = %q, want %q", got.Tail, "OVLY")
	}

	// Locate and read back the fake certificate-table entry via the
	// Security data directory offset/size, independent of Parse's own
	// Certificates field, to prove the raw byte range is externally usable
	// (as a caller handing bytes to cat's PKCS#7 parser would do).
	secDir, ok := got.SecurityDirectory()
	if !ok {
		t.Fatal("expected a Security data directory entry")
	}
	if secDir.VirtualAddress != certOffFixture() {
		t.Fatalf("security dir VirtualAddress = %#x, want %#x", secDir.VirtualAddress, certOffFixture())
	}
	raw := b[secDir.VirtualAddress : secDir.VirtualAddress+secDir.Size]
	rawCerts, err := ParseCertificateTable(raw)
	if err != nil {
		t.Fatalf("ParseCertificateTable on raw security-dir bytes: %v", err)
	}
	if len(rawCerts) != 1 {
		t.Fatalf("parsed %d certificates from raw bytes, want 1", len(rawCerts))
	}
	if rawCerts[0].Revision != CertRevision2_0 || rawCerts[0].Type != CertTypePKCSSignedData {
		t.Fatalf("cert revision/type mismatch: %+v", rawCerts[0])
	}
	if !bytes.Equal(rawCerts[0].Data, []byte("FAKE-PKCS7-SIGNED-DATA-BLOB-CONTENT")) {
		t.Fatalf("cert data mismatch: %q", rawCerts[0].Data)
	}

	// Also check against the Certificates already parsed onto Image.
	if len(got.Certificates) != 1 || !bytes.Equal(got.Certificates[0].Data, rawCerts[0].Data) {
		t.Fatalf("Image.Certificates mismatch: %+v", got.Certificates)
	}
}

func sectionDataFixture() []byte { return bytes.Repeat([]byte{0x90}, 64) }
func certOffFixture() uint32     { return 0x240 }

// buildPE32Fixture hand-constructs a minimal but structurally valid PE32
// (32-bit) image: DOS header/stub, COFF file header, PE32 optional header
// (with BaseOfData and 32-bit ImageBase/stack/heap fields), and one section.
// It carries no certificate table, to exercise the "absent Security
// directory" path.
func buildPE32Fixture(t *testing.T) *Image {
	t.Helper()

	dosStub := bytes.Repeat([]byte{0xAA}, 8) // 64 + 8 = 0x48 == e_lfanew

	sectionData := bytes.Repeat([]byte{0x00}, 32)
	const sectionOff = 0x200

	dirs := make([]DataDirectory, NumDataDirectories) // all zero: no Security entry

	var sectionName [8]byte
	copy(sectionName[:], ".data")

	// 64 (DOS header) + 8 (stub) + 4 (PE sig) + 20 (file header)
	// + 224 (PE32 optional header: 96 fixed + 16*8 directories)
	// + 40 (one section header) = 360 = 0x168; pad to SizeOfHeaders (0x200).
	headerPadding := make([]byte, 0x200-0x168)

	return &Image{
		DOSStub: dosStub,
		FileHeader: FileHeader{
			Machine:         MachineI386,
			TimeDateStamp:   0x11223344,
			Characteristics: FileExecutableImage,
		},
		OptionalHeader: OptionalHeader{
			Magic:                 OptionalHeaderMagicPE32,
			MajorLinkerVersion:    12,
			MinorLinkerVersion:    0,
			SizeOfCode:            0x400,
			SizeOfInitializedData: 0x200,
			AddressOfEntryPoint:   0x1000,
			BaseOfCode:            0x1000,
			BaseOfData:            0x2000,
			ImageBase:             0x00400000,
			SectionAlignment:      0x1000,
			FileAlignment:         0x200,
			SizeOfImage:           0x3000,
			SizeOfHeaders:         0x200,
			Subsystem:             1,
			SizeOfStackReserve:    0x100000,
			SizeOfStackCommit:     0x1000,
			SizeOfHeapReserve:     0x100000,
			SizeOfHeapCommit:      0x1000,
			DataDirectory:         dirs,
		},
		Sections: []Section{
			{
				Header: SectionHeader{
					Name:             sectionName,
					VirtualSize:      0x1000,
					VirtualAddress:   0x2000,
					SizeOfRawData:    uint32(len(sectionData)),
					PointerToRawData: sectionOff,
					Characteristics:  SectionInitializedDataFlag | SectionMemRead | SectionMemWrite,
				},
				RawData: sectionData,
			},
		},
		HeaderPadding: headerPadding,
	}
}

func TestImage32RoundTrip(t *testing.T) {
	orig := buildPE32Fixture(t)

	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	got, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	b2, err := got.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (2nd generation): %v", err)
	}
	if !bytes.Equal(b, b2) {
		t.Fatalf("round trip not byte-identical:\n first:  % x\n second: % x", b, b2)
	}

	if got.OptionalHeader.Magic != OptionalHeaderMagicPE32 || !got.OptionalHeader.Is32Bit() {
		t.Fatalf("expected PE32 magic, got %#04x", got.OptionalHeader.Magic)
	}
	if got.OptionalHeader.BaseOfData != 0x2000 {
		t.Fatalf("BaseOfData = %#x, want 0x2000", got.OptionalHeader.BaseOfData)
	}
	if got.OptionalHeader.ImageBase != 0x00400000 {
		t.Fatalf("ImageBase = %#x, want 0x400000", got.OptionalHeader.ImageBase)
	}
	if got.FileHeader.Machine != MachineI386 {
		t.Fatalf("Machine = %#04x, want MachineI386", got.FileHeader.Machine)
	}
	if secDir, ok := got.SecurityDirectory(); !ok || secDir.Size != 0 {
		t.Fatalf("expected an empty Security data directory entry, got %+v (ok=%v)", secDir, ok)
	}
	if len(got.Certificates) != 0 {
		t.Fatalf("expected no certificates, got %d", len(got.Certificates))
	}
}

func TestParseRejectsBadDOSMagic(t *testing.T) {
	orig := buildPE64Fixture(t)
	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	b[0] = 'X'
	if _, err := Parse(b); err == nil {
		t.Fatal("expected error for corrupted DOS magic")
	}
}

func TestParseRejectsBadPESignature(t *testing.T) {
	orig := buildPE64Fixture(t)
	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	lfanew := le.Uint32(b[0x3C:])
	b[lfanew] = 'X'
	if _, err := Parse(b); err == nil {
		t.Fatal("expected error for corrupted PE signature")
	}
}

func TestCertificateTablePadding(t *testing.T) {
	certs := []Certificate{
		{Revision: CertRevision2_0, Type: CertTypePKCSSignedData, Data: []byte("12345")},            // dwLength=13, pad to 16
		{Revision: CertRevision2_0, Type: CertTypePKCSSignedData, Data: []byte("1234567890123456")}, // dwLength=24, already aligned
	}
	b := AppendCertificateTable(nil, certs)
	if len(b) != EncodedCertificateTableLen(certs) {
		t.Fatalf("encoded length = %d, want %d", len(b), EncodedCertificateTableLen(certs))
	}
	if len(b)%8 != 0 {
		t.Fatalf("certificate table not 8-aligned: %d bytes", len(b))
	}
	got, err := ParseCertificateTable(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d certs, want 2", len(got))
	}
	for i, c := range certs {
		if !bytes.Equal(got[i].Data, c.Data) || got[i].Revision != c.Revision || got[i].Type != c.Type {
			t.Fatalf("cert %d mismatch: got %+v want %+v", i, got[i], c)
		}
	}
}
