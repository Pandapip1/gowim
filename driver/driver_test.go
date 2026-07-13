package driver

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf16"

	"github.com/Pandapip1/gowim/cat"
	"github.com/Pandapip1/gowim/pe"
	"github.com/Pandapip1/gowim/wim"
)

// --- synthetic .sys payload (minimal but structurally valid PE32+ image) ---

func buildSysFixture(t *testing.T) []byte {
	t.Helper()

	dosStub := bytes.Repeat([]byte{0xCC}, 16) // 64 + 16 = 0x50 == e_lfanew
	sectionData := bytes.Repeat([]byte{0x90}, 64)
	const sectionOff = 0x200

	dirs := make([]pe.DataDirectory, pe.NumDataDirectories) // no certificate table needed for this test

	var sectionName [8]byte
	copy(sectionName[:], ".text")

	headerPadding := make([]byte, 0x200-0x180)

	img := &pe.Image{
		DOSStub: dosStub,
		FileHeader: pe.FileHeader{
			Machine:         pe.MachineAMD64,
			TimeDateStamp:   0x5F5F5F5F,
			Characteristics: pe.FileExecutableImage | pe.FileLargeAddressAware,
		},
		OptionalHeader: pe.OptionalHeader{
			Magic:                       pe.OptionalHeaderMagicPE32Plus,
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
			CheckSum:                    0,
			Subsystem:                   1, // IMAGE_SUBSYSTEM_NATIVE (kernel driver)
			SizeOfStackReserve:          0x40000,
			SizeOfStackCommit:           0x2000,
			SizeOfHeapReserve:           0x100000,
			SizeOfHeapCommit:            0x1000,
			DataDirectory:               dirs,
		},
		Sections: []pe.Section{
			{
				Header: pe.SectionHeader{
					Name:             sectionName,
					VirtualSize:      0x1000,
					VirtualAddress:   0x1000,
					SizeOfRawData:    uint32(len(sectionData)),
					PointerToRawData: sectionOff,
					Characteristics:  pe.SectionCodeFlag | pe.SectionMemExecute | pe.SectionMemRead,
				},
				RawData: sectionData,
			},
		},
		HeaderPadding: headerPadding,
	}

	b, err := img.AppendTo(nil)
	if err != nil {
		t.Fatalf("pe AppendTo: %v", err)
	}
	return b
}

// --- synthetic catalog, hand-built the same way cat/cat_test.go does ---

func bmpString(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		out[i*2] = byte(c >> 8)
		out[i*2+1] = byte(c)
	}
	return out
}

func stringToUTF16LEWithNUL(s string) []byte {
	u16 := utf16.Encode([]rune(s + "\x00"))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		out[i*2] = byte(c)
		out[i*2+1] = byte(c >> 8)
	}
	return out
}

type testNameValueASN1 struct {
	RefName    asn1.RawValue
	TypeAction int32
	Value      []byte
}

type testDigestInfoASN1 struct {
	DigestAlgorithm pkix.AlgorithmIdentifier
	Digest          []byte
}

type testSpcAttrTypeAndValueASN1 struct {
	Type  asn1.ObjectIdentifier
	Value asn1.RawValue `asn1:"optional"`
}

type testSpcIndirectDataContentASN1 struct {
	Data          testSpcAttrTypeAndValueASN1
	MessageDigest testDigestInfoASN1
}

func buildNameValue(t *testing.T, refname string, value string) []byte {
	t.Helper()
	raw := testNameValueASN1{
		RefName: asn1.RawValue{
			Class: asn1.ClassUniversal,
			Tag:   asn1.TagBMPString,
			Bytes: bmpString(refname),
		},
		TypeAction: 0x10010001,
		Value:      stringToUTF16LEWithNUL(value),
	}
	b, err := asn1.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal name value: %v", err)
	}
	return b
}

func buildIndirectData(t *testing.T, hash []byte) []byte {
	t.Helper()
	idc := testSpcIndirectDataContentASN1{
		Data: testSpcAttrTypeAndValueASN1{Type: cat.OIDCatalogListMember},
		MessageDigest: testDigestInfoASN1{
			DigestAlgorithm: pkix.AlgorithmIdentifier{Algorithm: cat.OIDSHA1},
			Digest:          hash,
		},
	}
	b, err := asn1.Marshal(idc)
	if err != nil {
		t.Fatalf("marshal indirect data content: %v", err)
	}
	return b
}

