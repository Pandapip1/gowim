package wim

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ComputeTreeStats walks root's entire tree (every descendant, not
// including root itself -- matching real WIM XML, where a real image's
// <DIRCOUNT>/<FILECOUNT> never count the image's own root directory) and
// tallies the same four statistics a real WIM image's XML carries:
// dirCount and fileCount (one increment each per directory/file entry
// found), totalBytes (the sum of every file's real UncompressedSize, via
// bt), and hardLinkBytes (bytes belonging to a file whose HardLinkGroupID
// is shared with an entry already counted -- i.e. bytes TOTALBYTES counts
// again for a second name pointing at the same real content).
//
// This hardLinkBytes interpretation (first entry in a hard-link group counts
// toward totalBytes only; every subsequent entry in the same group counts
// toward hardLinkBytes instead) is this package's own best-effort reading of
// what the field represents -- reverse-engineered from real WIM XML samples
// during this package's development, not confirmed against DISM/wimgapi
// source. It is never load-bearing for a real WIM consumer the way
// LASTMODIFICATIONTIME is (see RecomputeXMLStats's doc comment): treat it as
// a reasonable approximation, not a guaranteed-exact figure.
//
// A file whose hash is not found in bt contributes 0 bytes rather than
// erroring -- validating that every referenced hash actually exists is
// RebuildBlobTable's job, not this function's.
func ComputeTreeStats(root *DirEntry, bt *BlobTable) (dirCount, fileCount, totalBytes, hardLinkBytes uint64) {
	seenHardLinkGroups := make(map[uint64]bool)
	var walk func(d *DirEntry)
	walk = func(d *DirEntry) {
		for _, child := range d.Children {
			if child.IsDirectory() {
				dirCount++
				walk(child)
				continue
			}
			fileCount++
			hash := child.MainHash()
			var size uint64
			if desc, ok := bt.ByHash(hash); ok {
				size = desc.Resource.UncompressedSize
			}
			if child.HardLinkGroupID != 0 {
				if seenHardLinkGroups[child.HardLinkGroupID] {
					hardLinkBytes += size
					continue
				}
				seenHardLinkGroups[child.HardLinkGroupID] = true
			}
			totalBytes += size
		}
	}
	walk(root)
	return dirCount, fileCount, totalBytes, hardLinkBytes
}

// xmlRawChild is one top-level child element of an <IMAGE> element's inner
// XML, preserved as raw source text (not re-serialized from a parsed
// struct) so that unrelated content this package doesn't model at all
// (<WINDOWS>, <DESCRIPTION>, <RESOURCES>, vendor extensions, ...) survives
// RecomputeXMLStats byte-for-byte, the same "describe, don't reconstruct"
// principle xmlImageRaw/InnerXML already use in export.go.
type xmlRawChild struct {
	Name string
	Raw  string
}

// splitTopLevelElements walks innerXML's top-level child elements (skipping
// whitespace/text between them) and returns each one's element name and
// exact raw source text, via Decoder.InputOffset/Skip rather than
// re-marshaling -- preserving original formatting, attribute order, and
// escaping exactly.
func splitTopLevelElements(innerXML string) ([]xmlRawChild, error) {
	dec := xml.NewDecoder(strings.NewReader(innerXML))
	var children []xmlRawChild
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if err := dec.Skip(); err != nil {
			return nil, fmt.Errorf("wim: skip <%s>: %w", se.Name.Local, err)
		}
		end := dec.InputOffset()
		children = append(children, xmlRawChild{Name: se.Name.Local, Raw: innerXML[start:end]})
	}
	return children, nil
}

// statsElementNames are the <IMAGE> child elements RecomputeXMLStats
// refreshes on every call (CREATIONTIME is handled separately: inserted
// only if entirely absent, never overwritten -- see the function's doc
// comment).
var statsElementNames = map[string]bool{
	"DIRCOUNT":             true,
	"FILECOUNT":            true,
	"TOTALBYTES":           true,
	"HARDLINKBYTES":        true,
	"LASTMODIFICATIONTIME": true,
}

