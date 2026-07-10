package wim

import (
	"bytes"
	"testing"
)

func TestResourceHeaderRoundTrip(t *testing.T) {
	orig := ResourceHeader{
		SizeInWIM:        0x0102030405,
		Flags:            ResFlagCompressed | ResFlagMetadata,
		OffsetInWIM:      0xdeadbeefcafe,
		UncompressedSize: 0x998877,
	}
	b, err := orig.appendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != ResourceHeaderSize {
		t.Fatalf("encoded length = %d, want %d", len(b), ResourceHeaderSize)
	}
	got, err := parseResourceHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != orig {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, orig)
	}
	// Verify the 7-byte size field: byte 7 must be flags, not size overflow.
	if b[7] != orig.Flags {
		t.Fatalf("flags byte = %#x, want %#x", b[7], orig.Flags)
	}
}

func TestResourceHeaderSizeOverflow(t *testing.T) {
	r := ResourceHeader{SizeInWIM: uint64(1) << 56} // one past 56-bit max
	if _, err := r.appendTo(nil); err == nil {
		t.Fatal("expected overflow error for 57-bit size")
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	orig := NewHeader()
	orig.Flags = HdrFlagCompression | HdrFlagCompressLZX
	orig.ChunkSize = 32768
	orig.ImageCount = 2
	orig.BootIndex = 1
	for i := range orig.GUID {
		orig.GUID[i] = byte(i * 7)
	}
	orig.BlobTable = ResourceHeader{SizeInWIM: 500, Flags: ResFlagMetadata, OffsetInWIM: 208, UncompressedSize: 500}
	orig.XMLData = ResourceHeader{SizeInWIM: 200, OffsetInWIM: 708, UncompressedSize: 200}
	orig.BootMetadata = ResourceHeader{SizeInWIM: 1000, Flags: ResFlagMetadata, OffsetInWIM: 908, UncompressedSize: 1000}

	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != HeaderSize {
		t.Fatalf("header length = %d, want %d", len(b), HeaderSize)
	}
	// hdr_size field must equal 208.
	if got := le.Uint32(b[offHdrSize:]); got != HeaderSize {
		t.Fatalf("hdr_size field = %d, want %d", got, HeaderSize)
	}
	got, err := ParseHeader(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != orig {
		t.Fatalf("header round trip mismatch:\n got %+v\nwant %+v", got, orig)
	}
}

func TestHeaderMagic(t *testing.T) {
	// "MSWIM\0\0\0" as the first bytes.
	b, _ := NewHeader().AppendTo(nil)
	want := []byte{'M', 'S', 'W', 'I', 'M', 0, 0, 0}
	if !bytes.Equal(b[:8], want) {
		t.Fatalf("magic bytes = %v, want %v", b[:8], want)
	}
}

func TestHeaderRejectsBadMagic(t *testing.T) {
	b, _ := NewHeader().AppendTo(nil)
	b[0] = 'X'
	if _, err := ParseHeader(b, 0); err != ErrNotWIM {
		t.Fatalf("err = %v, want ErrNotWIM", err)
	}
}

func TestHeaderRejectsBadPartNumber(t *testing.T) {
	h := NewHeader()
	h.PartNumber = 3
	h.TotalParts = 2
	b, _ := h.AppendTo(nil)
	if _, err := ParseHeader(b, 0); err == nil {
		t.Fatal("expected part-number error")
	}
}

func TestBlobTableRoundTrip(t *testing.T) {
	orig := &BlobTable{Entries: []BlobDescriptor{
		{
			Resource:   ResourceHeader{SizeInWIM: 123, Flags: ResFlagCompressed, OffsetInWIM: 208, UncompressedSize: 456},
			PartNumber: 1,
			RefCount:   3,
			Hash:       Hash{1, 2, 3, 4, 5},
		},
		{
			Resource:   ResourceHeader{SizeInWIM: 10, OffsetInWIM: 331, UncompressedSize: 10},
			PartNumber: 1,
			RefCount:   1,
			Hash:       Hash{},
		},
	}}
	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != orig.EncodedLen() || len(b) != 2*BlobDescriptorSize {
		t.Fatalf("encoded length = %d, want %d", len(b), 2*BlobDescriptorSize)
	}
	got, err := ParseBlobTable(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(got.Entries))
	}
	for i := range got.Entries {
		if got.Entries[i] != orig.Entries[i] {
			t.Fatalf("entry %d mismatch:\n got %+v\nwant %+v", i, got.Entries[i], orig.Entries[i])
		}
	}
}

func TestBlobTableSolidGrouping(t *testing.T) {
	tbl := &BlobTable{Entries: []BlobDescriptor{
		// A solid run: one spec (magic) + two packed blobs.
		{Resource: ResourceHeader{Flags: ResFlagSolid, OffsetInWIM: 208, SizeInWIM: 5000, UncompressedSize: SolidResourceMagic}},
		{Resource: ResourceHeader{Flags: ResFlagSolid, OffsetInWIM: 0, SizeInWIM: 100}, Hash: Hash{1}},
		{Resource: ResourceHeader{Flags: ResFlagSolid, OffsetInWIM: 100, SizeInWIM: 200}, Hash: Hash{2}},
		// A standalone entry terminating the run.
		{Resource: ResourceHeader{SizeInWIM: 42, OffsetInWIM: 5208, UncompressedSize: 42}, Hash: Hash{3}},
	}}
	// At VersionDefault, solid flag is ignored: no runs.
	if runs := tbl.SolidResources(false); runs != nil {
		t.Fatalf("expected no runs when solid invalid, got %d", len(runs))
	}
	runs := tbl.SolidResources(true)
	if len(runs) != 1 {
		t.Fatalf("expected 1 solid run, got %d", len(runs))
	}
	if len(runs[0].Specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(runs[0].Specs))
	}
	if len(runs[0].Blobs) != 2 {
		t.Fatalf("expected 2 packed blobs, got %d", len(runs[0].Blobs))
	}
}

func TestSecurityDataRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		descs [][]byte
	}{
		{"empty", nil},
		{"one", [][]byte{{0xaa, 0xbb, 0xcc}}},
		{"three-with-empty", [][]byte{{1, 2}, {}, {3, 4, 5, 6, 7}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := &SecurityData{Descriptors: tc.descs}
			b := orig.AppendTo(nil)
			if uint64(len(b)) != orig.EncodedLen() {
				t.Fatalf("encoded length = %d, EncodedLen = %d", len(b), orig.EncodedLen())
			}
			if len(b)%8 != 0 {
				t.Fatalf("security data not 8-aligned: %d bytes", len(b))
			}
			got, consumed, err := ParseSecurityData(b)
			if err != nil {
				t.Fatal(err)
			}
			if consumed != uint64(len(b)) {
				t.Fatalf("consumed %d, want %d", consumed, len(b))
			}
			if len(got.Descriptors) != len(tc.descs) {
				t.Fatalf("parsed %d descriptors, want %d", len(got.Descriptors), len(tc.descs))
			}
			for i := range tc.descs {
				if !bytes.Equal(got.Descriptors[i], tc.descs[i]) {
					t.Fatalf("descriptor %d mismatch: got %v want %v", i, got.Descriptors[i], tc.descs[i])
				}
			}
		})
	}
}

