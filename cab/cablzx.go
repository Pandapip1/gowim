package cab

// LZX decompression for the CAB-flavor of LZX, ported line-for-line from
// libmspack (https://github.com/kyz/libmspack, commit range fetched
// 2026-09-14) mspack/lzxd.c's lzxd_decompress/lzxd_init/lzxd_read_lens --
// the real, independent, widely-used (ii) reference implementation behind
// `cabextract`/`7z`'s own CAB-LZX support (this package's own real test
// cabinet was cross-extracted with both, byte-for-byte, as ground truth --
// see cablzx_test.go). This package's sibling gowim/lzx package explicitly
// does *not* implement this flavor (see its README's "WIM vs. CAB-LZX"
// section) -- WIM-LZX resets Huffman state and the match window every
// 32768-byte chunk and always applies the E8 filter with a fixed magic
// size, whereas CAB-LZX keeps one persistent, sliding/circular window and
// Huffman-table state across an entire CFFOLDER's CFDATA blocks, uses an
// explicit 24-bit block-size field, and signals E8 filtering (and its
// translation size) once via a header bit instead of always applying it.
// Only the "is_delta=0" (plain CAB, not LZX DELTA/CHM) and
// "reset_interval=0" (true for every real CAB file; CAB has no reset
// interval field at all, unlike CHM) paths of libmspack's decoder are
// implemented, since cabinet files never use the other modes.
//
// libmspack's own header comment on lzxd.c documents several real
// discrepancies between Microsoft's original "cab-sdk.exe" LZX prose
// specification and Microsoft's own shipped implementation (com.ms.util.cab)
// -- e.g. position-slot counts, the aligned-offset-tree's position relative
// to the main/length trees, and the uncompressed block's length field not
// being documented at all -- and states its own code follows the real
// implementation's behavior, not the prose, in every such conflict. This
// port follows libmspack (i.e. the real implementation's behavior) for the
// same reason, and was verified against real data rather than assumed: see
// cablzx_test.go, which decodes the real downloaded
// OpenSSH-Server-Package-amd64.cab end to end and diffs every one of its 38
// files, byte for byte, against both `cabextract`'s and `7z`'s independent
// extraction of the same real file.

import "fmt"

const (
	lzxNumChars            = 256
	lzxMinMatch            = 2
	lzxMaxMatch            = 257
	lzxNumPrimaryLengths   = 7 // matches libmspack's LZX_NUM_PRIMARY_LENGTHS
	lzxNumSecondaryLengths = 249
	lzxFrameSize           = 32768
	lzxPretreeNumSymbols   = 20
	lzxPretreeTableBits    = 6
	lzxMainTreeTableBits   = 10
	lzxLengthTableBits     = 8
	lzxAlignedTableBits    = 7
	lzxMainTreeMaxSymbols  = lzxNumChars + 50*8 // 50 slots is the max for a 2MB (order 21) window
	lzxLengthMaxSymbols    = lzxNumSecondaryLengths + 1
	lzxAlignedMaxSymbols   = 8
)

var lzxPositionSlots = [11]uint32{30, 32, 34, 36, 38, 42, 50, 66, 98, 162, 290}

var lzxExtraBits = [36]uint{
	0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8,
	9, 9, 10, 10, 11, 11, 12, 12, 13, 13, 14, 14, 15, 15, 16, 16,
}

