package pe

import (
	"fmt"
	"sort"
)

// Image is the in-memory form of a PE/COFF container: the DOS header/stub,
// PE signature, COFF file header, optional header, section headers and raw
// section data, and the Attribute Certificate Table.
//
// Parse decodes an Image from a byte slice; (*Image).AppendTo serializes it
// back. For a well-formed input, Parse followed by AppendTo reproduces the
// original bytes exactly, except that any zero-filled gaps between the
// header region, sections, and the certificate table are reconstructed as
// zero bytes rather than copied verbatim (this package does not track
// arbitrary inter-structure padding, only the padding it names explicitly:
// DOSStub, HeaderPadding, and Tail).
type Image struct {
	// DOSStub is the verbatim bytes between the end of IMAGE_DOS_HEADER (64
	// bytes) and e_lfanew — the MS-DOS stub program and message. This
	// package preserves it as an opaque byte range; e_lfanew itself is not
	// stored separately but derived as DOSHeaderSize+len(DOSStub) when
	// serializing.
	DOSStub []byte

	// FileHeader is the COFF file header. Its NumberOfSections and
	// SizeOfOptionalHeader fields are ignored by AppendTo, which derives
	// them from Sections and OptionalHeader respectively.
	FileHeader FileHeader

	// OptionalHeader is the PE32 or PE32+ optional header, selected by its
	// Magic field.
	OptionalHeader OptionalHeader

	// Sections holds one entry per section header table entry, each paired
	// with its raw on-disk bytes.
	Sections []Section

	// Certificates holds the Attribute Certificate Table entries located by
	// OptionalHeader.DataDirectory[DirEntrySecurity], if present. Each entry
	// exposes its bCertificate payload as raw bytes without interpreting it;
	// for an Authenticode-signed driver this is a PKCS#7 SignedData blob
	// (structurally parsed by the sibling package
	// github.com/gavin-john/gowim/cat, not by this package).
	Certificates []Certificate

	// HeaderPadding is the verbatim bytes, if any, between the end of the
	// section header table and OptionalHeader.SizeOfHeaders (the file-aligned
	// boundary at which the loader expects section data or other content to
	// begin).
	HeaderPadding []byte

	// Tail is any verbatim bytes present after the last byte accounted for
	// by sections, the certificate table, and the header region — for
	// example, overlay data appended after the image proper. It is nil if
	// there is none.
	Tail []byte
}

// SecurityDirectory returns the Security data directory entry
// (OptionalHeader.DataDirectory[DirEntrySecurity]), if present. Its
// VirtualAddress is a file offset (not an RVA) giving the start of the
// Attribute Certificate Table, and its Size is the table's total length in
// bytes.
func (img *Image) SecurityDirectory() (DataDirectory, bool) {
	return img.OptionalHeader.Directory(DirEntrySecurity)
}

// Parse decodes a PE/COFF image from data, validating the DOS header, PE
// signature, COFF file header, optional header, and section header table,
// and locating (but not interpreting) section raw data and the Attribute
// Certificate Table.
func Parse(data []byte) (*Image, error) {
	stub, lfanew, err := parseDOSHeader(data)
	if err != nil {
		return nil, err
	}

	fhOff := int(lfanew) + PESignatureSize
	if fhOff+FileHeaderSize > len(data) {
		return nil, fmt.Errorf("%w: file header at %#x runs past end of file", ErrTruncated, fhOff)
	}
	fh, err := parseFileHeader(data[fhOff : fhOff+FileHeaderSize])
	if err != nil {
		return nil, wrapErr("file header", err)
	}

	ohOff := fhOff + FileHeaderSize
	ohEnd := ohOff + int(fh.SizeOfOptionalHeader)
	if ohEnd < ohOff || ohEnd > len(data) {
		return nil, fmt.Errorf("%w: optional header at %#x (size %d) runs past end of file", ErrTruncated, ohOff, fh.SizeOfOptionalHeader)
	}
	oh, err := parseOptionalHeader(data[ohOff:ohEnd])
	if err != nil {
		return nil, wrapErr("optional header", err)
	}

	shOff := ohEnd
	shTableLen := int(fh.NumberOfSections) * SectionHeaderSize
	shTableEnd := shOff + shTableLen
	if shTableEnd < shOff || shTableEnd > len(data) {
		return nil, fmt.Errorf("%w: section header table at %#x (%d sections) runs past end of file", ErrTruncated, shOff, fh.NumberOfSections)
	}

	sections := make([]Section, fh.NumberOfSections)
	maxEnd := shTableEnd
	for i := range sections {
		sh, err := parseSectionHeader(data[shOff+i*SectionHeaderSize:])
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("section header %d", i), err)
		}
		var raw []byte
		if sh.PointerToRawData != 0 && sh.SizeOfRawData != 0 {
			start := int(sh.PointerToRawData)
			end := start + int(sh.SizeOfRawData)
			if end < start || end > len(data) {
				return nil, fmt.Errorf("%w: section %d raw data [%#x:%#x] runs past end of file", ErrTruncated, i, start, end)
			}
			raw = append([]byte(nil), data[start:end]...)
			if end > maxEnd {
				maxEnd = end
			}
		}
		sections[i] = Section{Header: sh, RawData: raw}
	}

	var headerPadding []byte
	if int(oh.SizeOfHeaders) > shTableEnd && int(oh.SizeOfHeaders) <= len(data) {
		headerPadding = append([]byte(nil), data[shTableEnd:oh.SizeOfHeaders]...)
		if int(oh.SizeOfHeaders) > maxEnd {
			maxEnd = int(oh.SizeOfHeaders)
		}
	}

	var certs []Certificate
	if secDir, ok := oh.Directory(DirEntrySecurity); ok && secDir.Size > 0 {
		start := int(secDir.VirtualAddress)
		end := start + int(secDir.Size)
		if end < start || end > len(data) {
			return nil, fmt.Errorf("%w: certificate table [%#x:%#x] runs past end of file", ErrTruncated, start, end)
		}
		certs, err = ParseCertificateTable(data[start:end])
		if err != nil {
			return nil, wrapErr("certificate table", err)
		}
		if end > maxEnd {
			maxEnd = end
		}
	}

	var tail []byte
	if maxEnd < len(data) {
		tail = append([]byte(nil), data[maxEnd:]...)
	}

	return &Image{
		DOSStub:        stub,
		FileHeader:     fh,
		OptionalHeader: oh,
		Sections:       sections,
		Certificates:   certs,
		HeaderPadding:  headerPadding,
		Tail:           tail,
	}, nil
}

