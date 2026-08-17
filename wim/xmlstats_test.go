package wim

import (
	"crypto/sha1"
	"encoding/xml"
	"strings"
	"testing"
)

// buildStatsFixture returns one image whose tree has: a directory with two
// files (one of them a hard-link-group duplicate of the other), a nested
// subdirectory, and a matching blob table with real UncompressedSize values
// -- enough to exercise every field ComputeTreeStats tallies.
func buildStatsFixture(t *testing.T) (*ImageMetadata, *BlobTable) {
	t.Helper()

	bt := &BlobTable{}
	addBlob := func(data []byte) Hash {
		hash := Hash(sha1.Sum(data))
		bt.Entries = append(bt.Entries, BlobDescriptor{
			Hash: hash, PartNumber: 1, RefCount: 1,
			Resource: ResourceHeader{UncompressedSize: uint64(len(data))},
		})
		return hash
	}

	aData := []byte("aaaaaaaaaa")   // 10 bytes
	bData := []byte("bbbbbbbbbbbb") // 12 bytes
	aHash := addBlob(aData)
	bHash := addBlob(bData)

	root := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone, Streams: []Stream{{}}}
	if _, err := root.Add(`a.txt`, aHash); err != nil {
		t.Fatalf("Add(a.txt): %v", err)
	}
	// a-hardlink.txt shares a.txt's HardLinkGroupID: its bytes should count
	// toward HARDLINKBYTES, not a second time toward TOTALBYTES.
	aLink, err := root.Add(`a-hardlink.txt`, aHash)
	if err != nil {
		t.Fatalf("Add(a-hardlink.txt): %v", err)
	}
	aOriginal, err := root.Lookup(`a.txt`)
	if err != nil {
		t.Fatalf("Lookup(a.txt): %v", err)
	}
	aOriginal.HardLinkGroupID = 42
	aLink.HardLinkGroupID = 42

	if _, err := root.Add(`sub\b.txt`, bHash); err != nil {
		t.Fatalf("Add(sub/b.txt): %v", err)
	}

	return &ImageMetadata{Security: &SecurityData{}, Root: root}, bt
}

func TestComputeTreeStats(t *testing.T) {
	img, bt := buildStatsFixture(t)

	dirCount, fileCount, totalBytes, hardLinkBytes := ComputeTreeStats(img.Root, bt)

	if dirCount != 1 {
		t.Fatalf("dirCount = %d, want 1 (sub)", dirCount)
	}
	if fileCount != 3 {
		t.Fatalf("fileCount = %d, want 3 (a.txt, a-hardlink.txt, sub/b.txt)", fileCount)
	}
	// a.txt (10) + sub/b.txt (12) = 22; a-hardlink.txt's 10 bytes go to
	// hardLinkBytes instead, since it shares a.txt's HardLinkGroupID.
	if totalBytes != 22 {
		t.Fatalf("totalBytes = %d, want 22", totalBytes)
	}
	if hardLinkBytes != 10 {
		t.Fatalf("hardLinkBytes = %d, want 10", hardLinkBytes)
	}
}