var lzxPositionBase = [290]uint32{
	0, 1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512,
	768, 1024, 1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768,
	49152, 65536, 98304, 131072, 196608, 262144, 393216, 524288, 655360,
	786432, 917504, 1048576, 1179648, 1310720, 1441792, 1572864, 1703936,
	1835008, 1966080, 2097152, 2228224, 2359296, 2490368, 2621440, 2752512,
	2883584, 3014656, 3145728, 3276800, 3407872, 3538944, 3670016, 3801088,
	3932160, 4063232, 4194304, 4325376, 4456448, 4587520, 4718592, 4849664,
	4980736, 5111808, 5242880, 5373952, 5505024, 5636096, 5767168, 5898240,
	6029312, 6160384, 6291456, 6422528, 6553600, 6684672, 6815744, 6946816,
	7077888, 7208960, 7340032, 7471104, 7602176, 7733248, 7864320, 7995392,
	8126464, 8257536, 8388608, 8519680, 8650752, 8781824, 8912896, 9043968,
	9175040, 9306112, 9437184, 9568256, 9699328, 9830400, 9961472, 10092544,
	10223616, 10354688, 10485760, 10616832, 10747904, 10878976, 11010048,
	11141120, 11272192, 11403264, 11534336, 11665408, 11796480, 11927552,
	12058624, 12189696, 12320768, 12451840, 12582912, 12713984, 12845056,
	12976128, 13107200, 13238272, 13369344, 13500416, 13631488, 13762560,
	13893632, 14024704, 14155776, 14286848, 14417920, 14548992, 14680064,
	14811136, 14942208, 15073280, 15204352, 15335424, 15466496, 15597568,
	15728640, 15859712, 15990784, 16121856, 16252928, 16384000, 16515072,
	16646144, 16777216, 16908288, 17039360, 17170432, 17301504, 17432576,
	17563648, 17694720, 17825792, 17956864, 18087936, 18219008, 18350080,
	18481152, 18612224, 18743296, 18874368, 19005440, 19136512, 19267584,
	19398656, 19529728, 19660800, 19791872, 19922944, 20054016, 20185088,
	20316160, 20447232, 20578304, 20709376, 20840448, 20971520, 21102592,
	21233664, 21364736, 21495808, 21626880, 21757952, 21889024, 22020096,
	22151168, 22282240, 22413312, 22544384, 22675456, 22806528, 22937600,
	23068672, 23199744, 23330816, 23461888, 23592960, 23724032, 23855104,
	23986176, 24117248, 24248320, 24379392, 24510464, 24641536, 24772608,
	24903680, 25034752, 25165824, 25296896, 25427968, 25559040, 25690112,
	25821184, 25952256, 26083328, 26214400, 26345472, 26476544, 26607616,
	26738688, 26869760, 27000832, 27131904, 27262976, 27394048, 27525120,
	27656192, 27787264, 27918336, 28049408, 28180480, 28311552, 28442624,
	28573696, 28704768, 28835840, 28966912, 29097984, 29229056, 29360128,
	29491200, 29622272, 29753344, 29884416, 30015488, 30146560, 30277632,
	30408704, 30539776, 30670848, 30801920, 30932992, 31064064, 31195136,
	31326208, 31457280, 31588352, 31719424, 31850496, 31981568, 32112640,
	32243712, 32374784, 32505856, 32636928, 32768000, 32899072, 33030144,
	33161216, 33292288, 33423360,
}

const (
	lzxBlockUncompressed = 3
	lzxBlockVerbatim     = 1
	lzxBlockAligned      = 2
)

type lzxCabState struct {
	r          *bitReader
	window     []byte
	windowSize int

	windowPosn int
	framePosn  int
	frame      int

	r0, r1, r2 uint32

	headerRead    bool
	intelStarted  bool
	intelFilesize int32

	blockType      int
	blockLength    int
	blockRemaining int

	mainLens    [lzxMainTreeMaxSymbols]byte
	lenLens     [lzxLengthMaxSymbols]byte
	alignLens   [lzxAlignedMaxSymbols]byte
	lengthEmpty bool

	mainDec  *huffTable
	lenDec   *huffTable
	alignDec *huffTable

	numOffsets int
}

