package pa30

import "fmt"

// Tree sizes/alphabet, per the README: the main tree covers literal bytes
// (0-255) plus non-literal match symbols; the length tree covers extended
// match lengths; the aligned-offset tree covers the low 4 bits of large
// offsets. The three trees' lengths are transmitted concatenated, 872
// (=0x368) values total, RLE-delta coded via a 39-symbol pretree.
const (
	mainTreeSize    = 0x258
	lengthTreeSize  = 0x100
	alignedTreeSize = 0x10
	totalLensSize   = mainTreeSize + lengthTreeSize + alignedTreeSize // 872
	pretreeSize     = 39
)

// blockTrees holds the three Huffman decode tables active for one
// compression-parameter block.
type blockTrees struct {
	main, length, aligned *huffmanTree
}

// parsePatchBuffer parses a PA30 patchBuffer's raw content bytes (extracted
// via readBuffer) and decodes its target-buffer content. source, if
// non-nil, is prepended to the decode-time output buffer so that DST/LRU
// back-references may reach into it (this is how real WinSxS `.manifest`
// files are actually compressed -- see doc.go); a non-empty *base rift
// table* is still rejected outright regardless of source, since this
// package does not implement block-reordering/rift-offset machinery at
// all (empirically, real `.manifest` files have an empty rift table even
// though they reference a non-empty source buffer -- the two are
// independent bits of scope).
func parsePatchBuffer(data []byte, source []byte, targetSize int) ([]byte, error) {
	br, err := newBitReader(data)
	if err != nil {
		return nil, fmt.Errorf("patch buffer: %w", err)
	}

	nonEmpty, err := br.readBit()
	if err != nil {
		return nil, fmt.Errorf("patch buffer: base rift table flag: %w", err)
	}
	if nonEmpty != 0 {
		return nil, fmt.Errorf("pa30: non-empty base rift table not supported")
	}

	blocks, blockStarts, err := readCompressionParameters(br)
	if err != nil {
		return nil, err
	}

	return decodeContent(br, blocks, blockStarts, source, targetSize)
}

// readCompressionParameters parses the "Composite Format" compression
// parameters: either a single implicit block using default Huffman
// lengths, or an explicit set of blocks each with their own RLE-delta-coded
// lengths (relative to the previous block, initially all-zero).
func readCompressionParameters(br *bitReader) ([]blockTrees, []int, error) {
	isDefault, err := br.readBit()
	if err != nil {
		return nil, nil, fmt.Errorf("patch buffer: isDefault flag: %w", err)
	}
	if isDefault != 0 {
		lens := make([]int, 0, totalLensSize)
		lens = append(lens, defaultLengths(mainTreeSize)...)
		lens = append(lens, defaultLengths(lengthTreeSize)...)
		lens = append(lens, defaultLengths(alignedTreeSize)...)
		bt, err := buildBlockTrees(lens)
		if err != nil {
			return nil, nil, err
		}
		return []blockTrees{bt}, []int{0}, nil
	}

	numBlocks, err := br.readNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("patch buffer: num_blocks: %w", err)
	}
	if numBlocks == 0 {
		return nil, nil, fmt.Errorf("pa30: num_blocks == 0")
	}

	starts := make([]int, numBlocks)
	cur := 0
	for i := range starts {
		d, err := br.readNumber()
		if err != nil {
			return nil, nil, fmt.Errorf("patch buffer: block start offset %d: %w", i, err)
		}
		cur += int(d)
		starts[i] = cur
	}

	pretreeLens := make([]int, pretreeSize)
	for i := range pretreeLens {
		v, err := br.readBits(4)
		if err != nil {
			return nil, nil, fmt.Errorf("patch buffer: pretree length %d: %w", i, err)
		}
		pretreeLens[i] = int(v)
	}
	pretree, err := buildHuffmanTree(pretreeLens, 15)
	if err != nil {
		return nil, nil, fmt.Errorf("patch buffer: pretree: %w", err)
	}

	prev := make([]int, totalLensSize) // previous block's lengths; all-zero initially
	blocks := make([]blockTrees, numBlocks)
	for b := 0; b < int(numBlocks); b++ {
		lens, err := readBlockLengths(br, pretree, prev)
		if err != nil {
			return nil, nil, fmt.Errorf("patch buffer: block %d lengths: %w", b, err)
		}
		bt, err := buildBlockTrees(lens)
		if err != nil {
			return nil, nil, fmt.Errorf("patch buffer: block %d trees: %w", b, err)
		}
		blocks[b] = bt
		prev = lens
	}
	return blocks, starts, nil
}