func TestIntegrityTableRoundTrip(t *testing.T) {
	orig := &IntegrityTable{
		ChunkSize: IntegrityChunkSize,
		Hashes:    []Hash{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}},
	}
	b := orig.AppendTo(nil)
	if len(b) != orig.EncodedLen() {
		t.Fatalf("encoded length = %d, EncodedLen = %d", len(b), orig.EncodedLen())
	}
	got, err := ParseIntegrityTable(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChunkSize != orig.ChunkSize || len(got.Hashes) != len(orig.Hashes) {
		t.Fatalf("mismatch: got chunk=%d n=%d", got.ChunkSize, len(got.Hashes))
	}
	for i := range orig.Hashes {
		if got.Hashes[i] != orig.Hashes[i] {
			t.Fatalf("hash %d mismatch", i)
		}
	}
}

func TestXMLDataRoundTrip(t *testing.T) {
	doc := `<WIM><TOTALBYTES>1024</TOTALBYTES><IMAGE INDEX="1"><NAME>Base</NAME><DESCRIPTION>test</DESCRIPTION><DIRCOUNT>5</DIRCOUNT><FILECOUNT>20</FILECOUNT><TOTALBYTES>1024</TOTALBYTES></IMAGE></WIM>`
	orig := &XMLData{Document: doc}
	b := orig.AppendTo(nil)
	// Must begin with UTF-16LE BOM.
	if len(b) < 2 || b[0] != 0xff || b[1] != 0xfe {
		t.Fatalf("XML data does not begin with UTF-16LE BOM: %v", b[:2])
	}
	got, err := ParseXMLData(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Document != doc {
		t.Fatalf("document mismatch:\n got %q\nwant %q", got.Document, doc)
	}
	if len(got.Images) != 1 {
		t.Fatalf("parsed %d images, want 1", len(got.Images))
	}
	im := got.Images[0]
	if im.Index != 1 || im.Name != "Base" || im.Description != "test" ||
		im.DirCount != 5 || im.FileCount != 20 || im.TotalBytes != 1024 {
		t.Fatalf("image parse mismatch: %+v", im)
	}
}

// TestXMLDataWindowsFields verifies parsing of the <WINDOWS> sub-element and
// its siblings (<DISPLAYNAME>, <DISPLAYDESCRIPTION>, <FLAGS>) using a
// trimmed, verbatim excerpt of the real <IMAGE> element observed in a real
// Windows 11 23H2 install.esd's XML data resource (confirmed 2026-07-10;
// full-image element was ~1.3KB, elided here to just the fields under test).
func TestXMLDataWindowsFields(t *testing.T) {
	doc := `<WIM><IMAGE INDEX="1"><DIRCOUNT>24909</DIRCOUNT><FILECOUNT>99241</FILECOUNT><TOTALBYTES>16383717491</TOTALBYTES><WINDOWS><ARCH>9</ARCH><PRODUCTNAME>Microsoft® Windows® Operating System</PRODUCTNAME><EDITIONID>Professional</EDITIONID><INSTALLATIONTYPE>Client</INSTALLATIONTYPE><PRODUCTTYPE>WinNT</PRODUCTTYPE><PRODUCTSUITE>Terminal Server</PRODUCTSUITE><LANGUAGES><LANGUAGE>en-US</LANGUAGE><DEFAULT>en-US</DEFAULT></LANGUAGES><VERSION><MAJOR>10</MAJOR><MINOR>0</MINOR><BUILD>22621</BUILD><SPBUILD>2283</SPBUILD><SPLEVEL>0</SPLEVEL><BRANCH>ni_release_svc_prod3</BRANCH></VERSION><SYSTEMROOT>WINDOWS</SYSTEMROOT></WINDOWS><NAME>Windows 11 Pro</NAME><DESCRIPTION>Windows 11 Pro</DESCRIPTION><DISPLAYNAME>Windows 11 Pro</DISPLAYNAME><DISPLAYDESCRIPTION>Windows 11 Pro</DISPLAYDESCRIPTION><FLAGS>Professional</FLAGS></IMAGE><TOTALBYTES>3502723354</TOTALBYTES></WIM>`
	orig := &XMLData{Document: doc}
	b := orig.AppendTo(nil)

	got, err := ParseXMLData(b)
	if err != nil {
		t.Fatal(err)
	}

	// Round-trip: Document must be preserved byte-identically; Images is a
	// read-only convenience view that must not affect AppendTo's output.
	if got.Document != doc {
		t.Fatalf("document mismatch:\n got %q\nwant %q", got.Document, doc)
	}
	b2 := got.AppendTo(nil)
	if !bytes.Equal(b, b2) {
		t.Fatalf("round-trip AppendTo mismatch:\n got %v\nwant %v", b2, b)
	}

	if len(got.Images) != 1 {
		t.Fatalf("parsed %d images, want 1", len(got.Images))
	}
	im := got.Images[0]

	if im.DisplayName != "Windows 11 Pro" || im.DisplayDescription != "Windows 11 Pro" || im.Flags != "Professional" {
		t.Fatalf("sibling fields mismatch: %+v", im)
	}

	if im.Windows == nil {
		t.Fatal("Windows is nil, want populated")
	}
	w := im.Windows
	if w.Architecture != 9 {
		t.Fatalf("Architecture = %d, want 9 (PROCESSOR_ARCHITECTURE_AMD64)", w.Architecture)
	}
	if got, want := w.ArchitectureName(), "x64"; got != want {
		t.Fatalf("ArchitectureName() = %q, want %q", got, want)
	}
	if w.ProductName != "Microsoft® Windows® Operating System" {
		t.Fatalf("ProductName = %q", w.ProductName)
	}
	if w.EditionID != "Professional" {
		t.Fatalf("EditionID = %q", w.EditionID)
	}
	if w.InstallationType != "Client" {
		t.Fatalf("InstallationType = %q", w.InstallationType)
	}
	if w.ProductType != "WinNT" {
		t.Fatalf("ProductType = %q", w.ProductType)
	}
	if w.ProductSuite != "Terminal Server" {
		t.Fatalf("ProductSuite = %q", w.ProductSuite)
	}
	if w.SystemRoot != "WINDOWS" {
		t.Fatalf("SystemRoot = %q", w.SystemRoot)
	}
	if len(w.Languages) != 1 || w.Languages[0] != "en-US" {
		t.Fatalf("Languages = %v, want [en-US]", w.Languages)
	}
	if w.DefaultLanguage != "en-US" {
		t.Fatalf("DefaultLanguage = %q, want en-US", w.DefaultLanguage)
	}
	if w.Version == nil {
		t.Fatal("Version is nil, want populated")
	}
	v := w.Version
	if v.Major != 10 || v.Minor != 0 || v.Build != 22621 || v.SPBuild != 2283 || v.SPLevel != 0 || v.Branch != "ni_release_svc_prod3" {
		t.Fatalf("Version mismatch: %+v", v)
	}
}

// TestXMLDataNoWindows confirms XMLImage.Windows is nil (not a zero-valued
// struct pointer) when the <IMAGE> element has no <WINDOWS> child, so callers
// can tell "no Windows metadata" apart from "all-zero Windows metadata".
func TestXMLDataNoWindows(t *testing.T) {
	doc := `<WIM><IMAGE INDEX="1"><NAME>Data</NAME></IMAGE></WIM>`
	got, err := ParseXMLData((&XMLData{Document: doc}).AppendTo(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 1 {
		t.Fatalf("parsed %d images, want 1", len(got.Images))
	}
	if got.Images[0].Windows != nil {
		t.Fatalf("Windows = %+v, want nil", got.Images[0].Windows)
	}
}

func TestDirEntryTreeRoundTrip(t *testing.T) {
	// Build a small tree: root dir with a file and a subdir; the subdir holds
	// a file with a named alternate data stream.
	file := &DirEntry{
		Attributes:     0x20, // FILE_ATTRIBUTE_ARCHIVE
		SecurityID:     0,
		CreationTime:   130000000000000000,
		LastWriteTime:  130000000000001000,
		LastAccessTime: 130000000000002000,
		Name:           stringToUTF16LE("hello.txt"),
		Streams:        []Stream{{Hash: Hash{0x11, 0x22}}},
	}
	streamed := &DirEntry{
		Attributes:      0x20,
		SecurityID:      SecurityIDNone,
		HardLinkGroupID: 0,
		Name:            stringToUTF16LE("data.bin"),
		Streams: []Stream{
			{Hash: Hash{0xaa}}, // unnamed
			{Name: stringToUTF16LE("meta"), Hash: Hash{0xbb}}, // named ADS
		},
	}
	sub := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Name:       stringToUTF16LE("subdir"),
		Streams:    []Stream{{}},
		Children:   []*DirEntry{streamed},
	}
	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Streams:    []Stream{{}},
		Children:   []*DirEntry{file, sub},
	}

	b, err := AppendDirEntryTree(nil, root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseDirEntryTree(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertTreeEqual(t, "", got, root)
}

func TestImageMetadataRoundTrip(t *testing.T) {
	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: 0,
		Streams:    []Stream{{}},
		Children: []*DirEntry{
			{
				Attributes: 0x20,
				SecurityID: 0,
				Name:       stringToUTF16LE("a.txt"),
				Streams:    []Stream{{Hash: Hash{9, 9, 9}}},
			},
		},
	}
	orig := &ImageMetadata{
		Security: &SecurityData{Descriptors: [][]byte{{0x01, 0x00, 0x04, 0x80}}},
		Root:     root,
	}
	b, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseImageMetadata(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Security.Descriptors) != 1 || !bytes.Equal(got.Security.Descriptors[0], orig.Security.Descriptors[0]) {
		t.Fatalf("security data mismatch: %+v", got.Security)
	}
	assertTreeEqual(t, "", got.Root, orig.Root)
}

func assertTreeEqual(t *testing.T, path string, got, want *DirEntry) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("%s: nil mismatch got=%v want=%v", path, got == nil, want == nil)
	}
	if got == nil {
		return
	}
	if got.Attributes != want.Attributes {
		t.Fatalf("%s: attributes got %#x want %#x", path, got.Attributes, want.Attributes)
	}
	if got.SecurityID != want.SecurityID {
		t.Fatalf("%s: security id got %d want %d", path, got.SecurityID, want.SecurityID)
	}
	if got.CreationTime != want.CreationTime || got.LastWriteTime != want.LastWriteTime || got.LastAccessTime != want.LastAccessTime {
		t.Fatalf("%s: timestamps differ", path)
	}
	if !bytes.Equal(got.Name, want.Name) {
		t.Fatalf("%s: name got %q want %q", path, got.NameUTF8(), want.NameUTF8())
	}
	if !bytes.Equal(got.ShortName, want.ShortName) {
		t.Fatalf("%s: short name differs", path)
	}
	if !want.IsReparsePoint() && got.HardLinkGroupID != want.HardLinkGroupID {
		t.Fatalf("%s: hard link id got %d want %d", path, got.HardLinkGroupID, want.HardLinkGroupID)
	}
	if len(got.Streams) != len(want.Streams) {
		t.Fatalf("%s: stream count got %d want %d", path, len(got.Streams), len(want.Streams))
	}
	for i := range want.Streams {
		if got.Streams[i].Hash != want.Streams[i].Hash {
			t.Fatalf("%s: stream %d hash differs", path, i)
		}
		if !bytes.Equal(got.Streams[i].Name, want.Streams[i].Name) {
			t.Fatalf("%s: stream %d name differs", path, i)
		}
	}
	if len(got.Children) != len(want.Children) {
		t.Fatalf("%s: child count got %d want %d", path, len(got.Children), len(want.Children))
	}
	for i := range want.Children {
		assertTreeEqual(t, path+"/"+want.Children[i].NameUTF8(), got.Children[i], want.Children[i])
	}
}