func buildMember(t *testing.T, tag []byte, fileName string, hash []byte) cat.CatalogMember {
	t.Helper()
	return cat.CatalogMember{
		Tag: tag,
		Attributes: []cat.Attribute{
			{
				Type: cat.OIDCatNameValue,
				Values: []asn1.RawValue{
					{FullBytes: buildNameValue(t, "File", fileName)},
				},
			},
			{
				Type: cat.OIDSpcIndirectDataContent,
				Values: []asn1.RawValue{
					{FullBytes: buildIndirectData(t, hash)},
				},
			},
		},
	}
}

func buildCatalog(t *testing.T, sysBytes, dllBytes []byte) []byte {
	t.Helper()
	sysHash := sha1.Sum(sysBytes)
	dllHash := sha1.Sum(dllBytes)

	ctl := &cat.CertificateTrustList{
		SubjectUsage:     []asn1.ObjectIdentifier{cat.OIDCatalogList},
		ListIdentifier:   []byte{0xaa, 0xbb, 0xcc, 0xdd},
		SequenceNumber:   big.NewInt(1),
		ThisUpdate:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		SubjectAlgorithm: pkix.AlgorithmIdentifier{Algorithm: cat.OIDSHA1},
		Members: []cat.CatalogMember{
			buildMember(t, []byte{0x01, 0x02, 0x03, 0x04}, "driver.sys", sysHash[:]),
			buildMember(t, []byte{0x05, 0x06, 0x07, 0x08}, "helper.dll", dllHash[:]),
		},
	}

	sd := &cat.SignedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{{Algorithm: cat.OIDSHA1}},
		ContentType:      cat.OIDCatalogList,
		CTL:              ctl,
		SignerInfos:      []byte{0x31, 0x00}, // empty SET OF SignerInfo, unsigned test stand-in
	}
	ci := &cat.ContentInfo{ContentType: cat.OIDSignedData, SignedData: sd}

	der, err := ci.AppendTo(nil)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	return der
}

// --- synthetic INF ---

const testINF = `[Version]
Signature="$Windows NT$"
Class=System
ClassGuid={4d36e97d-e325-11ce-bfc1-08002be10318}
Provider=%Mfg%
DriverVer=01/01/2026,1.0.0.0
CatalogFile=contoso.cat

[Manufacturer]
%Mfg%=Contoso,NTamd64

[Contoso.NTamd64]
%DeviceDesc%=Install,ACPI\CONTOSO0001

[Install.NTamd64]
CopyFiles=Install.CopyFiles

[Install.CopyFiles]
driver.sys
helper.dll

[SourceDisksNames]
1 = %DiskDesc%

[SourceDisksFiles]
driver.sys=1
helper.dll=1

[DestinationDirs]
DefaultDestDir=13

[Strings]
Mfg="Contoso, Ltd."
DeviceDesc="Contoso Sample Device"
DiskDesc="Contoso Driver Disk"
`

func buildFS(t *testing.T) (fstest.MapFS, []byte, []byte) {
	t.Helper()
	sysBytes := buildSysFixture(t)
	dllBytes := []byte("this is not really a PE, just DLL-shaped payload bytes")
	catBytes := buildCatalog(t, sysBytes, dllBytes)

	fsys := fstest.MapFS{
		"contoso.inf": {Data: []byte(testINF)},
		"contoso.cat": {Data: catBytes},
		"driver.sys":  {Data: sysBytes},
		"helper.dll":  {Data: dllBytes},
	}
	return fsys, sysBytes, dllBytes
}

