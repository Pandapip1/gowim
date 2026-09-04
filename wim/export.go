package wim

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// NewReaderBlobSource adapts a Reader plus the BlobTable already parsed from
// it into a BlobSource: Blob(h) looks h up via bt.ByHash and decompresses its
// resource via r.ReadResource. This is a general-purpose adapter, not
// specific to ExportImage -- it lets WriteTo/Assemble stream blob content
// lazily straight out of an existing WIM (decompressing one blob at a time)
// without the caller ever preloading the source WIM's content into memory,
// which matters when the source is a multi-gigabyte install image.
func NewReaderBlobSource(r *Reader, bt *BlobTable) BlobSource {
	return &readerBlobSource{r: r, bt: bt}
}

type readerBlobSource struct {
	r  *Reader
	bt *BlobTable
}

func (s *readerBlobSource) Blob(h Hash) ([]byte, error) {
	d, ok := s.bt.ByHash(h)
	if !ok {
		return nil, fmt.Errorf("wim: NewReaderBlobSource: no blob table entry for hash %s", h)
	}
	return s.r.ReadResource(d.Resource)
}

// xmlImageRaw is used to pick specific <IMAGE> elements out of a WIM XML
// document while preserving each one's full original inner content verbatim
// (InnerXML), rather than reconstructing it from XMLImage's light-parsed
// fields (which would lose real detail like <WINDOWS><EDITIONID>, etc.).
type xmlImageRaw struct {
	Index int    `xml:"INDEX,attr"`
	Inner string `xml:",innerxml"`
}

type xmlRootRaw struct {
	XMLName xml.Name      `xml:"WIM"`
	Images  []xmlImageRaw `xml:"IMAGE"`
}

// buildExportXMLData extracts the <IMAGE> elements at the given 1-based
// source indices (in the caller's order) from src's Document, and returns a
// new XMLData whose Document is a fresh <WIM> root containing just those
// <IMAGE> elements, renumbered sequentially starting at INDEX="1".
//
// Each selected <IMAGE> element's original inner content is preserved
// verbatim via the innerxml struct-tag technique (see xmlImageRaw); only the
// INDEX attribute is rewritten.
//
// Any <WIM>-level-only content in src.Document (i.e. anything besides the
// <IMAGE> elements themselves -- in practice, real WIM XML data has none,
// but the format does not forbid it) is dropped, not preserved or
// recomputed: ExportImage's output XML is exactly <WIM> followed by the
// selected, renumbered <IMAGE> elements, followed by </WIM>.
func buildExportXMLData(src *XMLData, indices []int) (*XMLData, error) {
	if src == nil {
		return nil, fmt.Errorf("wim: ExportImage: nil source XML data")
	}
	var root xmlRootRaw
	if strings.TrimSpace(src.Document) != "" {
		if err := xml.Unmarshal([]byte(src.Document), &root); err != nil {
			return nil, fmt.Errorf("wim: ExportImage: parse source XML: %w", err)
		}
	}
	byIndex := make(map[int]xmlImageRaw, len(root.Images))
	for _, im := range root.Images {
		byIndex[im.Index] = im
	}

	var b strings.Builder
	b.WriteString("<WIM>")
	for i, idx := range indices {
		im, ok := byIndex[idx]
		if !ok {
			return nil, fmt.Errorf("wim: ExportImage: source XML has no <IMAGE INDEX=%q>", fmt.Sprint(idx))
		}
		fmt.Fprintf(&b, `<IMAGE INDEX="%d">%s</IMAGE>`, i+1, im.Inner)
	}
	b.WriteString("</WIM>")
	return &XMLData{Document: b.String()}, nil
}