// chunk is a byte range to be placed at a specific file offset when
// reassembling an Image; used internally by AppendTo to interleave section
// data and the certificate table, which are addressed by absolute file
// offset rather than being contiguous with the header region.
type chunk struct {
	offset int
	data   []byte
}

// AppendTo serializes img, appending to dst. It reconstructs the DOS
// header/stub, PE signature, COFF file header (with NumberOfSections and
// SizeOfOptionalHeader derived from Sections and OptionalHeader), optional
// header, section header table, HeaderPadding, and then each section's raw
// data and the certificate table at the file offsets recorded in their
// respective headers (Section.Header.PointerToRawData and
// OptionalHeader.DataDirectory[DirEntrySecurity]), zero-filling any gaps
// between them, followed by Tail.
//
// AppendTo assumes img describes a well-formed, non-overlapping layout (as
// produced by Parse); it does not return an error for a malformed Image with
// overlapping regions, since doing so would require rejecting hand-built
// values that are merely unusual. Overlapping chunks are written in offset
// order without validation, which may produce output the caller did not
// intend.
func (img *Image) AppendTo(dst []byte) ([]byte, error) {
	fh := img.FileHeader
	fh.NumberOfSections = uint16(len(img.Sections))
	ohLen, err := img.OptionalHeader.EncodedLen()
	if err != nil {
		return dst, err
	}
	fh.SizeOfOptionalHeader = uint16(ohLen)

	header := appendDOSHeader(nil, img.DOSStub)
	header = fh.AppendTo(header)
	header, err = img.OptionalHeader.AppendTo(header)
	if err != nil {
		return dst, err
	}
	for _, s := range img.Sections {
		header = s.Header.AppendTo(header)
	}
	header = append(header, img.HeaderPadding...)

	var chunks []chunk
	for i, s := range img.Sections {
		if s.Header.PointerToRawData == 0 || len(s.RawData) == 0 {
			continue
		}
		if uint32(len(s.RawData)) != s.Header.SizeOfRawData {
			return dst, fmt.Errorf("pe: section %d: RawData length %d does not match SizeOfRawData %d", i, len(s.RawData), s.Header.SizeOfRawData)
		}
		chunks = append(chunks, chunk{offset: int(s.Header.PointerToRawData), data: s.RawData})
	}
	if secDir, ok := img.SecurityDirectory(); ok && secDir.Size > 0 {
		certBytes := AppendCertificateTable(nil, img.Certificates)
		chunks = append(chunks, chunk{offset: int(secDir.VirtualAddress), data: certBytes})
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].offset < chunks[j].offset })

	out := append(dst, header...)
	pos := len(header)
	for _, c := range chunks {
		if c.offset > pos {
			out = append(out, make([]byte, c.offset-pos)...)
			pos = c.offset
		}
		out = append(out, c.data...)
		pos += len(c.data)
	}
	out = append(out, img.Tail...)
	return out, nil
}