func TestLoadPackage(t *testing.T) {
	fsys, _, _ := buildFS(t)

	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if pkg.CatalogName != "contoso.cat" {
		t.Fatalf("CatalogName = %q, want contoso.cat", pkg.CatalogName)
	}
	if pkg.Catalog == nil || pkg.Catalog.CTL == nil {
		t.Fatal("Catalog not loaded")
	}
	if len(pkg.Files) != 2 {
		t.Fatalf("got %d payload files, want 2: %+v", len(pkg.Files), pkg.Files)
	}

	byName := make(map[string]PayloadFile)
	for _, f := range pkg.Files {
		byName[f.DestName] = f
	}
	sysFile, ok := byName["driver.sys"]
	if !ok {
		t.Fatal("driver.sys not enumerated")
	}
	if sysFile.SourcePath != "driver.sys" {
		t.Fatalf("driver.sys SourcePath = %q, want driver.sys", sysFile.SourcePath)
	}
	if sysFile.DirID != DirIDDriverStore {
		t.Fatalf("driver.sys DirID = %v, want %v", sysFile.DirID, DirIDDriverStore)
	}
	if _, ok := byName["helper.dll"]; !ok {
		t.Fatal("helper.dll not enumerated")
	}
}

func TestVerifyOK(t *testing.T) {
	fsys, _, _ := buildFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	results, err := pkg.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Status != VerifyOK {
			t.Fatalf("file %s: status = %v, want ok", r.File.DestName, r.Status)
		}
	}
}

func TestVerifyMismatch(t *testing.T) {
	sysBytes := buildSysFixture(t)
	dllBytes := []byte("this is not really a PE, just DLL-shaped payload bytes")
	catBytes := buildCatalog(t, sysBytes, dllBytes)

	// Corrupt the installed driver.sys after the catalog was computed from
	// the original bytes, so its hash no longer matches.
	corruptSys := append([]byte(nil), sysBytes...)
	corruptSys[len(corruptSys)-1] ^= 0xFF

	fsys := fstest.MapFS{
		"contoso.inf": {Data: []byte(testINF)},
		"contoso.cat": {Data: catBytes},
		"driver.sys":  {Data: corruptSys},
		"helper.dll":  {Data: dllBytes},
	}

	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	results, err := pkg.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var sawMismatch bool
	for _, r := range results {
		if r.File.DestName == "driver.sys" {
			if r.Status != VerifyMismatch {
				t.Fatalf("driver.sys status = %v, want mismatch", r.Status)
			}
			sawMismatch = true
		} else if r.Status != VerifyOK {
			t.Fatalf("file %s: status = %v, want ok", r.File.DestName, r.Status)
		}
	}
	if !sawMismatch {
		t.Fatal("expected a mismatch result for driver.sys")
	}
}

func TestInstall(t *testing.T) {
	fsys, sysBytes, dllBytes := buildFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}

	md := &wim.ImageMetadata{
		Root: &wim.DirEntry{
			Attributes: wim.FileAttributeDirectory,
			SecurityID: wim.SecurityIDNone,
		},
	}
	bt := &wim.BlobTable{}

	const destPath = `Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_deadbeef`
	destDirs := map[DirID]string{DirIDDriverStore: destPath}

	root, newBlobs, err := Install(md, bt, pkg, destDirs)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if root != md.Root {
		t.Fatal("Install did not return md.Root")
	}
	if len(newBlobs) != 2 {
		t.Fatalf("got %d new blobs, want 2", len(newBlobs))
	}
	if len(bt.Entries) != 2 {
		t.Fatalf("got %d blob table entries, want 2", len(bt.Entries))
	}

	sysHash := wim.Hash(sha1.Sum(sysBytes))
	dllHash := wim.Hash(sha1.Sum(dllBytes))

	sysEntry := findPath(t, root, "Windows", "System32", "DriverStore", "FileRepository",
		"contoso.inf_amd64_deadbeef", "driver.sys")
	if sysEntry.MainHash() != sysHash {
		t.Fatalf("driver.sys hash = %s, want %s", sysEntry.MainHash(), sysHash)
	}
	dllEntry := findPath(t, root, "Windows", "System32", "DriverStore", "FileRepository",
		"contoso.inf_amd64_deadbeef", "helper.dll")
	if dllEntry.MainHash() != dllHash {
		t.Fatalf("helper.dll hash = %s, want %s", dllEntry.MainHash(), dllHash)
	}

	for _, e := range bt.Entries {
		if e.RefCount != 1 {
			t.Fatalf("fresh blob table entry RefCount = %d, want 1", e.RefCount)
		}
	}

	// Installing the same package again into the same metadata/blob table
	// must dedupe by hash (RefCount bumped, no new blobs) rather than
	// duplicating entries.
	_, newBlobs2, err := Install(md, bt, pkg, destDirs)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(newBlobs2) != 0 {
		t.Fatalf("second install produced %d new blobs, want 0", len(newBlobs2))
	}
	if len(bt.Entries) != 2 {
		t.Fatalf("blob table grew on reinstall: %d entries, want 2", len(bt.Entries))
	}
	for _, e := range bt.Entries {
		if e.RefCount != 2 {
			t.Fatalf("blob table entry RefCount after reinstall = %d, want 2", e.RefCount)
		}
	}
}