func TestRecomputeXMLStatsInsertsAndPreserves(t *testing.T) {
	img, bt := buildStatsFixture(t)
	images := []*ImageMetadata{img}

	// A hand-built <IMAGE> element with real, must-survive content
	// (<NAME>, <WINDOWS><EDITIONID>) and none of the stats elements at
	// all -- exactly the shape a caller building a fresh WIM from scratch
	// (e.g. nano11-go's WinRE stub) starts with.
	xmlData := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>WinRE</NAME><WINDOWS><EDITIONID>WindowsPE</EDITIONID></WINDOWS></IMAGE></WIM>`}

	const timestamp = uint64(0x01DCADC1A02EFE90)
	out, err := RecomputeXMLStats(xmlData, images, bt, timestamp)
	if err != nil {
		t.Fatalf("RecomputeXMLStats: %v", err)
	}

	doc := out.Document
	for _, want := range []string{
		"<NAME>WinRE</NAME>",
		"<WINDOWS><EDITIONID>WindowsPE</EDITIONID></WINDOWS>",
		"<DIRCOUNT>1</DIRCOUNT>",
		"<FILECOUNT>3</FILECOUNT>",
		"<TOTALBYTES>22</TOTALBYTES>",
		"<HARDLINKBYTES>10</HARDLINKBYTES>",
		`<LASTMODIFICATIONTIME><HIGHPART>0x01DCADC1</HIGHPART><LOWPART>0xA02EFE90</LOWPART></LASTMODIFICATIONTIME>`,
		`<CREATIONTIME><HIGHPART>0x01DCADC1</HIGHPART><LOWPART>0xA02EFE90</LOWPART></CREATIONTIME>`,
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("output XML missing %q; got: %s", want, doc)
		}
	}

	// Re-parse (as plain UTF-8 XML, not ParseXMLData's on-disk UTF-16LE+BOM
	// wire format) to confirm the output is still well-formed and has
	// exactly one <IMAGE>.
	var reparsed struct {
		Images []struct{} `xml:"IMAGE"`
	}
	if err := xml.Unmarshal([]byte(doc), &reparsed); err != nil {
		t.Fatalf("re-parse output: %v", err)
	}
	if len(reparsed.Images) != 1 {
		t.Fatalf("reparsed image count = %d, want 1", len(reparsed.Images))
	}
}

func TestRecomputeXMLStatsPreservesExistingCreationTime(t *testing.T) {
	img, bt := buildStatsFixture(t)
	images := []*ImageMetadata{img}

	// A real donor's CREATIONTIME must survive untouched -- only
	// LASTMODIFICATIONTIME (and the plain counts) get refreshed.
	xmlData := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>WinRE</NAME>` +
		`<CREATIONTIME><HIGHPART>0x01DA8409</HIGHPART><LOWPART>0x6BAA1422</LOWPART></CREATIONTIME>` +
		`<LASTMODIFICATIONTIME><HIGHPART>0x1</HIGHPART><LOWPART>0x1</LOWPART></LASTMODIFICATIONTIME>` +
		`</IMAGE></WIM>`}

	out, err := RecomputeXMLStats(xmlData, images, bt, 0x01DCADC1A02EFE90)
	if err != nil {
		t.Fatalf("RecomputeXMLStats: %v", err)
	}

	if !strings.Contains(out.Document, `<CREATIONTIME><HIGHPART>0x01DA8409</HIGHPART><LOWPART>0x6BAA1422</LOWPART></CREATIONTIME>`) {
		t.Fatalf("existing CREATIONTIME was not preserved verbatim: %s", out.Document)
	}
	if !strings.Contains(out.Document, `<LASTMODIFICATIONTIME><HIGHPART>0x01DCADC1</HIGHPART><LOWPART>0xA02EFE90</LOWPART></LASTMODIFICATIONTIME>`) {
		t.Fatalf("LASTMODIFICATIONTIME was not refreshed: %s", out.Document)
	}
	if strings.Contains(out.Document, `<LASTMODIFICATIONTIME><HIGHPART>0x1</HIGHPART><LOWPART>0x1</LOWPART></LASTMODIFICATIONTIME>`) {
		t.Fatalf("stale LASTMODIFICATIONTIME survived: %s", out.Document)
	}
}

func TestRecomputeXMLStatsMissingImageErrors(t *testing.T) {
	img, bt := buildStatsFixture(t)
	xmlData := &XMLData{Document: `<WIM></WIM>`}
	if _, err := RecomputeXMLStats(xmlData, []*ImageMetadata{img}, bt, 1); err == nil {
		t.Fatalf("RecomputeXMLStats: expected error for missing <IMAGE INDEX=\"1\">, got nil")
	}
}