// readBlockLengths decodes one block's 872 concatenated tree-lengths, RLE
// (+delta) coded relative to prev via pretree-coded symbols, per the
// README's "RLE delta coding of lengths" table.
func readBlockLengths(br *bitReader, pretree *huffmanTree, prev []int) ([]int, error) {
	out := make([]int, totalLensSize)
	i := 0
	for i < totalLensSize {
		sym, err := pretree.decode(br)
		if err != nil {
			return nil, fmt.Errorf("length %d: %w", i, err)
		}
		switch {
		case sym <= 16:
			out[i] = sym
			i++
		case sym <= 19: // 17/18/19: +1/+2/+3 from previous block
			nv := prev[i] + (sym - 16)
			if nv > 16 {
				return nil, fmt.Errorf("length %d: +delta overflow", i)
			}
			out[i] = nv
			i++
		case sym <= 22: // 20/21/22: -1/-2/-3 from previous block
			nv := prev[i] - (sym - 19)
			if nv < 0 {
				return nil, fmt.Errorf("length %d: -delta underflow", i)
			}
			out[i] = nv
			i++
		case sym <= 38: // 23-30: RLE-fill last length; 31-38: RLE-copy from previous block
			lencode := (sym - 23) & 7
			var runLen int
			if lencode < 3 {
				runLen = lencode + 1
			} else {
				extra, err := br.readBits(lencode - 1)
				if err != nil {
					return nil, fmt.Errorf("length %d: RLE run bits: %w", i, err)
				}
				runLen = (1 << uint(lencode-1)) | int(extra)
			}
			if sym <= 30 {
				if i == 0 {
					return nil, fmt.Errorf("length %d: RLE fill at block start", i)
				}
				fillVal := out[i-1]
				for k := 0; k < runLen && i < totalLensSize; k++ {
					out[i] = fillVal
					i++
				}
			} else {
				for k := 0; k < runLen && i < totalLensSize; k++ {
					out[i] = prev[i]
					i++
				}
			}
		default:
			return nil, fmt.Errorf("length %d: invalid RLE symbol %d", i, sym)
		}
	}
	return out, nil
}

func buildBlockTrees(lens []int) (blockTrees, error) {
	main, err := buildHuffmanTree(lens[:mainTreeSize], maxCodeLen)
	if err != nil {
		return blockTrees{}, fmt.Errorf("main tree: %w", err)
	}
	length, err := buildHuffmanTree(lens[mainTreeSize:mainTreeSize+lengthTreeSize], maxCodeLen)
	if err != nil {
		return blockTrees{}, fmt.Errorf("length tree: %w", err)
	}
	aligned, err := buildHuffmanTree(lens[mainTreeSize+lengthTreeSize:], maxCodeLen)
	if err != nil {
		return blockTrees{}, fmt.Errorf("aligned-offset tree: %w", err)
	}
	return blockTrees{main: main, length: length, aligned: aligned}, nil
}

