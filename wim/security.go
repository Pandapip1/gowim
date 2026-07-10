package wim

import "fmt"

// SecurityData is the security-descriptor table that begins every WIM image
// metadata resource (struct wim_security_data). It holds a list of Windows
// SECURITY_DESCRIPTOR_RELATIVE blobs; a directory entry references one by
// 0-based index via its SecurityID field.
//
// This package treats each descriptor as an opaque byte slice; it does not
// parse the SDDL/binary security-descriptor structure.
type SecurityData struct {
	// Descriptors holds the raw security descriptors, indexed as referenced
	// by DirEntry.SecurityID.
	Descriptors [][]byte
}

// alignUp8 rounds n up to a multiple of 8.
func alignUp8(n uint64) uint64 { return (n + 7) &^ 7 }

// ParseSecurityData decodes the security data located at the start of a
// metadata resource buffer. It returns the parsed table and the number of bytes
// it occupies (already rounded up to an 8-byte boundary), which is where the
// directory-entry tree begins.
//
// Layout, from struct wim_security_data_disk:
//
//	+0x00  total_length  le32   (size of this whole section, 8-aligned; 0 means 8)
//	+0x04  num_entries   le32
//	+0x08  sizes[num_entries]  le64 each
//	       descriptors, concatenated, each sizes[i] bytes
//	       zero padding to an 8-byte boundary
func ParseSecurityData(b []byte) (*SecurityData, uint64, error) {
	if len(b) < 8 {
		return nil, 0, fmt.Errorf("%w: security data truncated", ErrInvalidHeader)
	}
	totalLength := uint64(le.Uint32(b[0:4]))
	numEntries := uint64(le.Uint32(b[4:8]))

	// A stored length of 0 is a special case meaning 8.
	totalLength = alignUp8(totalLength)
	if totalLength == 0 {
		totalLength = 8
	}

	// The security_id field of a dentry is a signed 32-bit index, so at most
	// 0x80000000 descriptors are possible.
	if numEntries > 0x80000000 {
		return nil, 0, fmt.Errorf("%w: security data num_entries %d too large", ErrInvalidHeader, numEntries)
	}
	if totalLength > uint64(len(b)) {
		return nil, 0, fmt.Errorf("%w: security data total_length %d exceeds resource size %d", ErrInvalidHeader, totalLength, len(b))
	}

	sizesSize := numEntries * 8
	sizeNoDescriptors := 8 + sizesSize
	if sizeNoDescriptors > totalLength {
		return nil, 0, fmt.Errorf("%w: security data sizes array overflows total_length", ErrInvalidHeader)
	}

	sd := &SecurityData{}
	if numEntries == 0 {
		return sd, totalLength, nil
	}

	sizes := make([]uint64, numEntries)
	for i := uint64(0); i < numEntries; i++ {
		sizes[i] = le.Uint64(b[8+i*8:])
		if sizes[i] > 0xffffffff {
			return nil, 0, fmt.Errorf("%w: security descriptor %d size %d too large", ErrInvalidHeader, i, sizes[i])
		}
	}

	sd.Descriptors = make([][]byte, numEntries)
	off := sizeNoDescriptors
	runningLen := sizeNoDescriptors
	for i := uint64(0); i < numEntries; i++ {
		if sizes[i] == 0 {
			sd.Descriptors[i] = []byte{}
			continue
		}
		runningLen += sizes[i]
		if runningLen > totalLength {
			return nil, 0, fmt.Errorf("%w: security descriptors overflow total_length", ErrInvalidHeader)
		}
		end := off + sizes[i]
		desc := make([]byte, sizes[i])
		copy(desc, b[off:end])
		sd.Descriptors[i] = desc
		off = end
	}
	return sd, totalLength, nil
}

// AppendTo serializes the security data, appending its (8-aligned) bytes to dst
// and returning the new slice. The layout matches write_wim_security_data.
func (sd *SecurityData) AppendTo(dst []byte) []byte {
	numEntries := uint64(len(sd.Descriptors))

	// Compute total_length: header (8) + sizes array + descriptor bytes,
	// rounded up to 8.
	body := uint64(8 + numEntries*8)
	for _, d := range sd.Descriptors {
		body += uint64(len(d))
	}
	totalLength := alignUp8(body)
	if numEntries == 0 {
		// Matches wimlib: an empty table still occupies 8 bytes.
		totalLength = 8
	}

	start := len(dst)
	var head [8]byte
	le.PutUint32(head[0:4], uint32(totalLength))
	le.PutUint32(head[4:8], uint32(numEntries))
	dst = append(dst, head[:]...)

	var sz [8]byte
	for _, d := range sd.Descriptors {
		le.PutUint64(sz[:], uint64(len(d)))
		dst = append(dst, sz[:]...)
	}
	for _, d := range sd.Descriptors {
		dst = append(dst, d...)
	}
	// Zero-pad to the computed total length.
	for uint64(len(dst)-start) < totalLength {
		dst = append(dst, 0)
	}
	return dst
}

// EncodedLen returns the number of bytes AppendTo will write.
func (sd *SecurityData) EncodedLen() uint64 {
	numEntries := uint64(len(sd.Descriptors))
	if numEntries == 0 {
		return 8
	}
	body := uint64(8 + numEntries*8)
	for _, d := range sd.Descriptors {
		body += uint64(len(d))
	}
	return alignUp8(body)
}