func TestInstallRejectsCorruptSys(t *testing.T) {
	sysBytes := buildSysFixture(t)
	corruptSys := append([]byte(nil), sysBytes...)
	corruptSys[0] = 'X' // corrupt the DOS magic
	dllBytes := []byte("this is not really a PE, just DLL-shaped payload bytes")
	catBytes := buildCatalog(t, sysBytes, dllBytes)

	fsys := fstest.MapFS{
		"contoso.inf": {Data: []byte(testINF)},
		"contoso.cat": {Data: catBytes},
		"driver.sys":  {Data: corruptSys},
		"helper.dll":  {Data: dllBytes},
	}
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}

	md := &wim.ImageMetadata{Root: &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}}
	bt := &wim.BlobTable{}
	destDirs := map[DirID]string{DirIDDriverStore: `Windows\System32\DriverStore\FileRepository\x`}

	if _, _, err := Install(md, bt, pkg, destDirs); err == nil {
		t.Fatal("expected an error installing a corrupt .sys payload")
	}
}

// TestInstallMissingDestDir checks that Install still requires an explicit
// destDirs entry for any DIRID other than 13 (DirIDDriverStore, which
// Install now computes automatically -- see TestInstallComputesDriverStoreDir).
func TestInstallMissingDestDir(t *testing.T) {
	fsys, _, _ := buildFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	// Splice in an extra payload file under an arbitrary DIRID no fixture
	// INF uses, so it's guaranteed absent from destDirs.
	pkg.Files = append(pkg.Files, PayloadFile{
		DestName:   "extra.dll",
		SourceName: "helper.dll",
		SourcePath: "helper.dll",
		DirID:      20000,
	})

	md := &wim.ImageMetadata{Root: &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}}
	bt := &wim.BlobTable{}
	if _, _, err := Install(md, bt, pkg, map[DirID]string{}); err == nil {
		t.Fatal("expected an error when no destination directory is supplied for a non-DriverStore DIRID")
	}
}

// TestInstallComputesDriverStoreDir checks that Install now computes DIRID
// 13's destination itself (via FileRepositoryDirName) when destDirs doesn't
// supply an explicit override, reproducing the real DriverStore folder-name
// hash reverse-engineered from drvstore.dll (see driverstore.go).
func TestInstallComputesDriverStoreDir(t *testing.T) {
	fsys, _, _ := buildFS(t)
	pkg, err := LoadPackage(fsys, "contoso.inf", "amd64")
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	md := &wim.ImageMetadata{Root: &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}}
	bt := &wim.BlobTable{}

	root, _, err := Install(md, bt, pkg, map[DirID]string{})
	if err != nil {
		t.Fatalf("Install (no destDirs at all): %v", err)
	}

	infData, err := fsys.ReadFile("contoso.inf")
	if err != nil {
		t.Fatalf("ReadFile(contoso.inf): %v", err)
	}
	wantDirName := FileRepositoryDirName("contoso.inf", infData, "amd64")

	findPath(t, root, "Windows", "System32", "DriverStore", "FileRepository", wantDirName, "driver.sys")
}

func findPath(t *testing.T, root *wim.DirEntry, components ...string) *wim.DirEntry {
	t.Helper()
	cur := root
	for _, c := range components {
		var next *wim.DirEntry
		for _, child := range cur.Children {
			if child.NameUTF8() == c {
				next = child
				break
			}
		}
		if next == nil {
			t.Fatalf("path component %q not found under %q", c, cur.NameUTF8())
		}
		cur = next
	}
	return cur
}