// lzxCabDecompress decompresses one CFFOLDER's worth of CAB-LZX-compressed
// CFDATA payload (all of that folder's CFDATA blocks' compressed bytes,
// concatenated in file order, per cfdata.go) into its full decompressed
// byte stream. uncompSizes gives each source CFDATA block's declared
// decompressed size, in order -- CAB-LZX's frame structure (and therefore
// exactly when the E8 filter and Huffman-table continuation apply) is
// driven by these declared sizes, not by re-deriving them, matching
// libmspack's lzxd_decompress (which is told the per-block uncompressed
// size by its caller, cabd_sys_read_block, the same way).
func lzxCabDecompress(compressed []byte, uncompSizes []int, windowBits int) ([]byte, error) {
	if windowBits < 15 || windowBits > 21 {
		return nil, fmt.Errorf("cab: unsupported LZX window order %d (want 15-21)", windowBits)
	}
	totalOut := 0
	for _, n := range uncompSizes {
		totalOut += n
	}
	out := make([]byte, totalOut)

	windowSize := 1 << uint(windowBits)
	st := &lzxCabState{
		r:          newBitReader(compressed),
		window:     make([]byte, windowSize),
		windowSize: windowSize,
		r0:         1, r1: 1, r2: 1,
		numOffsets: int(lzxPositionSlots[windowBits-15]) << 3,
	}

	outPos := 0
	for _, frameSize := range frameSizes(totalOut) {
		if err := st.decodeFrame(frameSize); err != nil {
			return out[:outPos], err
		}
		frameData := st.window[st.framePosn : st.framePosn+frameSize]
		filtered := st.intelStarted && st.intelFilesize != 0 && st.frame < 32768 && frameSize > 10
		if filtered {
			buf := make([]byte, frameSize)
			copy(buf, frameData)
			e8Untranslate(buf, outPos, st.intelFilesize)
			copy(out[outPos:outPos+frameSize], buf)
		} else {
			copy(out[outPos:outPos+frameSize], frameData)
		}
		outPos += frameSize
		st.framePosn += frameSize
		st.frame++
		if st.windowPosn == st.windowSize {
			st.windowPosn = 0
		}
		if st.framePosn == st.windowSize {
			st.framePosn = 0
		}
	}
	return out, nil
}

// frameSizes splits totalOut into libmspack's fixed 32768-byte frames (the
// final frame may be shorter).
func frameSizes(totalOut int) []int {
	var sizes []int
	for off := 0; off < totalOut; off += lzxFrameSize {
		n := lzxFrameSize
		if totalOut-off < n {
			n = totalOut - off
		}
		sizes = append(sizes, n)
	}
	return sizes
}