// decodeContent decodes the compressor bitstream into targetSize target
// bytes, using blocks[i]'s Huffman tables once the absolute output position
// (source-prefix included) reaches blockStarts[i]. If source is non-nil,
// it's used as the initial contents of the output buffer (never itself
// re-emitted; only stripped off before returning) so DST/LRU matches can
// reference into it -- this is how real WinSxS `.manifest` files actually
// decode (see doc.go). SRC/FULLSRC matches (slots 0-3) are decoded via a
// distance-based formula reverse-engineered from real msdelta.dll
// disassembly, NOT verified against any real sample yet -- see matchParams'
// doc comment in match.go for full provenance and caveats.
func decodeContent(br *bitReader, blocks []blockTrees, blockStarts []int, source []byte, targetSize int) ([]byte, error) {
	sourceLen := len(source)
	out := make([]byte, sourceLen, sourceLen+targetSize)
	copy(out, source)
	var lru [3]int
	blockIdx := 0
	for len(out)-sourceLen < targetSize {
		for blockIdx+1 < len(blocks) && len(out) >= blockStarts[blockIdx+1] {
			blockIdx++
		}
		t := blocks[blockIdx]
		targetPos := len(out) - sourceLen

		sym, err := t.main.decode(br)
		if err != nil {
			return nil, fmt.Errorf("content: at output offset %d: %w", targetPos, err)
		}
		if sym < 256 {
			out = append(out, byte(sym))
			continue
		}

		slot := (sym - 256) >> 3
		lenField := (sym - 256) & 7
		if slot == 7 {
			slot, err = expandSlot7(br)
			if err != nil {
				return nil, fmt.Errorf("content: at output offset %d: slot7 expansion: %w", targetPos, err)
			}
		}

		params, err := decodeMatchParams(br, t.aligned, slot)
		if err != nil {
			return nil, fmt.Errorf("content: at output offset %d: match params: %w", targetPos, err)
		}
		length, err := decodeLength(br, t.length, lenField)
		if err != nil {
			return nil, fmt.Errorf("content: at output offset %d: length: %w", targetPos, err)
		}

		// SRC/FULLSRC (slot <= 3): reverse-engineered from real msdelta.dll
		// disassembly, then corrected and confirmed against a full real
		// corpus -- see matchParams' doc comment in match.go for full
		// provenance and caveats. Disassembly's "target position" for the
		// sourcePos = targetPos - distance formula is measured
		// target-content-only (targetPos, this variable -- not len(out)'s
		// source-prefixed count), and the resulting sourcePos indexes
		// directly into the unified [source|target] buffer -- so the
		// distance-from-current-end offset copyMatch needs is
		// sourceLen + distance, not distance alone (offset = len(out) -
		// sourcePos = len(out) - (targetPos - distance) = sourceLen +
		// distance, since len(out)-targetPos == sourceLen). Confirmed
		// (2026-07-13) against every file (17189) in a real image's
		// `Windows\WinSxS\Manifests`, each cryptographically hash-verified
		// via DecodeWithSource's built-in target-hash check -- see TODO.md's
		// SRC/FULLSRC entry and TestDecodeWithSourceRealFULLSRCSample.
		var offset int
		switch {
		case slot <= 3:
			offset = sourceLen + params.delta
		case slot >= 4 && slot <= 6:
			offset = lru[params.lruIndex]
		default:
			offset = params.offset
		}
		if offset <= 0 || offset > len(out) {
			return nil, fmt.Errorf("pa30: at output offset %d: invalid back-reference offset %d (slot %d)", targetPos, offset, slot)
		}
		if targetPos+length > targetSize {
			return nil, fmt.Errorf("pa30: at output offset %d: match of length %d overruns TargetSize %d", targetPos, length, targetSize)
		}
		copyMatch(&out, offset, length)
		// Disassembly traced SRC/FULLSRC's dispatch arms into the same
		// LRU-update code DST/LRU-repeat matches use, so this package
		// updates the queue unconditionally here -- indirectly corroborated
		// by the same full-corpus hash-verified pass above (a wrong LRU
		// update would corrupt any later LRU-repeat match relying on it,
		// which would fail that file's hash check), though not proven
		// symbol-by-symbol.
		updateLRU(&lru, offset)
	}
	return out[sourceLen:], nil
}