// RecomputeXMLStats returns a copy of xmlData with each images[i]'s
// corresponding <IMAGE INDEX="i+1"> element's statistics refreshed to
// reflect that image's current tree, mirroring wimlib's own
// xml_update_image_info() (src/xml.c), which real WIM writers run right
// before every write:
//   - <DIRCOUNT>, <FILECOUNT>, <TOTALBYTES>, <HARDLINKBYTES> are recomputed
//     from images[i].Root via ComputeTreeStats and overwrite whatever was
//     there before (or are inserted fresh if absent).
//   - <LASTMODIFICATIONTIME> is likewise always overwritten/inserted, set to
//     timestamp.
//   - <CREATIONTIME> is inserted, set to timestamp, ONLY if the element has
//     none already; an existing <CREATIONTIME> is left completely
//     untouched. This matches wimlib's own behavior (xml_add_image() sets
//     it once, at image creation; nothing ever rewrites it afterward) and
//     is why this function takes an existing XMLData rather than building
//     one from scratch -- callers that already know a real creation time
//     (e.g. grafted from a real donor image) should put it in their input
//     XMLData before calling this, so it survives.
//
// Every other child element of each <IMAGE> (<NAME>, <WINDOWS>,
// <DESCRIPTION>, <RESOURCES>, vendor extensions, ...) is preserved
// byte-for-byte in its original relative order; the six stats elements
// above are collected and reinserted as a single block immediately after
// them, not interleaved back into their original positions -- real WIM
// XML consumers are documented/observed to parse these by tag name, not
// position, so this does not need to (and does not attempt to) reproduce
// exact original element ordering.
//
// This is the fix for the real bug that motivated it: a fresh <IMAGE>
// element built by hand with no <LASTMODIFICATIONTIME> at all resolves to
// 0 when a real WIM consumer looks it up, and at least one real Windows
// component (Windows Setup's SafeOS phase, via wimgapi.dll's
// WIMCommitImageHandle -> StateStoreGetMountedImageTime, confirmed by
// clean-room disassembly of the real DLL) has a genuine bug where its
// registry-backed cache of that value conflates "legitimately read as
// zero" with "read failed," aborting the caller outright. See
// github.com/Pandapip1/nano11-go's filecleanup.go (winREStubFromDonor) for
// the full real-world incident this was reverse-engineered from.
//
// Every images[i] must have a corresponding <IMAGE INDEX="i+1"> already
// present in xmlData.Document -- RecomputeXMLStats only refreshes an
// existing element's statistics, it does not synthesize a whole new
// <IMAGE> element (with a real <NAME>, etc.) on a caller's behalf.
func RecomputeXMLStats(xmlData *XMLData, images []*ImageMetadata, bt *BlobTable, timestamp uint64) (*XMLData, error) {
	if xmlData == nil {
		return nil, fmt.Errorf("wim: RecomputeXMLStats: nil XMLData")
	}
	var root xmlRootRaw
	if strings.TrimSpace(xmlData.Document) != "" {
		if err := xml.Unmarshal([]byte(xmlData.Document), &root); err != nil {
			return nil, fmt.Errorf("wim: RecomputeXMLStats: parse XML: %w", err)
		}
	}
	byIndex := make(map[int]xmlImageRaw, len(root.Images))
	for _, im := range root.Images {
		byIndex[im.Index] = im
	}

	for i, img := range images {
		index := i + 1
		im, ok := byIndex[index]
		if !ok {
			return nil, fmt.Errorf("wim: RecomputeXMLStats: no <IMAGE INDEX=%d> for images[%d]", index, i)
		}
		if img == nil {
			return nil, fmt.Errorf("wim: RecomputeXMLStats: images[%d] is nil", i)
		}

		children, err := splitTopLevelElements(im.Inner)
		if err != nil {
			return nil, fmt.Errorf("wim: RecomputeXMLStats: <IMAGE INDEX=%d>: %w", index, err)
		}

		dirCount, fileCount, totalBytes, hardLinkBytes := ComputeTreeStats(img.Root, bt)

		var kept strings.Builder
		hasCreationTime := false
		for _, c := range children {
			if c.Name == "CREATIONTIME" {
				hasCreationTime = true
				kept.WriteString(c.Raw)
				continue
			}
			if statsElementNames[c.Name] {
				continue
			}
			kept.WriteString(c.Raw)
		}

		var stats strings.Builder
		fmt.Fprintf(&stats, `<DIRCOUNT>%d</DIRCOUNT><FILECOUNT>%d</FILECOUNT><TOTALBYTES>%d</TOTALBYTES><HARDLINKBYTES>%d</HARDLINKBYTES>`,
			dirCount, fileCount, totalBytes, hardLinkBytes)
		if !hasCreationTime {
			fmt.Fprintf(&stats, `<CREATIONTIME><HIGHPART>0x%08X</HIGHPART><LOWPART>0x%08X</LOWPART></CREATIONTIME>`,
				uint32(timestamp>>32), uint32(timestamp))
		}
		fmt.Fprintf(&stats, `<LASTMODIFICATIONTIME><HIGHPART>0x%08X</HIGHPART><LOWPART>0x%08X</LOWPART></LASTMODIFICATIONTIME>`,
			uint32(timestamp>>32), uint32(timestamp))

		im.Inner = stats.String() + kept.String()
		byIndex[index] = im
	}

	var b strings.Builder
	b.WriteString("<WIM>")
	for i := range images {
		index := i + 1
		im := byIndex[index]
		fmt.Fprintf(&b, `<IMAGE INDEX="%d">%s</IMAGE>`, index, im.Inner)
	}
	b.WriteString("</WIM>")
	return &XMLData{Document: b.String()}, nil
}