func (st *lzxCabState) decodeFrame(frameSize int) error {
	r := st.r
	if !st.headerRead {
		if r.readBits(1) != 0 {
			hi := r.readBits(16)
			lo := r.readBits(16)
			st.intelFilesize = int32((hi << 16) | lo)
		} else {
			st.intelFilesize = 0
		}
		st.headerRead = true
	}

	bytesTodo := frameSize
	for bytesTodo > 0 {
		if st.blockRemaining == 0 {
			if st.blockType == lzxBlockUncompressed && st.blockLength&1 != 0 {
				r.readByte()
			}
			st.blockType = int(r.readBits(3))
			hi := r.readBits(16)
			lo := r.readBits(8)
			st.blockLength = int(hi<<8 | lo)
			st.blockRemaining = st.blockLength

			switch st.blockType {
			case lzxBlockAligned:
				for i := range st.alignLens {
					st.alignLens[i] = byte(r.readBits(3))
				}
				var err error
				st.alignDec, err = newHuffTable(st.alignLens[:], lzxAlignedTableBits)
				if err != nil {
					return err
				}
				fallthrough
			case lzxBlockVerbatim:
				if err := readLZXLens(r, st.mainLens[:256], 0, 256); err != nil {
					return err
				}
				if err := readLZXLens(r, st.mainLens[256:lzxNumChars+st.numOffsets], 256, lzxNumChars+st.numOffsets); err != nil {
					return err
				}
				var err error
				st.mainDec, err = newHuffTable(st.mainLens[:lzxNumChars+st.numOffsets], lzxMainTreeTableBits)
				if err != nil {
					return err
				}
				if st.mainLens[0xE8] != 0 {
					st.intelStarted = true
				}
				if err := readLZXLens(r, st.lenLens[:lzxNumSecondaryLengths], 0, lzxNumSecondaryLengths); err != nil {
					return err
				}
				st.lenDec, st.lengthEmpty, err = newHuffTableMaybeEmpty(st.lenLens[:lzxNumSecondaryLengths], lzxLengthTableBits)
				if err != nil {
					return err
				}
			case lzxBlockUncompressed:
				st.intelStarted = true
				r.align()
				st.r0 = r.readU32()
				st.r1 = r.readU32()
				st.r2 = r.readU32()
				if st.r0 == 0 || st.r1 == 0 || st.r2 == 0 {
					return fmt.Errorf("cab: %w: zero recent offset in uncompressed block", ErrInvalidLZXData)
				}
			default:
				return fmt.Errorf("cab: %w: bad block type %d", ErrInvalidLZXData, st.blockType)
			}
		}

		thisRun := st.blockRemaining
		if thisRun > bytesTodo {
			thisRun = bytesTodo
		}
		bytesTodo -= thisRun
		st.blockRemaining -= thisRun

		switch st.blockType {
		case lzxBlockAligned, lzxBlockVerbatim:
			overrun, err := st.decodeMatches(thisRun)
			if err != nil {
				return err
			}
			// A match's length need not respect this_run's boundary (a
			// match is atomic and always fully written); when it doesn't,
			// libmspack's lzxd_decompress corrects block_remaining by the
			// overrun afterward ("did the final match overrun our desired
			// this_run length?"), since block_remaining was only
			// decremented by the (possibly too-small) nominal this_run
			// above. Skipping this was the actual bug found while
			// decoding this package's real test cabinet: without it, a
			// block boundary get misdetected one match early, and the
			// next block-header read desyncs the whole rest of the
			// bitstream from that point on (only surfacing several
			// frames later as an invalid block type).
			if overrun < 0 {
				st.blockRemaining -= -overrun
				if st.blockRemaining < 0 {
					return fmt.Errorf("cab: %w: match overran past end of block", ErrInvalidLZXData)
				}
			}
		case lzxBlockUncompressed:
			if !r.readBytes(st.window[st.windowPosn : st.windowPosn+thisRun]) {
				return fmt.Errorf("cab: %w: truncated uncompressed block", ErrInvalidLZXData)
			}
			st.windowPosn += thisRun
		}
	}

	// Realign the input bitstream to the next 16-bit coding-unit boundary
	// at the end of every frame -- per libmspack's lzxd_decompress
	// ("re-align input bitstream": "if (bits_left > 0) ENSURE_BITS(16); if
	// (bits_left & 15) REMOVE_BITS(bits_left & 15);"), unconditionally,
	// regardless of block type. This was the actual bug found while
	// decoding this package's real test cabinet: frame 0 decoded its own
	// 32768 bytes correctly (block boundaries don't require realignment),
	// but frame 1 -- continuing mid-block, with no new block header in
	// between -- silently started reading from the wrong bit position
	// without this, corrupting every byte from its very first one
	// onward.
	if r.nbits > 0 {
		r.ensure(16)
	}
	if rem := r.nbits & 15; rem != 0 {
		r.remove(rem)
	}
	return nil
}

// decodeMatches decodes literals/matches until thisRun bytes have been
// accounted for, returning the (possibly negative) leftover: a match's
// length is never split, so the last one decoded may overshoot thisRun,
// in which case the return value is negative -- see the overrun-correction
// comment at this function's one call site.
func (st *lzxCabState) decodeMatches(thisRun int) (int, error) {
	r := st.r
	for thisRun > 0 {
		mainElement, ok := st.mainDec.decode(r)
		if !ok {
			return 0, fmt.Errorf("cab: %w: bad main symbol", ErrInvalidLZXData)
		}
		if int(mainElement) < lzxNumChars {
			st.window[st.windowPosn] = byte(mainElement)
			st.windowPosn++
			thisRun--
			continue
		}
		mainElement -= lzxNumChars

		matchLength := int(mainElement) & lzxNumPrimaryLengths
		if matchLength == lzxNumPrimaryLengths {
			if st.lengthEmpty {
				return 0, fmt.Errorf("cab: %w: LENGTH symbol needed but tree is empty", ErrInvalidLZXData)
			}
			lsym, ok := st.lenDec.decode(r)
			if !ok {
				return 0, fmt.Errorf("cab: %w: bad length symbol", ErrInvalidLZXData)
			}
			matchLength += int(lsym)
		}
		matchLength += lzxMinMatch

		slot := int(mainElement) >> 3
		var matchOffset uint32
		switch slot {
		case 0:
			matchOffset = st.r0
		case 1:
			matchOffset = st.r1
			st.r1 = st.r0
			st.r0 = matchOffset
		case 2:
			matchOffset = st.r2
			st.r2 = st.r0
			st.r0 = matchOffset
		default:
			extra := uint(17)
			if slot < 36 {
				extra = lzxExtraBits[slot]
			}
			matchOffset = lzxPositionBase[slot] - 2
			if extra >= 3 && st.blockType == lzxBlockAligned {
				if extra > 3 {
					v := r.readBits(extra - 3)
					matchOffset += v << 3
				}
				asym, ok := st.alignDec.decode(r)
				if !ok {
					return 0, fmt.Errorf("cab: %w: bad aligned symbol", ErrInvalidLZXData)
				}
				matchOffset += uint32(asym)
			} else if extra != 0 {
				v := r.readBits(extra)
				matchOffset += v
			}
			st.r2 = st.r1
			st.r1 = st.r0
			st.r0 = matchOffset
		}

		if st.windowPosn+matchLength > st.windowSize {
			return 0, fmt.Errorf("cab: %w: match ran over window wrap", ErrInvalidLZXData)
		}
		if err := st.copyMatch(matchOffset, matchLength); err != nil {
			return 0, err
		}
		thisRun -= matchLength
		st.windowPosn += matchLength
	}
	return thisRun, nil
}