// exportHashes walks root's entire tree (all descendants, all Streams,
// unnamed and named/alternate alike) and increments counts[hash] once per
// stream reference, skipping the all-zero hash used for empty files/streams.
// Called once per exported image so that the resulting counts reflect usage
// across only the images actually being exported, not the source WIM's
// original (possibly larger-scope) RefCounts.
func exportHashes(root *DirEntry, counts map[Hash]int, order *[]Hash) {
	if root == nil {
		return
	}
	var walk func(d *DirEntry)
	walk = func(d *DirEntry) {
		for _, s := range d.Streams {
			if s.Hash.IsZero() {
				continue
			}
			if counts[s.Hash] == 0 {
				*order = append(*order, s.Hash)
			}
			counts[s.Hash]++
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	walk(root)
}

// RebuildBlobTable walks each of images' current DirEntry trees and returns a
// fresh BlobTable containing only the blobs actually still referenced, with
// every RefCount recomputed from scratch by counting real references in the
// tree.
//
// This is the "whole-WIM-aware" reclaim step that several packages'
// removal functions (driver.Uninstall, appx.Remove, component.Remove,
// registry.Hive.Save) deliberately defer to a higher-level caller: those
// functions only ever decrement an existing BlobDescriptor.RefCount (or, for
// registry.Hive.Save, add a new one for freshly-written hive content) -- they
// never drop a zero-refcount entry, because removing it outright would be
// premature if some other, not-yet-processed part of the same image still
// referenced it. Left unaddressed, WriteTo/Assemble will still faithfully
// write every entry in the BlobTable they're given, including dead,
// zero-refcount ones -- their payload never actually leaves the file, only
// their DirEntry reference does. Call RebuildBlobTable once, after all tree
// edits for every image being written are done, and pass its result (instead
// of the BlobTable you were mutating refcounts on directly) to WriteTo -- the
// same reclaim step DISM's own /Export-Image performs, which is why real
// nano11/tiny11-style debloat scripts always finish with one.
//
// src supplies the existing blob table to validate each surviving hash
// against (RebuildBlobTable errors if a tree references a hash genuinely
// missing from src, the same validation ExportImage itself already
// performed) -- but no other field of src's entries (RefCount, Resource,
// PartNumber) carries over, since WriteTo recomputes every entry's real
// on-disk placement from scratch anyway regardless of what BlobTable it's
// given.
func RebuildBlobTable(images []*ImageMetadata, src *BlobTable) (*BlobTable, error) {
	counts := make(map[Hash]int)
	var order []Hash
	for _, im := range images {
		if im == nil {
			continue
		}
		exportHashes(im.Root, counts, &order)
	}

	bt := &BlobTable{Entries: make([]BlobDescriptor, 0, len(order))}
	for _, h := range order {
		if _, ok := src.ByHash(h); !ok {
			return nil, fmt.Errorf("wim: RebuildBlobTable: referenced blob %s not found in source blob table", h)
		}
		bt.Entries = append(bt.Entries, BlobDescriptor{
			Hash:       h,
			RefCount:   uint32(counts[h]),
			PartNumber: 1,
			// Resource is intentionally left zero; WriteTo fills it in with
			// the destination's own on-disk placement (d.Resource describes
			// where the blob lives in the *source* file, which is
			// meaningless in the new one).
		})
	}
	return bt, nil
}

// ExportImage copies a subset of a source WIM's images -- plus only the
// blobs those images actually reference -- into a new, standalone WIM,
// mirroring DISM's /Export-Image. It writes the result to w (which, like
// WriteTo, must support both writing and seeking).
//
// src, srcBlobTable, and srcXMLData must already be parsed from the source
// WIM (via NewReader/Reader.BlobTable/Reader.XMLData). imageIndices lists the
// 1-based source image indices to export, in the order they should appear in
// the destination; more than one is supported (not just DISM's common
// single-image case) since WriteTo already accepts a slice of images.
//
// The image-index-to-metadata-resource mapping used to locate each selected
// image follows the same convention WriteTo itself establishes on the write
// side and this package's own tests confirm on the read side:
// srcBlobTable.MetadataResources() lists metadata resources in the same
// order the corresponding images appear (index 1 first), so source image
// index i's metadata resource is MetadataResources()[i-1]. (Real multi-image
// WIMs to cross-check this against were not available in this environment --
// the local sample boot.wim/install.esd each contain exactly one image --
// but this is exactly how WriteTo constructs bt.Entries for images it
// writes, in image order, so a WIM produced by this package's own writer,
// or by wimlib/DISM which uses the same one-metadata-resource-per-image,
// written-in-index-order convention, satisfies it.)
//
// For each selected image, ExportImage reads its metadata resource, walks
// its entire directory-entry tree collecting every referenced blob hash, and
// counts references across only the selected images (see exportHashes) --
// not carried over from src's original file-wide RefCounts, which may
// include images not being exported. It builds a new BlobTable containing
// only the referenced hashes, with freshly computed RefCounts and
// PartNumber 1 (Resource is left zero; WriteTo overwrites it), and a new
// XMLData containing only the selected images' <IMAGE> elements, renumbered
// sequentially from INDEX="1" (see buildExportXMLData for exactly what is
// and is not preserved).
//
// opts is passed straight through to WriteTo, so it may request a different
// CompressionType/ChunkSize than the source WIM used: WriteTo always
// re-encodes every blob's raw bytes with the destination's chosen
// compression regardless of how the source stored them, so this
// transparently supports recompression during export.
func ExportImage(src *Reader, srcBlobTable *BlobTable, srcXMLData *XMLData, imageIndices []int, w io.WriteSeeker, opts WriteOptions) (int64, error) {
	images, bt, xmlData, err := prepareExport(src, srcBlobTable, srcXMLData, imageIndices)
	if err != nil {
		return 0, err
	}
	return WriteTo(w, images, bt, xmlData, NewReaderBlobSource(src, srcBlobTable), opts)
}

// ExportImageAssemble is the in-memory convenience form of ExportImage,
// mirroring Assemble's relationship to WriteTo.
func ExportImageAssemble(src *Reader, srcBlobTable *BlobTable, srcXMLData *XMLData, imageIndices []int, opts WriteOptions) ([]byte, error) {
	images, bt, xmlData, err := prepareExport(src, srcBlobTable, srcXMLData, imageIndices)
	if err != nil {
		return nil, err
	}
	return Assemble(images, bt, xmlData, NewReaderBlobSource(src, srcBlobTable), opts)
}

func prepareExport(src *Reader, srcBlobTable *BlobTable, srcXMLData *XMLData, imageIndices []int) ([]*ImageMetadata, *BlobTable, *XMLData, error) {
	if src == nil {
		return nil, nil, nil, fmt.Errorf("wim: ExportImage: nil source reader")
	}
	if srcBlobTable == nil {
		return nil, nil, nil, fmt.Errorf("wim: ExportImage: nil source blob table")
	}
	if len(imageIndices) == 0 {
		return nil, nil, nil, fmt.Errorf("wim: ExportImage: no image indices given")
	}

	metaResources := srcBlobTable.MetadataResources()

	images := make([]*ImageMetadata, 0, len(imageIndices))
	for _, idx := range imageIndices {
		if idx < 1 || idx > len(metaResources) {
			return nil, nil, nil, fmt.Errorf("wim: ExportImage: image index %d out of range (source has %d image(s))", idx, len(metaResources))
		}
		im, err := src.ImageMetadata(metaResources[idx-1])
		if err != nil {
			return nil, nil, nil, wrapErr(fmt.Sprintf("ExportImage: image %d metadata", idx), err)
		}
		images = append(images, im)
	}

	// Note: we deliberately do not call exportHashes here even though
	// RebuildBlobTable (below) needs exactly that per-image hash/count data.
	// This function used to compute it via its own exportHashes loop and
	// then discard the result, immediately calling RebuildBlobTable, which
	// walks every image's full DirEntry tree *again* to recompute the same
	// counts/order from scratch. On a real multi-gigabyte WIM each such walk
	// is the expensive one (calling BlobTable.ByHash once per file), so that
	// was a full redundant tree walk per export call. RebuildBlobTable stays
	// the single source of truth for this computation -- it's a public
	// function external callers (e.g. driver.Uninstall/appx.Remove/
	// component.Remove/registry.Hive.Save's higher-level caller) invoke
	// directly per its own doc comment, so its signature/behavior is left
	// untouched here; this function simply stops duplicating its work.

	bt, err := RebuildBlobTable(images, srcBlobTable)
	if err != nil {
		return nil, nil, nil, wrapErr("ExportImage", err)
	}

	xmlData, err := buildExportXMLData(srcXMLData, imageIndices)
	if err != nil {
		return nil, nil, nil, err
	}

	return images, bt, xmlData, nil
}