func (st *lzxCabState) copyMatch(matchOffset uint32, length int) error {
	dst := st.windowPosn
	if int(matchOffset) > st.windowPosn {
		j := int(matchOffset) - st.windowPosn
		if j > st.windowSize {
			return fmt.Errorf("cab: %w: match offset beyond window boundaries", ErrInvalidLZXData)
		}
		src := st.windowSize - j
		i := length
		if j < i {
			for k := 0; k < j; k++ {
				st.window[dst+k] = st.window[src+k]
			}
			dst += j
			i -= j
			src = 0
			for k := 0; k < i; k++ {
				st.window[dst+k] = st.window[src+k]
			}
			return nil
		}
		for k := 0; k < i; k++ {
			st.window[dst+k] = st.window[src+k]
		}
		return nil
	}
	src := dst - int(matchOffset)
	for k := 0; k < length; k++ {
		st.window[dst+k] = st.window[src+k]
	}
	return nil
}

// readLZXLens reads a delta-coded run of Huffman codeword lengths for
// lens[first:last] (lens itself spans the whole table; first/last are the
// absolute indices being filled this call, matching libmspack's
// lzxd_read_lens(lzx, lens, first, last)). Lengths persist and are updated
// in place across calls within one folder's decode, exactly as in CAB-LZX
// (contrast gowim/lzx, which always starts a WIM chunk's lengths at zero).
func readLZXLens(r *bitReader, lens []byte, first, last int) error {
	var preLens [lzxPretreeNumSymbols]byte
	for i := range preLens {
		preLens[i] = byte(r.readBits(4))
	}
	dec, err := newHuffTable(preLens[:], lzxPretreeTableBits)
	if err != nil {
		return err
	}

	x := first
	base := first
	get := func(i int) byte { return lens[i-base] }
	set := func(i int, v byte) { lens[i-base] = v }
	for x < last {
		z, ok := dec.decode(r)
		if !ok {
			return fmt.Errorf("cab: %w: bad precode symbol", ErrInvalidLZXData)
		}
		switch {
		case z == 17:
			y := int(r.readBits(4)) + 4
			for ; y > 0 && x < last; y-- {
				set(x, 0)
				x++
			}
		case z == 18:
			y := int(r.readBits(5)) + 20
			for ; y > 0 && x < last; y-- {
				set(x, 0)
				x++
			}
		case z == 19:
			y := int(r.readBits(1)) + 4
			z2, ok := dec.decode(r)
			if !ok || z2 > 17 {
				return fmt.Errorf("cab: %w: bad precode run symbol", ErrInvalidLZXData)
			}
			v := int(get(x)) - int(z2)
			if v < 0 {
				v += 17
			}
			for ; y > 0 && x < last; y-- {
				set(x, byte(v))
				x++
			}
		default:
			v := int(get(x)) - int(z)
			if v < 0 {
				v += 17
			}
			set(x, byte(v))
			x++
		}
	}
	return nil
}
