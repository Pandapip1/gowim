# TODO

Features needed to provide sufficient API surface to reimplement [nano11builder.ps1](https://github.com/ntdevlabs/nano11/blob/main/nano11builder.ps1)
(the `nano11` Windows-image debloating/ISO-building script) as a pure Go tool
on top of this repo's packages, without requiring a real Windows host,
Administrator rights, or a DISM/WOF mount. Nothing below is started.

Two items are explicitly marked "research first" — do not assume feasibility
or format before checking, the way the DriverStore hash and `DriverDatabase`
non-goals were handled in `driver`.

## WIM compression codecs & writer

`wim` currently treats compressed resources as opaque/unreadable and has no
whole-file writer; all of this is needed to read `.esd` files and to write
any compressed output at all.

- [x] Implement the XPRESS compression codec (decode + encode). Done: new
      `xpress` module, decoder+encoder both verified against real
      wimlib-produced data (embedded fixtures, no chunk-table framing).
- [x] Implement the LZX compression codec (decode + encode) — typical
      "max"-compressed resources, and `boot.wim`. Done: new `lzx` module;
      caught and fixed a real ALIGNED-block decoder bug via ground-truth
      testing against real `boot.wim` data; encoder output independently
      verified through `wimlib-imagex extract`. A second real decoder bug
      was found and fixed 2026-07-14, via a stock Windows 11 25H2 retail
      ISO's real `install.wim` (while testing an out-of-tree nano11-style
      debloat harness against it, not part of this repo):
      `bitReader.align` (used at the start of an `LZX_BLOCKTYPE_UNCOMPRESSED`
      block) was missing wimlib's documented "always discard one extra
      16-bit unit even when already aligned" quirk (`bitstream_ensure_bits`
      before `bitstream_align` in `src/lzx_decompress.c`), causing a
      desync a few chunks later on any real file whose LZX stream happens
      to hit an uncompressed block on an already-aligned boundary — see
      `lzx/README.md`'s "A second bug found and fixed" section for the full
      writeup and real-data regression test.
- [x] Implement the LZMS compression codec (decode + encode). Done: new
      `lzms` module; decoder verified against real wimlib-produced data
      (including a multi-chunk file); encoder is simpler/unverified against
      an independent decoder, an explicitly documented known limitation.
      Solid-resource packing/unpacking (the container-level, multi-blob
      framing) is intentionally deferred to the "full WIM writer" item
      below, not part of the codec itself — see `lzms/README.md`.
- [x] Wire compression codecs into `wim`'s actual read/write path (chunk-table
      framing, per-chunk raw-vs-compressed fallback, whole-resource
      raw-store fallback). Done: `wim` now depends on `xpress`/`lzx`/`lzms`
      (a deliberate, real architecture change — no longer a dependency-free
      leaf module); `Reader` transparently decompresses non-solid XPRESS/
      LZX/LZMS resources; `EncodeResourceData` compresses on the write side.
      Solid resources remain unsupported (unchanged non-goal). This work
      also caught and fixed a real bug in `lzx` (incomplete Huffman code for
      single-used-symbol alphabets), found only because real
      `wimlib-imagex` round-tripping was required, not just self-consistency.
- [x] Implement a full WIM writer/assembler that lays out resource offsets
      and writes a complete, valid WIM file from a
      `Header`+`BlobTable`+`XMLData`+metadata (today's `wim`
      package can only serialize individual components, not assemble a
      whole file). Done: `wim.WriteTo`/`wim.Assemble`, multi-image, blob
      dedup/refcount, streaming `BlobSource` abstraction; verified against
      real `wimlib-imagex info`/`apply` for all three compression types plus
      uncompressed. No integrity table, no solid resources, single-part
      only (see below/README for what's still deferred).
- [x] Implement WIM "export image" (copy one image + only its referenced
      blobs into a new WIM, optional recompression/reindexing), mirroring
      `DISM /Export-Image`. Done: `wim.ExportImage`/`ExportImageAssemble`,
      supports multiple/reordered images, recomputes blob `RefCount`s to
      reflect only the exported subset, preserves each image's original XML
      content verbatim (via `innerxml`) while renumbering indices, supports
      recompressing to a different codec than the source. Verified against
      real files plus `wimlib-imagex info`/`extract`/`verify`.
- [x] **Fixed (2026-08-19).** `lzx`'s encoder was measurably less
      space-efficient than wimlib's, by design, not by bug. Every cause
      below was eventually addressed, and the two items cause 2 originally
      deferred as "substantially bigger changes" -- a real optimal/DP parse
      and ALIGNED-offset block support -- were both subsequently
      implemented (see `lzx/optimal.go`'s `findMatchesOptimal`, run as
      `compressOptimal` alongside the lookahead parser in `encode.go`, and
      the ALIGNED/split block trials in `encode.go`/`splitstats.go`),
      together with iterative reparse refinement (`refineParseWith`),
      length-2 (hash2) matches, and statistics-driven block splitting.

      **Current status: the gap is closed and slightly reversed** --
      gowim's near-optimal path now compresses ~0.06% *smaller* than
      wimlib's own encoder (maintainer's most recent check, 2026-08-19).
      The full historical progression is preserved below rather than
      overwritten, since the intermediate measurements are what justify
      each individual fix: original +7.7% -> +5.4% (cause 1) -> +5.3%
      (cause 3) -> +4.1% (cause 4) -> parity-or-better (DP parse +
      ALIGNED + refinement).

      Two later corrections to this work are recorded further down rather
      than here: a window-boundary crash in hash2 candidate discovery
      (commit `5dadfff`), and an accidental O(arrivals) merge scan in the
      DP beam that cost 18x wall time and 147.3 GB of allocation on a
      4 MiB corpus before being bounded at insert time (commit `a63fd2c`).
      The encoder's speed/size tradeoff is now caller-selectable via
      `lzx.Options`/`CompressWith` and its preset ladder (commits
      `000effe`, `495c40b`); the DP parser that closed this gap is what
      the `Default` and `Max` rungs run, while `Fast` skips it entirely.

      Originally found while
      investigating boot.wim size reduction for the out-of-tree nano11-go
      debloat harness (2026-08-17/18, see its own TODO.md): a real stock
      Windows 11 25H2 `boot.wim`, re-exported through `wim.ExportImage`/
      `WriteTo` with the exact same compression type, chunk size, and image
      content (nothing added, removed, or otherwise changed) came out ~4%
      larger than the source file.

      **Ground-truthed (2026-08-18)** by extracting a real 12.4MB
      `ntoskrnl.exe` from a real boot.wim, splitting it into the same 398
      32768-byte WIM chunks both encoders see, and compressing every chunk
      both with this repo's `lzx.Compress` and with wimlib's own compressor
      (built from `/tmp/claude/repos/wimlib`'s `lzx_compress.c` against the
      installed `libwim.so.15`, called directly via
      `wimlib_create_compressor`, all three of its levels: 20/50/100).
      Every one of the 398 chunks came out worse under `lzx.Compress` --
      gowim total 7,169,604 bytes vs. wimlib's 6,657,082 (level 100) /
      6,666,482 (level 50, wimlib's default) / 6,860,472 (level 20, fast
      path) -- i.e. **gowim is ~4.5-7.7% larger depending which wimlib
      level you compare against**, matching the same order of magnitude as
      the whole-file finding. This is systematic, not an isolated bug.

      Three confirmed root causes, in order of impact:
      1. **[x] No repeat-offset (R0/R1/R2 LRU queue) support at all --
         fixed 2026-08-18.** `lzx/encode.go`'s `offsetSlot()` used to
         explicitly start its scan at slot 3 "since this encoder never uses
         the repeat-offset LRU queue"; `lzx/matcher.go`'s greedy parser
         never considered it either. wimlib's `struct lzx_lru_queue`
         (`wimlib/src/lzx_compress.c:1316-1374`) is used by all its
         compression levels. Fixed by having `findMatches` track the same
         3-entry recent-offsets queue the decoder already maintains (see
         `decode.go`'s `recentOffsets`), checking it directly at every
         position alongside the hash-chain search, and preferring a repeat
         match within `repeatBonus` (2) bytes of the best fresh match,
         since reusing a recent offset costs 0 extra offset bits vs. up to
         17 for a fresh one. Verified via the same real 398-chunk/12.4MB
         `ntoskrnl.exe` test as above: total dropped from 7,169,604 to
         7,017,306 bytes, a real 2.1% reduction, narrowing the gap to
         wimlib's level-100 output from +7.7% to +5.4%.

         Caution for anyone reading the history here: an earlier draft of
         this entry claimed the three pure-`0x00` chunks' exact 156-vs-78-
         byte 2x gap was the "smoking gun" for this cause. That was wrong,
         and was corrected before the fix even landed by checking
         `offsetSlot(1)` directly: a *fresh* offset of 1 already lands in
         slot 3 with 0 extra offset bits in the old encoder (only offsets
         >= 2 ever cost extra bits at all -- see `lzxExtraOffsetBits`), so
         repeat-offset tracking makes zero difference for that specific
         all-zero case (confirmed: it's still exactly 156 bytes after this
         fix). That gap is really about cause 3 below (no precode
         run-length compression) dominating when almost the entire ~496-
         symbol main alphabet goes unused. Lesson: verify a "smoking gun"
         against the actual encoder logic before writing it down, not just
         against a plausible-sounding story.
      2. **Greedy-only parsing, no lazy/near-optimal parse.** `lzx/matcher.
         go`'s `findMatches` (~line 27-122) always takes the single longest
         match at the current position via a bounded hash-chain (depth 96),
         never evaluating literal-then-later-match tradeoffs (the new
         repeat-offset check above is a cheap direct comparison, not a real
         lookahead). wimlib's near-optimal DP parser
         (`lzx_compress_near_optimal`, `wimlib/src/lzx_compress.c:301`
         struct, used above `MAX_FAST_LEVEL`=34) beats gowim even though
         gowim's chain depth (96) exceeds wimlib level 50's actual search
         depth (24, from `(24*compression_level)/50`,
         `lzx_compress.c:2875`) -- direct evidence the remaining gap is
         parsing strategy, not candidate-search breadth. Not yet
         implemented -- a real lazy or DP parse is a substantially bigger
         change than cause 1 above.
      3. **[x] No precode run-length compression -- fixed 2026-08-18.**
         `encode.go`'s `writeCodewordLens` used to send every one of a
         ~496-symbol main alphabet's codeword lengths individually via the
         precode, instead of using its run-length symbols (17/18) to
         collapse the long runs of unused (zero-length) symbols real data
         always has. wimlib's `lzx_write_compressed_code`-adjacent
         run-length handling in `compress_common.c` does this. Fixed by
         adding `codewordLenTokens` (`encode.go`), which greedily collapses
         runs of >= 4 consecutive zero-delta symbols into symbol 17 (a run
         of 4-19) or 18 (a run of 20-51), matching the read side this
         package's decoder already implemented (`decode.go`'s
         `readCodewordLens`). Symbol 19 (a short run of identical *nonzero*
         deltas) is a smaller secondary optimization, not implemented.
         Note this package still emits VERBATIM-only single-block chunks
         with no iterative cost-model feedback (only the *table
         transmission*, not the block/tree structure, changed) -- those
         remain real, smaller-value gaps folded into cause 2's scope below
         rather than reopened as a separate item.

         Verified two ways: (1) the all-zero 32768-byte chunk used as the
         original "smoking gun" (see the corrected note under cause 1)
         now compresses to exactly 78 bytes, matching wimlib's real output
         exactly (was 156) -- codified as `TestCompressAllZerosMatchesWimlibSize`.
         (2) the same real 398-chunk/12.4MB `ntoskrnl.exe` test: combined
         with cause 1's fix, total dropped from the original 7,169,604 to
         7,008,666 bytes (2.24% total reduction), narrowing the gap to
         wimlib's level-100 output from the original +7.7% to +5.3%.
      4. **[x] Greedy-only parsing -- partially fixed 2026-08-18 (one-step
         lazy matching added).** `findMatches` used to always take the
         single longest match at the current position with no lookahead.
         Added a one-step lazy parse (`matcher.go`'s `findMatches`,
         `chooseMatch`/`candidateMatch`): at each position, compute the
         best match (repeat-offset-aware, as cause 1 added), then check
         whether a strictly longer match exists one byte later using the
         *same* repeat-offset queue state (valid since a literal never
         changes it) -- if so, emit a literal now and take the better
         match next iteration. This is the same "lazy matching" technique
         real encoders like zlib's deflate and wimlib's own non-near-
         optimal levels use, not a full optimal/DP parse (wimlib's
         `lzx_compress_near_optimal`, used above `MAX_FAST_LEVEL`=34,
         remains unmatched -- see below) or an iterative bit-cost model,
         and the encoder still never emits ALIGNED-offset blocks.

         Found and fixed a real bug while implementing this: the initial
         version inserted a position's own hash-chain entry *before*
         searching for its match (needed so the lazy peek at the *next*
         position could reference the current one), which let position 0
         match against its own freshly-inserted entry at offset 0 -- an
         immediately invalid match. Any short repeated pattern reproduced
         it (`bytes.Repeat([]byte("AB"), 50)` was enough to break
         round-tripping). Fixed by computing the match *before* inserting
         the position's own hash entry; guarded by
         `TestFindMatchesNeverSelfMatchesAtPositionZero`.

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with causes 1 and 3's fixes, total dropped from the
         original 7,169,604 to 6,929,560 bytes (3.35% total reduction),
         narrowing the gap to wimlib's level-100 output from the original
         +7.7% to +4.1%. Also ran an 800-trial round-trip stress test
         (random/low-alphabet/patterned/mixed data, various sizes) with no
         failures, plus the existing full test suite.

         What was left of cause 2 as of 2026-08-18: a real optimal/DP parse
         (wimlib's `lzx_compress_near_optimal`, `wimlib/src/
         lzx_compress.c:301`) and ALIGNED-offset block support. Both were
         substantially bigger changes than the lazy-matching step above,
         and the then-remaining ~4.1% gap was attributed to these without
         being further decomposed between them.

         **Both have since been implemented** -- see this item's header for
         the current status. That attribution turned out to be correct:
         building the DP parse and ALIGNED support is what took the
         remaining ~4.1% to parity-or-better against wimlib.

      A follow-on reverse-engineering pass (2026-08-18) tried to compare
      against Microsoft's own real encoder (in `wimgapi.dll`, not just
      wimlib) to see whether it does anything wimlib doesn't -- see the
      "Further LZX encoder optimizations" list right below for what that
      found and why it didn't change the plan: confirmed wimgapi.dll
      implements LZX itself (not delegated to Cabinet.dll/ntdll) with the
      exact same offset-slot table, block-type constants, and E8-filter
      constant as documented LZX/gowim; traced its LZX coder-object down to
      an unambiguous bitstream *reader* refill routine
      (`setup_wimgapi.dll+0x180029660`) -- i.e. found the real decompressor,
      not the compressor. The actual encoder was not located even with
      radare2 (r2ghidra decompilation was attempted but doesn't build
      against the installed r2 5.5.0 -- real API breakage in r2ghidra's
      source, not a config issue) within the effort spent; whether MS's own
      parse strategy beats wimlib's near-optimal DP parser remains
      genuinely unanswered.

      **Further LZX encoder optimizations (2026-08-18, second round, from
      first-principles brainstorming rather than reference-implementation
      comparison):**
      1. **[x] Cost-aware match/offset selection.** Replaced the old flat
         `repeatBonus` heuristic (see cause 1 above) with a real (if
         approximate) bit-cost comparison: `costModel.matchValue` in
         `matcher.go` estimates `length*8 - matchCost - extraOffsetBits`
         for every candidate (each repeat-offset slot and the best fresh
         match), so a shorter match at a much cheaper offset can now beat a
         longer one at an expensive offset, not just "repeat within N
         bytes of fresh." Guarded by `TestCostModelPrefersCheaperOffset`.
      2. **[x] Two-pass Huffman refinement.** `compress()` in `encode.go`
         now parses twice: pass 1 uses `costModel{}`'s flat per-symbol bit
         estimates (no real Huffman table exists yet); its resulting token
         frequencies build a first Huffman table, which becomes pass 2's
         cost model for a refined re-parse against this chunk's *actual*
         codeword-length costs. This is the standard two-pass technique
         approximating joint parse/code optimization without a full
         iterative optimal parser. Literal cost stays at the flat estimate
         in both passes (only match cost is refined) since it varies far
         less than match cost's 0-17 extra-bit range -- see `costModel`'s
         doc in `matcher.go` for the reasoning.

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with items 1+2, total dropped from 6,929,560 to
         6,864,532 bytes (a further 0.94% reduction), narrowing the gap to
         wimlib's level-100 output from +4.1% to +3.12%. Also ran an
         800-trial round-trip stress test with no failures, plus the full
         existing suite.
      3. **[x] ALIGNED-block trial.** `compress()` now encodes each chunk
         both as VERBATIM and ALIGNED (`encodeBlock` in `encode.go`,
         parameterized by block type; `buildAlignedTable` computes the
         8-symbol aligned Huffman code from the low 3 extra-offset bits of
         every fresh match at slot >= `minAlignedOffsetSlot`) and keeps
         whichever encodes smaller. No cost model or estimation needed here
         -- the main/length trees are identical either way, so directly
         comparing the two real encoded sizes is exact, not approximate.
         Guarded by `TestAlignedBlockCanBeSmaller` (synthetic skewed-
         low-bits data where ALIGNED provably wins).

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with items 1-3, total dropped from 6,864,532 to
         6,850,540 bytes (a further, smaller 0.20% reduction -- real WIM
         content apparently doesn't have as skewed a low-offset-bit
         distribution as the synthetic test case, so the win is modest but
         real), narrowing the gap to wimlib's level-100 output from +3.12%
         to +2.91%.
      4. **[x] Better match finder (binary-tree/BST instead of hash-chain).**
         `matcher.go`'s `findMatches` used a simple hash-chain (recency-
         ordered linked list per hash bucket, walked up to `maxChainLen`
         deep). Replaced with a binary search tree per hash bucket
         (`bstSearch`/`bstInsert`, the standard "insert while descending"
         BST match finder used by real "bt"-family match finders, e.g. the
         LZMA SDK's bt4): candidates are ordered by lexicographic
         comparison of their suffixes rather than recency, so within the
         same bounded comparison budget the tree can discard whole
         subtrees known to be on the wrong side of a comparison instead of
         walking a flat list. Read-only search (for both the real
         per-position lookup and the lazy-matching peek) and the actual
         tree insertion are now separate traversals (`bstSearch` never
         mutates; `bstInsert` does) since the earlier hash-chain's combined
         "insert-then-search-would-self-match" hazard (see cause 4 above,
         `TestFindMatchesNeverSelfMatchesAtPositionZero`) made clear that
         these two concerns need to stay cleanly separated. Guarded by a
         new `TestFindMatchesBSTFindsGlobalBestWithinSmallBuffer`, which
         checks every reported match against a brute-force reference for
         both "is this a real match at all" and "is this actually the
         longest available match" (within a small-enough buffer that the
         depth budget can't be exhausted before finding the true best).

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with items 1-4, total dropped from 6,850,540 to
         6,843,596 bytes (a further, small 0.10% reduction), narrowing the
         gap to wimlib's level-100 output from +2.91% to +2.80%. Real cost:
         encode time for the whole file roughly doubled (~2.0s to ~3.3s)
         since search and insertion became separate tree traversals instead
         of one combined chain walk -- still fast enough in absolute terms
         (13MB in ~3.3s) for this project's offline debloat-tool use case,
         but a real tradeoff worth knowing about, not a free win.
      5. **[x] Bounded lookahead beyond one-step lazy matching -- scoped
         down from "full optimal/DP parse" deliberately.** A real
         optimal/DP parse (wimlib's `lzx_compress_near_optimal`) needs to
         explore the combinatorics of every repeat-offset-queue state
         reachable at every position in the chunk -- genuinely complex, and
         risky to get right given how easy it already proved to introduce a
         real self-match bug in a much simpler change (cause 4's
         `TestFindMatchesNeverSelfMatchesAtPositionZero`). Given the
         diminishing returns already measured on similar-effort steps above
         (item 4: 0.10% for a 65% time increase), attempting a full DP was
         judged not worth the risk without first checking whether a much
         cheaper, bounded generalization of lazy matching captured most of
         the value.

         Implemented instead: `findMatches` now evaluates three options at
         every position -- the best repeat-offset candidate, the best
         fresh-offset candidate, and "emit a literal" -- each combined with
         its own single, non-recursive 1-step continuation value (not a
         further nested lookahead, so this stays a fixed depth-2
         evaluation, never a whole-chunk search), and commits whichever
         totals highest. This generalizes one-step lazy matching (which
         only ever compared a single pre-picked best candidate against
         "literal, then re-decide") to comparing each *kind* of candidate's
         own continuation independently -- catching cases where taking the
         repeat candidate now, even at a lower immediate value than the
         fresh candidate, sets up a better continuation that a single
         best-of-both comparison would never consider.

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with items 1-5, total dropped from 6,843,596 to
         6,818,710 bytes (a further 0.36% reduction -- notably a better
         size-per-time-cost ratio than item 4's binary-tree change),
         narrowing the gap to wimlib's level-100 output from +2.80% to
         +2.43%. Real cost: whole-file encode time increased only
         modestly, ~3.3s to ~3.67s (~11%), much less than the naive
         worst-case estimate (evaluating 3 options x their own
         sub-searches could have cost far more) -- the bounded scope-down
         from a full DP paid off in practice, not just in reduced risk.
         A full optimal/DP parse with real repeat-offset-state exploration
         remains unimplemented; this bounded lookahead is presented
         honestly as a partial step, not a substitute for it.
      6. **[x] Precode symbol 19 (short runs of identical nonzero deltas).**
         `codewordLenTokens` in `encode.go` now also collapses a run of 4-5
         consecutive *equal nonzero* deltas into one symbol-19 token
         (itself plus the shared delta value, which is separately precode-
         encoded -- see decode.go's `readCodewordLens`, case 19, already
         implementing the read side) instead of 4-5 individual symbols.
         Operates purely on the already-computed delta array, so it's
         correct regardless of what the previous-block baseline actually
         was (all-zero for a chunk's first/only block, or the prior
         block's real lengths for a second block -- see item 7 below,
         which added that second case after this one was written). Guarded
         by `TestCodewordLenTokensUsesSymbol19` (direct
         token-level check) and `TestRoundTripExercisesSymbol19Indirectly`
         (a real encode/decode round-trip on data likely to trigger it).

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with items 1-6, total dropped from 6,818,710 to
         6,818,486 bytes -- a negligible 224-byte (~0.003%) reduction,
         exactly matching the prediction that this was the smallest-value
         item on the list (most of the precode's real-world saving was
         already captured by symbols 17/18 in an earlier commit; real main-
         tree codeword lengths apparently don't have many runs of 4-5
         *identical nonzero* values back to back). Implemented anyway per
         explicit instruction to go down the full list; kept since it's
         correctness-verified and adds negligible risk or complexity.
      7. **[x] Multi-block-per-chunk splitting (bounded, single split
         point).** `compress()` now also tries encoding the chunk as 2 LZX
         blocks instead of 1 (`trySplitChunk` in `encode.go`), splitting at
         the token boundary closest to the chunk's midpoint (never inside
         a token, since matches may not cross a block boundary -- see
         decode.go's `lzCopy`), each block with its own independently-built
         Huffman tables and its own VERBATIM-vs-ALIGNED trial, and keeps
         the split only if it comes out smaller than the single-block
         encoding. Required generalizing `encodeBlock` into a standalone
         wrapper plus a shared `writeBlockInto` core that can write
         multiple blocks into one continuous bitstream (LZX blocks are not
         byte-aligned relative to each other -- only an UNCOMPRESSED block
         realigns), with the second block's codeword-length tables
         delta-coded against the *first* block's real lengths rather than
         an all-zero baseline (previously this package only ever emitted
         one block per call, so `writeCodewordLens` was always called
         against an all-zero baseline; item 6 above was written before
         this generalization and has been corrected to reflect it).

         This tries exactly one split point, not a general search over
         every possible number/position of blocks -- real near-optimal
         encoders decide splits via an iterative cost-based search across
         many candidate boundaries, a substantially bigger undertaking
         than justified without first checking whether even one bounded
         split point captures a meaningful share of the benefit. Guarded
         by `TestSplitChunkRoundTrips` and
         `TestTrySplitChunkProducesValidSplit` (deliberately mixed
         repetitive-text/high-entropy-random data, to both exercise a real
         split decision and confirm round-trip correctness either way).

         Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
         combined with items 1-7, total dropped from 6,818,486 to
         6,815,102 bytes -- a further, modest 0.05% reduction, roughly in
         between items 4 and 6 in value (small, as predicted up front,
         since most real WIM chunks don't have statistics that vary sharply
         enough mid-chunk to be worth a second block's header overhead,
         but not as negligible as symbol 19). Real cost: whole-file encode
         time increased a further ~8% (~3.67s to ~3.96s) from the extra
         per-chunk split-trial encoding work.

      **Summary across all 7 items (2026-08-18):** starting from the
      3-cause investigation's already-fixed 7,169,604 bytes, the real
      398-chunk/12.4MB `ntoskrnl.exe` test now produces 6,815,102 bytes --
      a cumulative 4.94% reduction from these 7 items alone (12.4%
      smaller than the very first, unoptimized measurement), narrowing the
      gap to wimlib's level-100 output from the original +7.7% to +2.37%.
      Whole-file encode time grew from roughly 1s to roughly 4s across all
      7 items combined -- a real, honestly-reported cost, still fast
      enough in absolute terms for this project's offline debloat-tool use
      case. The single biggest remaining lever, per items 2/4's own
      write-ups, is a full optimal/DP parse with real repeat-offset-state
      exploration (wimlib's `lzx_compress_near_optimal`) -- deliberately
      not attempted here given the risk/complexity judged not worthwhile
      relative to the diminishing returns already measured on cheaper,
      bounded steps (items 4 and 6 in particular).

      **Item 8 (2026-08-18, attempted anyway after the above): a bounded
      DP parse, deliberately NOT wimlib's full near-optimal parser.**
      `findMatchesOptimal` (`optimal.go`) is a forward shortest-path DP
      over the whole chunk using the same binary-tree match finder, but
      tracks a SINGLE repeat-offset queue trajectory per position (the
      queue state belonging to whichever predecessor gives the minimum
      cost to reach that position) rather than exploring every reachable
      queue-state hypothesis the way wimlib's `lzx_compress_near_optimal`
      does -- a real, acknowledged approximation: a slightly costlier path
      to some position might in principle carry a queue state enabling a
      much cheaper match afterward, and this DP would never discover that
      trade-off. Edge counts are explicitly bounded per position
      (`repeatLengthSamples`: full length + one midpoint length per
      repeat-offset candidate, not every length from `minMatchLen` up;
      `maxFreshCandidates`: at most 8 Pareto-frontier fresh-offset
      candidates from the binary-tree walk) specifically to avoid
      unbounded worst-case cost on highly repetitive input, given this
      project's own already-documented performance sensitivity on
      multi-GB real WIM files (see "Performance: concurrency
      opportunities" below). Wired into `compress()` as an additional
      "try it, keep it only if smaller" candidate alongside the existing
      bounded-lookahead/ALIGNED/split candidates (not a replacement --
      the single-queue-trajectory approximation is not guaranteed to beat
      the bounded lookahead on every input).

      **A real bug was found and fixed in the test harness while
      verifying this, not in the DP itself** -- worth recording since it
      cost real debugging time: an early ad hoc round-trip check compared
      the decoder's output against the *preprocessed* (post-E8-filter)
      buffer instead of the true original data. `Decompress`/`decompress`
      always reverses the E8 filter internally when it sees a literal
      0xE8 symbol in the main tree, so that comparison was structurally
      wrong regardless of encoder correctness -- it happened to surface
      only for inputs where a real (or coincidental, since the filter is
      unconditional and does not check for genuine x86 code) 0xE8 byte
      occurred, which is exactly why it looked like a rare, DP-specific
      correctness bug at first. Diagnosed by: (1) verifying tokens are
      real matches with full coverage (passed -- ruled out the DP itself
      producing invalid matches), (2) adding a temporary decode-side
      trace hook and confirming the decoded event sequence matched the
      token list exactly (passed -- ruled out an encode/decode bitstream
      desync), which left only the test's own comparison buffer as the
      remaining suspect, per this project's standing discipline of
      verifying against real, independently-checked ground truth at each
      step rather than guessing at the next hypothesis. `TestFindMatches
      OptimalRoundTrips`/`TestFindMatchesOptimalStress` (200-trial) now
      encode this lesson directly: they always compare against the true
      original data, never the preprocessed intermediate.

      Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
      combined with items 1-7 and this DP trial, total dropped from
      6,815,102 to 6,752,430 bytes -- a further 0.92% reduction, notably
      the best single-item size win of this whole second round (items 4,
      6, and 7 each landed under 0.4%). Cumulatively: 7,169,604 down to
      6,752,430 bytes (5.82% total reduction from the post-3-cause
      baseline), narrowing the gap to wimlib's level-100 output from the
      original +7.7% down to **+1.43%**. Real, honestly-reported cost:
      whole-file encode time roughly doubled again on top of the other 7
      items, from ~4s to ~7.1s for this 13MB test file (~1.8MB/s
      throughput) -- confirming the up-front concern that a DP-based pass
      is a real performance tradeoff, not a free win, though the
      size-per-time-cost ratio was still better than items 4-7
      individually delivered. Given this project's own documented
      performance sensitivity on multi-GB real WIM images (several
      minutes single-threaded even before this change -- see "Performance:
      concurrency opportunities" below), anyone wiring this up for bulk
      real-world use should weigh that cost against the 0.92% marginal
      size win for their own use case; it is left wired in by default
      here since gowim's own test suite and this project's own explicit
      request prioritized "try it and measure honestly" over pre-emptively
      gating it behind a flag.

      What remains unclosed (per the single-queue-trajectory
      approximation and the bounded edge counts above): a fully faithful
      multi-state DP matching wimlib's own `lzx_compress_near_optimal`
      exactly. Whether that would close most of the remaining ~1.43% gap
      or only a small further slice of it is genuinely unknown -- unlike
      every other step in this investigation, this was not measured,
      since it was not attempted.

      **Item 9 (2026-08-18, tried anyway after the above): a bounded
      multi-state beam DP, replacing item 8's single-queue-trajectory
      simplification.** `findMatchesOptimal` (`optimal.go`) was rewritten
      to track up to `beamWidth` (4) distinct (cost, queue-state)
      hypotheses per position instead of only the single cheapest
      arrival, closer in spirit to wimlib's `lzx_compress_near_optimal`
      exploring multiple live queue-state hypotheses -- a higher-cost
      path that carries a different, more useful queue state is no
      longer discarded outright, as long as it's among the 4 cheapest
      distinct-queue arrivals at that position. Mechanically: `states[pos]`
      is now a slice of `(cost, queue, edge)` entries instead of a single
      `cost[pos]`/`queueAt[pos]` pair; `mergeState` folds a new arrival in
      (replacing an existing same-queue entry only if cheaper, else
      appending), and `pruneState` trims to the `beamWidth` cheapest
      entries once a position's incoming edges are all known (guaranteed
      once the forward scan reaches that position as `i`, since all
      edges go strictly forward). Path reconstruction walks back through
      `(from position, fromState index)` pairs rather than a flat
      position chain. This is still explicitly NOT wimlib's own parser --
      wimlib's near-optimal parser is not beam-limited in the same way
      and explores a larger edge set per position -- so the doc comment
      in `optimal.go` was rewritten to describe this honestly as a
      bounded beam-search approximation, not "wimlib's algorithm."
      Guarded by the same `TestFindMatchesOptimalRoundTrips` and
      `TestFindMatchesOptimalStress` (200-trial) tests as item 8, both of
      which passed unmodified against the new multi-state implementation
      (no test changes were needed since they only check tokens are real
      matches, cover the whole chunk, and round-trip against the true
      original data -- properties the beam version preserves).

      Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test
      (re-extracted fresh from the same `boot.wim` this round, and a
      fresh wimlib-imagex LZX:100 capture of the same file for the
      reference number, rather than reusing item 8's cached figures):
      total dropped from 6,752,430 to 6,731,684 bytes -- a further 0.31%
      reduction, smaller than item 8's own 0.92% win but a genuine further
      improvement, not noise (the beam DP's tokens still round-trip
      correctly across the full stress suite). Cumulatively: 7,169,604
      down to 6,731,684 bytes (6.11% total reduction from the post-3-cause
      baseline). The freshly-measured wimlib LZX:100 reference for this
      same file came out to 6,659,122 bytes (vs item 8's previously-cited
      6,657,082 -- the ~2KB difference is attributed to this being an
      independent re-capture, not a regression in either encoder), putting
      the gap at **+1.09%**, down from item 8's +1.43%. Real, honestly-
      reported cost: whole-file encode time roughly doubled again on top
      of item 8, from ~7.1s to ~14.1s for this 13MB test file (~0.9MB/s
      throughput) -- the beam width directly multiplies the DP's inner-loop
      work (4x the states, each considering the same edge set), and this
      is exactly that cost showing up as measured, not estimated.

      Diminishing returns are now visible within the DP-parse line of
      work itself, not just across the whole 9-item list: item 8 (single
      trajectory to unbounded-fresh-length exploration) bought 0.92% for
      roughly a 2x time cost; item 9 (single trajectory to 4-way beam)
      bought a further 0.31% for another roughly 2x time cost. Whether a
      wider beam (8, 16...) continues that same pattern of shrinking
      returns per doubling, or whether beam width 4 happened to sit past
      most of the real benefit already, was not tested -- doing so would
      be the natural next step if this line of work continues, rather
      than guessing which is true.

      What remains unclosed, same as item 8: a fully faithful,
      unbounded-state DP matching wimlib's own `lzx_compress_near_optimal`
      exactly (no beam-width cap, and a larger edge set per position).
      Whether that would close most of the remaining ~1.09% gap or only a
      small further slice is still genuinely unknown, for the same reason
      as before -- it has not been attempted, only approximated at
      increasing (and increasingly expensive) levels of fidelity.

      **Correction (2026-08-18) to items 8/9's characterization of
      wimlib's algorithm, found by actually reading wimlib's source
      (`src/lzx_compress.c`, cached at `/tmp/claude/repos/wimlib`) rather
      than continuing to assume:** wimlib's `lzx_find_min_cost_path` does
      **not** explore multiple repeat-offset-queue-state hypotheses. It
      tracks a single queue trajectory per position, exactly like item
      8's original (pre-beam) approach -- its own doc comment says so
      explicitly: "the way the algorithm handles this adaptive state... is
      actually only an approximation... The algorithm does not solve this
      problem in general; it only looks one step ahead." Items 8 and 9's
      write-ups above, and `optimal.go`'s doc comments at the time, wrongly
      asserted wimlib explores every reachable queue state -- that was
      never verified against the real source before being written, which
      is exactly the kind of unverified claim this project's own standing
      discipline says not to make. It has been corrected here and in
      `optimal.go`'s doc comment. What wimlib actually does differently
      from item 8's original single-trajectory version: relaxes *every*
      match length (not a sampled subset) for both repeat-offset and
      fresh-offset candidates, since each extra length is an O(1) cost
      lookup once a match is confirmed; folds ALIGNED-block cost estimates
      directly into the per-position cost model (`CONSIDER_ALIGNED_COSTS`)
      instead of only comparing finished encodings after the fact; and
      runs multiple full refinement passes per block (up to 4 at
      compression level 100, each re-deriving costs from the previous
      pass's actual chosen path), not just 2.

      **Item 10 (2026-08-18): implemented all three of those real
      differences, measured them, and reverted all three after the real
      398-chunk/12.4MB `ntoskrnl.exe` test showed each one made total
      output *larger*, not smaller** -- a genuine, reproducible, measured
      regression, not noise (verified by isolating each change in its own
      run rather than changing all three at once):
        - Baseline (item 9, sampled lengths, no inline aligned cost, 1
          pass): 6,731,684 bytes.
        - + exhaustive per-length relaxation only: 6,734,060 bytes (worse).
        - + inline aligned-cost model on top of that: 6,735,646 bytes
          (worse again).
        - + a second refinement pass on top of both: 6,742,596 bytes
          (worse again).

        Each addition made the measured result monotonically worse, in
        the same direction, across three independent runs -- not a
        one-off fluke. The likely reason (plausible, not fully proven):
        the DP's cost model is only ever an *estimate* (from a previous
        pass's Huffman lengths), and the final real size depends on the
        actual Huffman table built from whatever tokens the DP ends up
        choosing. Giving the DP more freedom to chase small estimated
        savings can shift the chosen token distribution in ways that make
        the *final, real* Huffman code measurably less efficient overall,
        even though every individual relaxation was "cheaper" under the
        (imperfect) model it was evaluated against. This is a real
        illustration of why wimlib's own comment above calls its result
        "not guaranteed to be the true minimum cost path" -- greedily
        trusting a cost estimate further does not necessarily help once
        that estimate feeds back into what code gets built from it. All
        three changes were reverted (`git checkout -- lzx/encode.go
        lzx/matcher.go lzx/optimal.go`, back to item 9's committed state,
        `d657f3a`) rather than kept as a net-worse "closer to wimlib"
        change; gowim's own measured, working 6,731,684-byte result
        remains the current best.

        One genuinely interesting side effect surfaced during this
        experiment, worth recording even though the code that produced it
        was reverted: with exhaustive-length relaxation active, the
        existing `TestCompressAllZerosMatchesWimlibSize` regression guard
        *failed* -- not because the output was wrong, but because it
        dropped to 76 compressed bytes for the all-zero 32768-byte test
        chunk, 2 bytes *smaller* than wimlib's own real 78-byte output for
        the same input, and it still round-tripped correctly (verified
        directly against `Decompress`). So gowim's encoder is not
        strictly bounded above by wimlib's own output even today, on at
        least this one synthetic input -- interesting, but not something
        to chase further without a concrete real-world case motivating it,
        since generalizing from a single synthetic all-zero chunk to a
        real-world size claim would itself be the kind of unverified
        extrapolation this project avoids.

        What remains unclosed, same as items 8/9: whether a *properly
        converging* multi-pass refinement (wimlib manages 4 passes at
        level 100 without regressing, so a well-behaved version of this
        must be possible) would help once implemented correctly, versus
        this session's single extra pass which measurably did not. That
        would require understanding why wimlib's iteration converges and
        this session's naive one-shot rebuild didn't -- a real, specific
        follow-up question, not yet investigated.

      **Item 11 (2026-08-18): real per-literal costs in `findMatches`'
      match-value comparison, replacing a flat 8-bits-per-literal guess
      that this package's own doc comment had (wrongly, it turns out)
      called a safe simplification.** Checking real byte-frequency data
      from the same `ntoskrnl.exe` test file (a representative 32768-byte
      chunk) showed a common padding byte at ~20% frequency (~2.3 bits of
      real entropy) alongside rare bytes at ~0.003% (~15 bits) in the same
      chunk -- not "little variation," directly contradicting
      `matcher.go`'s prior doc comment justifying the flat estimate.
      `costModel.matchValue` (used by `findMatches`, the bounded-lookahead
      parser) was always comparing a match's cost against
      `length*flatLiteralBits` for the literals it would replace,
      regardless of whether real Huffman lengths were already known from
      pass 1 -- so a match's real value was systematically mispriced by
      however far the true literal costs in its range departed from a
      flat 8 bits. Fixed by adding `costModel.literalCost(b)` (real
      Huffman length when known, else the flat estimate -- the same
      pattern `findMatchesOptimal` already used correctly) and a
      `litPrefix` running-sum array so any match's real summed literal
      cost is an O(1) lookup, then threading that through `matchValue` as
      an explicit `litCost` argument instead of computing
      `length*flatLiteralBits` internally.

      This is a different kind of change from item 10's regression: it
      does not add any new edges or exploration (same candidates
      considered, same number of passes) -- it only corrects the accuracy
      of an existing comparison already being made, so it does not carry
      item 10's "more freedom under an approximate model can backfire"
      risk. Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe`
      test: 6,731,684 -> 6,730,928 bytes, a small but real 756-byte (0.011%)
      further reduction, with no measurable time cost (unchanged ~14s,
      since no extra passes or edges were added) -- narrowing the gap to
      wimlib's fresh LZX:100 reference (6,659,122 bytes) from +1.09% to
      **+1.08%**. Modest, as expected, since `findMatches` is only one of
      several candidates `compress()` tries per chunk and is often not the
      one actually kept; the same fix was not needed in
      `findMatchesOptimal`, which already used real per-literal costs via
      its own `literalCost` closure.

      **Item 12 (2026-08-18): more refinement passes alone, tried and
      reverted -- isolates a variable item 10 had conflated with others.**
      Item 10's regression combined three changes at once (exhaustive
      lengths, inline aligned costs, an extra DP pass); this tested "just
      more passes" alone, on `compressLookahead` specifically, with
      nothing else changed: after the existing 2-pass parse, up to 6
      additional passes were run, each re-parsing using the *previous*
      pass's own real Huffman lengths as its cost model (mirroring
      wimlib's `lzx_optimize_and_flush_block`, which does up to 4 total
      passes at compression level 100). Measured on the same real
      398-chunk/12.4MB `ntoskrnl.exe` benchmark, starting from
      6,730,928 bytes (post-item-11):
        - +2 extra passes (4 total): 6,730,902 bytes -- a 26-byte
          "improvement," statistically noise on a 6.7-million-byte total.
        - +6 extra passes (8 total): 6,730,730 bytes -- 198 bytes better
          than baseline (0.003%), for a real ~19% time increase (9.8s to
          11.7s on this benchmark).

      So: not a regression like item 10, but not a real lever either --
      the token-frequency-based cost model this package uses converges to
      essentially the same result within 1-2 passes, and pushing further
      buys only fractions of a byte per chunk at real added cost. This
      narrows down *why* wimlib's own multi-pass refinement helps more for
      wimlib than for this package: it's likely not the pass *count*
      itself, but something else combined with it in wimlib's design (the
      fractional-bit, frequency-calibrated initial cost model described in
      item 8/9's write-up, its real per-position aligned-cost weighting,
      or its different match-finder/candidate set) -- none of which this
      isolated test changed. Reverted back to the committed 2-pass
      baseline; not kept even as a flag, since there's no measured case
      where it's worth its cost here.

      **Where this leaves the remaining ~1.08% gap:** every wimlib-
      inspired hypothesis tested so far in this investigation -- queue-
      state trajectory count (item 9's correction), per-length
      exhaustiveness, inline aligned costs, extra refinement passes (this
      item) -- has come back negative or negligible when isolated and
      measured on real data, not just assumed. What has NOT been tried:
      (1) wimlib's real block-splitting heuristic (`lzx_init_block_split_
      stats`), a genuinely different, statistics-driven approach vs. this
      package's single bounded-midpoint split attempt; (2) a direct,
      position-by-position comparison of which matches wimlib's own
      match-finder actually finds vs. gowim's, to check whether the
      remaining gap is a match-*discovery* problem (hash quality, tie-
      breaking) rather than a match-*selection* problem (cost model,
      parse strategy) -- everything tested in this investigation so far
      has been the latter. Neither has been attempted; both are real,
      specific, checkable next steps, not vague future work.

      **Item 13 (2026-08-18): wimlib's real, statistics-driven block-
      splitting heuristic, implemented and kept -- a genuine, if modest,
      win.** Read wimlib's actual `lzx_should_end_block`/
      `lzx_observe_literal`/`lzx_observe_match`/`lzx_init_block_split_
      stats` (`src/lzx_compress.c`) directly rather than guessing, and
      found real block-splitting there is a fundamentally different,
      richer mechanism than this package's single bounded-midpoint
      attempt (`trySplitChunk`, item 7): wimlib buckets each literal into
      1 of 8 types (top 2 bits + low 1 bit) and each match into 1 of 2
      types (short/long, threshold length 5), and every 400 such
      observations (`NUM_OBSERVATIONS_PER_BLOCK_CHECK`), compares the
      newly-accumulated bucket distribution against the block's
      cumulative distribution so far; if they differ by at least 7/8 of
      the expected total (computed with the same cross-multiplied
      integer arithmetic wimlib uses, to stay faithful rather than
      "cleaner but different"), it ends the current block right there.
      This can produce zero, one, or several split points per input,
      driven by real content shifts rather than position alone. Ported
      byte-faithfully as `lzxBlockSplitPoints` (new file
      `lzx/splitstats.go`), using wimlib's own `MIN_BLOCK_SIZE` (6500) as
      the minimum-gap requirement from both the start and end of the
      chunk; wimlib's separate soft-max-block-size (100,000 bytes) and
      match-cache-overflow triggers don't apply here, since a WIM chunk
      (32768 bytes) is always under wimlib's own soft max and this
      package holds the whole chunk in memory already.

      `trySplitChunkStats` generalizes `trySplitChunk`'s single-split
      machinery to an arbitrary number of split points: each resulting
      segment gets its own Huffman tables and its own independent
      VERBATIM-vs-ALIGNED decision, chained via the existing
      `writeBlockInto` (each subsequent segment's tables delta-coded
      against the *previous* segment's real lengths, not an all-zero
      baseline). Wired into `compressLookahead` as a fourth candidate
      alongside VERBATIM/ALIGNED/`trySplitChunk`, run concurrently with
      them (same "try independent candidates, keep smallest" pattern as
      every other candidate in this encoder). Guarded by
      `TestLzxBlockSplitPointsFindsRealShift` (verifies at least one
      split point is found for data with a real, sharp content shift --
      pseudo-random ASCII text into pure random bytes, chosen specifically
      because a *repeated phrase* produces too few actual observations to
      exercise the heuristic at all, having collapsed into a handful of
      very long matches -- an early version of this test used repeated
      text and silently found zero splits for exactly that reason, caught
      by checking the split count rather than assuming it worked),
      `TestTrySplitChunkStatsProducesValidSplit` (direct round-trip
      guard), and `TestTrySplitChunkStatsRoundTripsThroughCompress` (full
      `Compress()`/`Decompress()` path).

      Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test:
      6,730,928 -> 6,729,686 bytes -- a real but modest 1,242-byte
      (0.018%) further reduction, no measured time regression (runs
      concurrently with the other three lookahead candidates). Narrows
      the gap to wimlib's LZX:100 reference (6,659,122 bytes) from +1.08%
      to **+1.06%**. Modest, as expected: a 32768-byte WIM chunk can fit
      at most a handful of wimlib-style splits (each segment needing >=
      6500 bytes), so there's an inherent ceiling on how much this lever
      alone can move the needle within gowim's chunk-at-a-time
      architecture, unlike wimlib's own much larger (up to 100,000-byte)
      real blocks. Still a genuine, real-content-driven win, not the
      negative/negligible result of items 10 and 12 -- this was a
      different lever (block layout) from those (parse/cost-model
      strategy), which is exactly why it paid off where they didn't.

      What remains unclosed, same as before this item: a direct,
      position-by-position comparison of which matches wimlib's own
      match-finder actually finds vs. gowim's, to check whether the
      remaining ~1.06% gap is now more a match-*discovery* problem than a
      match-*selection* or block-*layout* problem -- still not attempted.

      **Item 14 (2026-08-18): the match-discovery comparison above was
      finally done for real, against wimlib's own compiled matchfinder --
      and it surfaced a serious, real, pre-existing decoder bug that had
      nothing to do with match discovery at all.**

      **Part A -- the actual match-discovery comparison.** Built a
      standalone C driver (`/tmp/claude/mfcompare`, not part of this repo)
      against wimlib's real `include/wimlib/bt_matchfinder.h` directly
      (self-contained enough to compile with just `-Iinclude`; no need to
      build all of wimlib), configured with the exact level-100 parameters
      read from `src/lzx_compress.c` (`max_search_depth=48,
      nice_match_length=96`, plus `BT_MATCHFINDER_HASH2_ORDER=12`, which
      wimlib defines before including the header to enable length-2 match
      detection). Ran it against a real 32768-byte chunk of the same
      `ntoskrnl.exe` used throughout this investigation, and separately
      dumped gowim's own per-position best match via a temporary test-only
      port of `findMatches`' exact hash+BST logic. Comparing the two
      position-by-position across all 32768 positions:
        - 0 positions where wimlib's best match was strictly longer than
          gowim's own (whenever gowim found anything at or above its own
          minMatch=3 floor).
        - 523 positions where gowim's match was strictly longer than
          wimlib's.
        - 25,760 positions (78.6%) where both found the exact same length.
        - The entire remaining gap -- 3,470 positions (10.6%) where
          wimlib found a match gowim didn't -- was **100% attributable to
          matches of length exactly 2**, which gowim's fresh-match finder
          cannot find at all: its hash reads 3 bytes (`minMatch=3`), so a
          position whose only match with an earlier position is 2 bytes
          long never lands in the same hash bucket as that earlier
          position in the first place. wimlib's separate `hash2_tab`
          (single-slot-per-bucket, no tree -- only the most recent
          occurrence of a 2-byte prefix ever matters, since for equal
          length a closer offset is never worse) exists specifically to
          catch this.

        Conclusion: gowim's own match discovery is not the bottleneck --
        if anything it's *better* than wimlib's real matcher at every
        length wimlib can find. The one real, confirmed, structural gap is
        length-2 fresh matches.

      **Part B -- implementing the length-2 fix surfaced a real, separate,
      serious bug.** Added a `hash2`-equivalent (single-slot 12-bit table,
      matching wimlib's `BT_MATCHFINDER_HASH2_ORDER`) to `findMatches`, and
      a round-trip test (`TestTrySplitChunkStatsProducesValidSplit`, using
      pseudo-ASCII text shifting into random bytes -- chosen because a
      *repeated* phrase collapses into too few actual match-finder
      "observations" to exercise anything, a lesson from item 13's own
      test design) started failing with `bad main symbol`. Bisecting this
      down (real-match/coverage check on tokens -- passed; a decode-trace
      hook comparing the decoder's exact event sequence against the
      encoder's token list, the same technique used in item 8's earlier
      debugging saga -- found the actual divergence) led to a genuine,
      pre-existing, **production-impacting decoder bug**, unrelated to
      hash2 itself and merely surfaced by its changed token statistics:

      `codewordLenTokens` (`encode.go`), which decides when to collapse a
      run of codeword-length entries into precode symbols 17/18 (a run of
      *unused*, i.e. actual-length-0, symbols) or 19 (a run of entries
      that resolve to the same *nonzero* length), was grouping runs by
      whether the *transmitted delta* (against `prevLens`) was equal
      across the run -- not by whether the *actual new codeword length*
      was equal, which is what the real LZX format (and `decode.go`'s own
      `readCodewordLens`, matching wimlib's real decoder exactly, which
      broadcasts a single resolved length across the whole run rather than
      recomputing per position) actually requires. These two groupings
      coincide only when `prevLens` is uniformly zero -- exactly this
      package's original one-block-per-chunk baseline -- which is why
      this was never caught by items 7's or 13's own tests: real
      multi-block content only occasionally produces a run of 4+ entries
      that are actually-equal-and-nonzero while their prevLens-relative
      deltas differ, and neither item's specific test data happened to
      hit it. Verified directly against wimlib's own real source
      (`src/lzx_compress.c`'s `lzx_compute_precode_items`, read line by
      line rather than re-derived from assumption after an initial wrong
      attempt at a decode-side fix was itself checked against wimlib's
      real decoder and found backwards): wimlib's own encoder groups by
      `lens[run_start] == lens[i]` (the actual length), computing the
      transmitted delta only from the run's first position, exactly
      matching what `decode.go` already did on the read side. Fixed
      `codewordLenTokens` to take `lens`/`prevLens` directly and group by
      actual length equality, matching wimlib exactly; `decode.go` needed
      no changes at all (it was already correct -- confirmed against
      wimlib's real decoder source, not just re-tested). Added
      `TestCodewordLenTokensGroupsByLengthNotDelta`, a direct, minimal,
      deterministic regression test (uniform nonzero `lens` with
      deliberately non-uniform `prevLens`, so per-position deltas all
      differ) guarding this exact distinction going forward.

      **Real-world severity, discovered by finally checking round-trip
      correctness on the real benchmark file instead of only compressed
      size (a real methodology gap in this whole investigation up to this
      point -- the benchmark harness used throughout items 7-13 only ever
      measured output *size*, never decoded it):** re-running the real
      398-chunk/12.4MB `ntoskrnl.exe` benchmark with the *pre-fix* code
      (commit `25613ff`, item 13's committed state) and actually decoding
      every chunk's output found **20 of 398 chunks (5%) were silently
      corrupted** -- `bad main symbol`, `bad length symbol`, and `match
      offset out of range` decode errors, meaning item 13's previously-
      reported "6,729,686 bytes" figure included real, broken output that
      would have failed in any real decoder (including wimlib's own).
      This means items 7 and 13's reported compression-ratio numbers in
      this file were measured on a benchmark that could not have detected
      this corruption, since it never checked for it.

      With the fix applied (and the length-2/hash2 addition from Part A
      reverted -- see below): **6,728,890 bytes, 0 corrupted chunks
      (verified: every one of the 398 chunks decodes back to its exact
      original bytes)**, narrowing the gap to wimlib's LZX:100 reference
      (6,659,122 bytes) to **+1.05%**. This is the new, trustworthy
      baseline; the previous "+1.06%" figure is superseded, both because
      it was measured on partially-corrupted output and because the fix
      itself changed which encoding several chunks actually use.

      **The length-2/hash2 addition from Part A was itself reverted after
      being measured, separately from the bug it surfaced.** Once the
      precode bug no longer masked the real comparison: hash2 support
      alone, gated to only the real-cost-model (pass 2) parse (an
      un-gated version, active during the flat-cost pass 1 too, measured
      even worse -- 6,736,146 bytes -- since pass 1's flat 8-bit-per-
      symbol cost estimate systematically overvalues marginal length-2
      matches whose *real* codeword would end up long, being a rare,
      newly-introduced main-alphabet symbol), still measured **6,733,878
      bytes -- 4,988 bytes *worse* than the corrected no-hash2 baseline**,
      despite Part A's own analysis showing gowim's match discovery is
      never worse than wimlib's at any length wimlib can find. This is the
      same shape of result as items 10 and 12: a real, genuine, verified
      capability (finding real length-2 match opportunities that
      genuinely exist in the data) makes gowim's own two-pass token-
      frequency-rebuild architecture produce *worse* final output, because
      introducing new, low-frequency main-alphabet symbols for marginal
      matches measurably degrades the Huffman table's efficiency for
      everything else more than those matches individually save -- an
      interaction this package's simpler joint parse/code approximation
      doesn't handle as gracefully as wimlib's own more elaborate,
      frequency-calibrated, multi-pass cost model. Reverted; not kept
      behind a flag, since there is no measured case where it wins here.

      **Standing methodology lesson, recorded so it isn't repeated:** any
      future benchmark change to this encoder must verify round-trip
      correctness on the real test file (decode every chunk, compare
      against the original bytes), not just compare compressed sizes --
      this investigation's own tooling didn't do that for 7 items' worth
      of history, and it took a match-discovery side investigation to
      notice.
- [x] **Re-attempted hash2 with a progressive, real-table-rebuild greedy
      pass instead of a single static cost model (2026-08-18)**, following
      up on the reverted attempt above: the earlier hash2 regression was
      diagnosed as the two-pass encoder scoring every hash2 candidate
      against one Huffman table that never reflected any hash2 usage at
      all, so a genuinely-zero-frequency main symbol always looked
      artificially expensive (or literally unusable, codeword length 0) --
      a real Kraft-inequality effect, not a modeling bug. Implemented
      `greedyApplyHash2` (`lzx/hash2greedy.go`): finds every pair of
      consecutive literal tokens whose 2-byte value already occurred
      earlier in the chunk (via an exact 65536-entry "most recent
      occurrence" table, not a lossy hash), then greedily accepts
      candidates one at a time, rebuilding the *real* main Huffman table
      from the actual frequency counts after every acceptance and scoring
      remaining candidates against that up-to-date table -- so each
      accepted candidate's real cost (including any knock-on lengthening
      of unrelated codewords from adding a previously-unused symbol) is
      reflected before the next decision, not just the initial static
      estimate the old attempt used. Stops as soon as the best remaining
      candidate's value is <= 0 (the "MC > MB" stopping rule).

      Found and fixed a second, real, serious bug while building this,
      analogous in spirit to the precode bug above: splicing a new match
      token into the middle of an existing token stream perturbs the
      repeat-offset LRU queue's contents at every later position (every
      match, fresh or repeat-offset, updates the queue -- see matcher.go's
      `applyMatch`), so a later token's `repeat` field, chosen by the
      original parse against the *original* queue trajectory, can
      silently reference the wrong offset once the queue has drifted.
      This produced real corruption (2 of 398 real ntoskrnl.exe chunks
      decoded without error to the *wrong* bytes) before being caught by
      the verified benchmark (see the standing methodology lesson just
      above -- this is exactly the class of bug that a size-only
      benchmark would have missed). Fixed with `fixupQueueState`, which
      re-derives every match token's `repeat` field from the actual
      resulting queue trajectory (reusing matcher.go's own `applyMatch`)
      rather than trusting the original parse's now-stale classification;
      wired in unconditionally as a correctness step, not an optional
      pass. Added `TestGreedyHash2RoundTrips`
      (testdata/hash2_greedy_chunk1.bin, the actual real chunk this was
      found on) as a permanent regression fixture, confirmed to fail
      without `fixupQueueState` and pass with it.

      Wired into `compressLookahead` as a fifth candidate encoding,
      alongside VERBATIM/ALIGNED/split/splitStats, kept only if it
      measures smaller -- consistent with this encoder's established
      "try both, keep smaller" pattern, so this can never regress output
      even in some unmeasured edge case. Verified via the same real
      398-chunk/12.4MB `ntoskrnl.exe` test, decoding every chunk: **0
      corrupted chunks, 6,728,832 bytes**, a real (if modest) 58-byte
      improvement over the 6,728,890-byte baseline above -- unlike the
      first hash2 attempt's 4,988-byte regression, this one is a genuine,
      verified (if small) win, confirming the progressive-rebuild
      approach avoids the earlier failure mode. The gain is small because
      the greedy loop's per-round *selection* still scores remaining
      candidates against the table as of the last acceptance rather than
      a full hypothetical rebuild per remaining candidate (which would
      cost O(candidates^2) instead of O(candidates) and wasn't judged
      worth it here); see `greedyApplyHash2`'s own doc for the precise
      scope of what is and isn't exactly measured.
- [x] **Generalized the "rebuild the real table, don't trust a stale one"
      lesson from full-match matches, not just hash2 (2026-08-18).** hash2's
      greedy splice only works because a length-2 substitution never
      overlaps an existing match's byte range, giving it a trivial,
      local conflict model; a general match candidate (different offset,
      different length, extending/shrinking a match already chosen)
      overlaps its neighbors arbitrarily, so there's no equivalent cheap
      "accept one candidate, keep the rest" step. The real generalization
      is iterative re-parsing instead: `refineParse` (`lzx/encode.go`)
      feeds each round's own resulting real Huffman table back into
      `findMatches` as the next round's cost model -- extending this
      package's existing "pass 1 flat cost -> pass 2 real cost" two-pass
      technique into a fixed-point iteration (pass 2's table informs a
      pass 3 re-parse, pass 3's informs pass 4, up to `maxRefineIters =
      4`), stopping as soon as a round's real measured encoded size
      (not the token-implied estimate) fails to beat the previous best --
      the same "try both, keep smaller" safety property as everywhere
      else in this encoder, so a round that doesn't help is simply
      discarded. Wired into `compressLookahead` in place of the old
      single pass-2 `findMatches` call.

      Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test,
      decoding every chunk: **0 corrupted chunks, 6,727,462 bytes** -- a
      further, genuine 1,370-byte improvement over the hash2-only
      baseline above (6,728,832), narrowing the gap to wimlib's LZX:100
      reference (6,659,122 bytes) further still. Cost: real chunks
      converge in under 1 extra round on average (measured: 0.78 of the
      4-round cap across a 40-chunk sample -- a round that doesn't help
      stops immediately), but every round, converging or not, is a full
      extra `findMatches` call, which measurably roughly doubled
      `compressLookahead`'s own real-world wall time on the full
      benchmark (11.5s -> 24.0s for all 398 chunks) since even a
      non-improving chunk pays for the one round that checked.
- [x] **Investigated why gowim still trailed wimlib by ~1% despite the
      above (2026-08-18), at the user's explicit request to find the real
      cause rather than assume feasibility.** Read wimlib's actual
      near-optimal parser source (`lzx_compress_near_optimal`/
      `lzx_find_min_cost_path` in `src/lzx_compress.c`, v1.14.5) directly
      rather than recall/guess, confirming with file:line citations:
      - wimlib's repeat-offset queue is genuinely **not** a DP state
        dimension -- its own doc comment on `lzx_find_min_cost_path`
        states plainly that tracking it is "actually only an
        approximation" and "the algorithm does not solve this problem in
        general; it only looks one step ahead" (its one concession being
        a narrow, hand-coded 2-step "gap match" special case). gowim's own
        `findMatchesOptimal` beam DP (optimal.go), which tracks up to 4
        distinct queue-state hypotheses per position, already goes
        *beyond* this, not behind it -- ruling out queue-state handling as
        the explanation.
      - wimlib folds an aligned-offset cost estimate into match costs
        *during* the parse itself (`CONSIDER_ALIGNED_COSTS`), not only when
        choosing VERBATIM vs ALIGNED after the fact. gowim's cost model had
        no equivalent. Implemented one (`costModel.offsetExtraCost` in
        matcher.go, fed by a per-round-rebuilt `alignedLens` table in
        `refineParseWith`) -- measured only a **10-byte** improvement
        (6,727,462 -> 6,727,452) on the full benchmark, real but far too
        small to be the dominant cause.
      - Checked whether excess block-splitting overhead explained the gap:
        only 25 of 398 chunks (6.3%) actually split into more than one
        block, and the ~150-200-byte/chunk gap was already showing up
        uniformly even in single-block chunks -- not a block-layout issue.

      **The actual dominant cause, found by instrumenting which of
      `compress()`'s two concurrent parses (`compressLookahead`'s bounded
      lookahead vs `compressOptimal`'s beam DP) wins per chunk:
      `compressOptimal`'s DP won 346 of 398 real chunks (87%), by 57,360
      bytes total.** Every refinement built earlier today (`refineParse`'s
      iterative real-cost-model re-parsing, the aligned-cost folding
      above, and `greedyApplyHash2`) had only ever been wired into
      `compressLookahead` -- meaning the large majority of this encoder's
      real-world output was coming from a single-pass, un-refined DP
      parse against only the flat pass-1 cost model, untouched by any of
      the improvements made under the (false) assumption that the
      lookahead path was the one that mattered. Generalized `refineParse`
      into `refineParseWith(data, order, nMainSyms, initial, parse)`,
      parameterized over which parser to iterate (`findMatches` or
      `findMatchesOptimal` -- both share the same `func([]byte,
      costModel) []token` shape), and wired `compressOptimal` to use it
      plus a `greedyApplyHash2` pass on the DP's own resulting tokens,
      exactly mirroring what `compressLookahead` already had.

      Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test,
      decoding every chunk: **0 corrupted chunks, 6,715,522 bytes** -- an
      11,930-byte improvement, roughly 8x every other improvement made
      today combined, narrowing the gap to wimlib's LZX:100 reference to
      **~0.88%**, down from ~1.05% (measured directly against the same
      installed `libwim.so.15` used throughout this investigation:
      6,657,082 bytes -- close to but not identical to the 6,659,122
      figure recorded earlier in this file for the same comparison; not
      re-investigated, treat 6,657,082 as the more current figure). Cost:
      the DP path is more expensive per round than the lookahead path, so
      iterating it roughly doubled `compressOptimal`'s own time cost on
      top of the lookahead path's own earlier doubling (full-benchmark
      wall time: 24.0s -> 57.4s for all 398 chunks).

      **Standing lesson:** before spending further effort on cost-model
      precision (the aligned-cost and iteration ideas that motivated this
      investigation), it's worth first checking *which code path the
      improvement actually reaches* -- an objectively correct, well-
      reasoned improvement applied to the wrong (already-losing) parse
      path measures as nearly a no-op, exactly what happened here for
      most of today's earlier work before this was found.
- [x] **Found and fixed refineParseWith's own non-monotonic-convergence
      bug, then made hash2 fully native (replacing the ex-post splice
      entirely) and, in doing so, found and fixed a real cost-model bug
      that -- once fixed -- closed the rest of the gap to wimlib entirely
      (2026-08-18, same day, continuing the investigation above).**

      **Part A: `refineParseWith` was stopping too early.** Instrumenting
      round-by-round encoded size across real chunks found the sequence is
      NOT monotonic: it can regress for several consecutive rounds and
      then recover to a new true best later (one sampled chunk's true best
      over 12 rounds only appeared on round 12, after 3 and then 5
      consecutive non-improving rounds). The original "stop at the first
      non-improving round" policy measurably left real gains on the table:
      926 bytes lost across just 12 sampled chunks compared to tracking
      the true global best over more rounds. Fixed with a patience-based
      stopping rule (keep going as long as recent rounds keep failing to
      beat the *global* best, not just the immediately preceding round)
      plus an explicit fixed-point/cycle detector (`tokensFingerprint`,
      an FNV-1a hash of a round's full token sequence -- an exact repeat
      of any earlier round's output proves every later round will just
      replay the same cycle forever, so stop immediately rather than
      burning the full patience/hard-cap budget for nothing). Swept
      `refinePatience` directly: 2 captured 86% of the gain available
      from going all the way to 6, at ~1.6x the time cost vs. 6's ~4.7x --
      kept 2, with a generous (rarely-hit) `maxRefineItersHardCap = 32`
      safety ceiling, since there's no proof this iteration is guaranteed
      to reach a true fixed point on every input.

      **Part B: went back to wimlib's real match usage and found gowim
      was still radically under-using hash2, at the user's direction ("the
      problem is that we aren't using them when we need to").** Decoded
      wimlib's *actual* compressed output for several real chunks with
      gowim's own (already-verified-correct) decoder, position by
      position, and directly diffed the resulting token streams against
      gowim's own. The result was unambiguous: the overwhelming majority
      of divergences were length-2 matches wimlib used and gowim didn't
      (783 vs. 112 the other way, for one representative chunk). Checked
      whether `greedyApplyHash2` (the ex-post splice from the earlier
      entry) was even trying: it found 2,291 real candidates in that same
      chunk, accepted only 31, and even those 31 made the encoding
      *worse* -- confirmed across an 11-chunk sample (thousands of
      candidates each, single-digit-percent acceptance, net negative in
      most chunks). Root cause: introducing any previously-zero-frequency
      symbol into an *already-built* Huffman table is expensive by
      construction (Kraft-inequality reallocation), so a post-hoc splice
      against a fixed table can never compete with a native encoder that
      treats length-2 matches as first-class from its first pass, the way
      wimlib's real encoder does.

      **Replaced the splice with native support.** Deleted
      `hash2greedy.go` entirely (`greedyApplyHash2`, `findHash2Candidates`,
      `fixupQueueState`) and added length-2 fresh-match candidates directly
      to both parsers: `buildHash2PrevOcc` (matcher.go) precomputes, for
      every position, the most recent earlier occurrence of the same
      2-byte value (an exact 65536-entry table, needing no incremental
      update since byte values never change based on which tokens get
      chosen -- unlike the length>=3 BST, which does need per-position
      insertion). `findMatches`' bounded lookahead gained a 4th candidate
      (`bestFreshCandidate2`) alongside repeat/fresh-length>=3/literal;
      `findMatchesOptimal`'s DP gained a matching edge per position,
      relaxed once per beam state. Also confirmed directly from wimlib's
      real `lzx_get_num_main_syms` (`src/lzx_common.c`) that a length-2
      match's one edge-case offset (`windowSize-2`, one slot beyond what
      `numOffsetSlots` sizes for) is not an oversight to patch around but
      an explicit LZX format restriction -- its own comment states outright
      that the format disallows this exact case specifically so the
      offset-slot table can be one slot smaller. Guarded for it
      (`hash2Candidate` rejects any candidate whose main symbol doesn't
      fit `nMainSyms`) with a permanent regression test reproducing the
      exact boundary crash a review agent's fuzz run had separately found
      in the (now-deleted) splice version.

      **This measurably regressed the full benchmark at first: 6,709,290
      -> 6,738,762 bytes, and 1.6x slower.** Prompted directly by the user
      ("exploring more options should not result in a regression"): a
      *true* optimal solver can't get worse by gaining legal moves, since
      the old solution stays reachable -- so this had to be a real bug in
      the approximate optimizer, not an inherent property of having more
      candidates. Found it: `costModel.matchCost` read a real Huffman
      codeword length of `0` (buildLengths' documented meaning: "this
      symbol has no codeword in the current table") as if it meant "this
      symbol costs 0 bits" -- i.e. free -- instead of falling back to the
      flat estimate the way `literalCost` already correctly did for the
      identical situation. Every brand-new length-2 offset slot (zero
      frequency in whatever round's table was active) looked artificially
      free the instant it was considered, causing exactly the kind of
      runaway over-selection observed. `costModel.offsetExtraCost` had the
      identical bug for aligned-code symbols. Fixed both (fall back to
      the flat/raw-bits estimate whenever the real length is 0, matching
      `literalCost`'s existing pattern) -- this was the actual, correct
      fix, not a workaround: no on/off toggle or dual-parse safety net was
      needed once the cost function itself was correct.

      **Verified via the same real 398-chunk/12.4MB `ntoskrnl.exe` test,
      decoding every chunk: 0 corrupted chunks, 6,653,212 bytes -- smaller
      than wimlib's own real level-100 output (6,657,082 bytes) on this
      file, by about 3,870 bytes (~0.06%).** This is the first point in
      the whole investigation where gowim's pure-Go encoder has matched or
      beaten wimlib's real compiled one on this benchmark, closing what
      was a real, measured, verified gap. Cost: native hash2 costs real
      time (94s -> 225s for the full benchmark, on top of all the earlier
      iteration/beam-width costs), since both parsers now do meaningfully
      more work per round; not yet tuned for speed given the ratio result
      was the more urgent question to settle.
- [x] Implement WIM integrity-table (re)computation for newly written files,
      mirroring `DISM /CheckIntegrity`. Done: `WriteOptions.
      ComputeIntegrityTable`, integrated as a single pass into `wim.WriteTo`.
      The real covered byte range — `[HeaderSize, end of blob table)`,
      excluding XML data and the integrity table itself despite being
      written after XML data — was determined empirically against real
      `boot.wim`/`install.esd` integrity tables (every other candidate range
      mismatched) and corroborated in wimlib's source. `wimlib-imagex
      verify` confirmed a file produced by our writer with an integrity
      table passes real independent verification. Also added
      `Reader.VerifyIntegrity()`.

## Performance: concurrency opportunities (new)

Found 2026-07-14 while testing an out-of-tree nano11-style debloat harness
(not part of this repo) against a real, stock Windows 11 25H2 retail ISO's
`install.wim`: exporting/re-encoding a single ~4-5GB edition through the pure-Go
LZX encoder single-threaded took several minutes. All three are safe,
embarrassingly-parallel loops (no shared mutable state across iterations
beyond writing to independent, pre-sized slice indices), not a redesign of
any codec's actual algorithm.

- [x] Parallelize `wim/compress.go`'s `EncodeResourceData` per-chunk
      compression loop (`chunks := make([][]byte, numChunks); for i := ...
      { chunks[i] = compressChunk(...) }`). Each WIM chunk (conventionally
      32768 bytes) is compressed completely independently by design (see
      `lzx`/`lzms`/`xpress`'s own package docs); a bounded worker pool
      writing to distinct `chunks[i]` slots needs no locking. Highest
      impact on large individual files (hundreds of chunks each) — e.g. the
      real `nl7models0804.dll` (129 chunks) found during the LZX
      `align`-quirk investigation above. Done (2026-07-14): new
      `compressChunksParallel` (`wim/compress.go`), a `min(numChunks,
      GOMAXPROCS)`-bounded worker pool pulling chunk indices off a shared
      counter; `EncodeResourceData` unchanged otherwise. Verified with
      `go test -race`.
- [x] Parallelize `wim/writer.go`'s `WriteTo` per-blob loop (`for i := range
      bt.Entries { data := blobs.Blob(hash); payload :=
      EncodeResourceData(...); writeBytes(payload) }`). Fetching and
      compressing each blob is independent; only the final `writeBytes`
      (and its running `offset`) must stay in original blob-table order —
      a "parallel compute, ordered drain" pattern (worker pool feeding an
      indexed/ordered results channel, single writer goroutine draining in
      order). Highest impact on a full multi-GB image with many small
      files (the common case: most files are 1 chunk each, where the
      per-chunk parallelization above does nothing). Done (2026-07-14): new
      `encodeBlobsPipeline` (`wim/blob_pipeline.go`) dispatches blob
      indices through a bounded `jobs` channel (capacity `2*GOMAXPROCS`) to
      a fixed worker pool, each writing its result to a dedicated
      buffered-1 per-index channel; `WriteTo` drains these strictly in
      index order, keeping the final write (and `offset` bookkeeping)
      unchanged. The bounded jobs channel caps how far compression can run
      ahead of the writer, so memory use stays bounded rather than
      buffering a whole multi-gigabyte image's compressed output at once.
      Verified with `go test -race`.
- [x] Parallelize `component/build.go`'s `BuildFromImage` per-file read+parse
      loop (`.mum`/`.manifest` files, confirmed independent of each other by
      the existing 17189-file real-corpus survey). Lower urgency than the
      two above — parsing is cheap relative to compression — but still a
      real win for building a `Store` over a full real image. Done
      (2026-07-14): new `runJobsParallel` (`component/build.go`) — each
      file's read+parse becomes a `func() *Entry` closure, run on a
      `min(len(jobs), GOMAXPROCS)`-bounded worker pool; results are
      collected in any order (order never mattered here — `Build` only
      indexes entries into a map afterward). Safe because `*wim.Reader`'s
      `ReadFile` goes through `ReadAt`, which is safe for concurrent use
      the same way `os.File.ReadAt` is. Verified with `go test -race`.
- [x] Parallelize *within* `lzx.compress()` itself (2026-08-18), as distinct
      from the three per-chunk/per-blob items above (which parallelize
      *across* chunks/blobs, at the `wim` package level) -- this helps even
      when there's only one chunk to compress, or when outer parallelism
      is already saturating every core. Two changes, both verified with
      `go test -race` and both confirmed byte-identical output (same
      6,730,928-byte real-benchmark total as before, just faster) since
      neither changes which candidate wins, only how many run at once:
        - `compress()`'s two most expensive steps -- the bounded-lookahead
          parse (`compressLookahead`, new) and the DP parse
          (`compressOptimal`, new) -- only depend on pass 1's tables, not
          on each other, so they now run in two goroutines joined by a
          `sync.WaitGroup` instead of serially. This is the change that
          actually mattered: on the real 398-chunk/12.4MB `ntoskrnl.exe`
          test, compressing chunks one at a time (no outer parallelism at
          all, to isolate this change's own effect) dropped from ~14.0s to
          ~11.7s (~16% faster) for byte-identical output.
        - Finer-grained: `compressLookahead`'s three candidates (VERBATIM,
          ALIGNED, the 2-block split trial) and `trySplitChunk`'s two
          independent per-half table-building/ALIGNED-decision steps were
          also parallelized (three and two goroutines respectively). This
          part is honestly not worth much on its own: measured before/
          after this specific change in isolation, the same benchmark
          stayed at ~11.7s (11.72s vs 11.71s, within noise) -- these steps
          are cheap encodeBlock/table-building work compared to the two
          real match-finding parses above, which already dominate total
          cost and were already the ones parallelized. Kept anyway since
          it's a real, correctness-preserving, race-clean improvement in
          the cases where it *does* matter (e.g. a machine with far more
          cores than this single chunk's own critical path can use), not
          reverted the way item 10 above was -- unlike that experiment,
          this one never changes the chosen output, only overlaps
          independent work, so there is no risk of the "more exploration
          under an approximate model backfires" failure mode from item 10.

        **A further, tried-and-reverted experiment (2026-08-18):**
        parallelizing `findMatchesOptimal`'s up-to-`beamWidth` (4) states
        *within a single position* -- i.e. spawning goroutines for each of
        the up to 4 queue-state hypotheses' relax work at every position,
        collecting each into a private slice, then merging serially
        (avoiding any data race on the shared `states` array). Correctness
        held (same byte-identical 6,730,928-byte output), but real
        wall-clock got dramatically *worse*: ~11.7s -> ~39.8s (over 3x
        slower) on the same real benchmark. This is the expected result of
        parallelizing at the wrong granularity, now confirmed rather than
        assumed: the DP visits on the order of 32768 positions per chunk
        across 398 chunks, each spawning up to 4 goroutines for work that
        is itself only a handful of arithmetic operations and slice
        appends -- goroutine creation/scheduling overhead (order of
        100ns-1us each) vastly exceeds the nanoseconds-scale work being
        parallelized, at tens of millions of spawns total. Reverted
        immediately; not kept even behind a flag, since there's no
        plausible input size where this granularity would pay off. The
        earlier items in this list parallelize substantial, coarse units
        of work (a whole chunk's worth of parsing, or a whole alternate
        parse strategy) -- this experiment is a concrete data point for
        where the profitable boundary sits between those and "too fine to
        bother."

        **SIMD investigation (2026-08-18): word-at-a-time (SWAR) kept,
        Go 1.26's experimental `simd`/`archsimd` package tried and
        removed.** The match finder's hottest inner loop --
        `matchLenCapped`/`matchLenAt`'s common-prefix-length comparison,
        called on nearly every candidate during a parse -- was a naive
        byte-by-byte loop. Replaced with `commonPrefixLen`
        (`lzx/matchlen.go`), comparing 8 bytes at a time via a single XOR
        plus `math/bits.TrailingZeros64` ("SWAR" -- SIMD-within-a-register
        -- the same technique real encoders like zstd's `ZSTD_count` use
        for this exact operation), portable to every Go-supported
        architecture with no build tags or CPU-feature checks. Verified
        byte-identical output (6,730,928 bytes) and a real further
        speedup: ~11.7s -> ~9.8s on the same real benchmark.

        Also tried real AVX2 vector instructions via Go 1.26's
        `simd/archsimd` package (`goexperiment.simd` build tag,
        `GOEXPERIMENT=simd`), comparing 32 bytes at a time. This surfaced
        a real, checked (not assumed) Go-compiler fact worth recording:
        this compiler's inliner has a fixed per-call cost floor of
        roughly 60-72 (confirmed via isolated minimal reproductions, not
        estimated) against its default budget of 80, so `commonPrefixLen`
        can never be inlined at its call sites once it needs to call out
        to extend a match at all -- no amount of restructuring closes
        that gap, since the mere presence of a function call already
        consumes most of the budget before counting anything else. This
        applies equally to the plain SWAR version (also never inlined),
        yet SWAR still measured faster than the byte-loop baseline -- so
        inlining was a red herring for *this* speedup; the real win was
        just doing less work per comparison.

        The AVX2 version's real problem was different: on the initial
        attempt, it always ran the `archsimd.X86.AVX2()` check and set up
        vector registers on every single call, even for short comparisons
        that mismatch or terminate well under 32 bytes -- the common case
        for real binary data. Measured result: ~18.5s, *worse* than even
        the original byte-loop baseline (~11.7s). Restructuring to defer
        all AVX2 machinery into a separate function reached only once an
        8-byte fast path confirms a real match worth extending brought it
        down to ~10.7-10.8s -- much closer to, but still measurably worse
        than, plain SWAR's ~9.8s. Raising the threshold before even
        attempting AVX2 (requiring at least 2 full 32-byte blocks'
        remaining length) barely moved that number. Given further tuning
        would need real CPU profiling to make progress (rather than more
        parameter guessing) and the practical downsides of shipping it at
        all -- `GOEXPERIMENT=simd`-only, AMD64-only, and an explicitly
        unstable API not covered by Go's compatibility promise, none of
        which SWAR has -- it was removed rather than kept as an
        unused-by-default option. `lzx/matchlen.go` is the sole
        remaining implementation; there is no build-tag split anymore.

## In-memory WIM filesystem operations

Replaces what the script does via a real DISM mount, by operating on the
`DirEntry` tree directly instead.

- [x] Implement a path-based file-read API over a WIM image's `DirEntry`
      tree (resolve a path, return decompressed contents). Done:
      `Reader.ReadFile` + `BlobTable.ByHash` + `DirEntry.Lookup`.
- [x] Implement a path-based file add/replace API (generalize
      `driver/install.go`'s ad hoc `placeFile` into a reusable,
      non-driver-specific API). Done: `DirEntry.Add`.
- [x] Implement a path-based delete API (file or recursive directory
      subtree). Done: `DirEntry.Remove`.
- [x] Implement path-based rename/move and directory-listing (readdir)
      APIs. Done: `DirEntry.Rename`, `DirEntry.ReadDir`.
- [x] Implement glob/pattern matching over WIM image-tree paths/names
      (replicates the script's `-like 'prn*'`-style filtering). Done:
      `MatchName`.

## Registry generalization

- [x] Generalize `service`'s Services-only regf key/value navigation into a
      generic, hive-agnostic path-based API (create-if-missing,
      delete-subtree) usable against any hive (SOFTWARE, DEFAULT, SYSTEM,
      COMPONENTS, `NTUSER.DAT`), not just SYSTEM's `Services` tree. Done
      (2026-07-13): the underlying logic (`FindSubkey`/`RemoveSubkey`/
      `FindOrCreateSubkey`/`FindValue`/`SetValue`/`RemoveValue`, previously
      in `service/keys.go`, hardcoded to nothing Services-specific in
      practice but only exposed for that one use case) moved to the sibling
      `regf` package itself, as methods directly on `*regf.Key`/`*regf.Value`
      (`regf/key.go`): `Subkey`/`FindOrCreateSubkey`/`DeleteSubkey`,
      `Value`/`SetValue`/`DeleteValue`, plus new backslash-separated
      path-based navigation (`OpenPath`/`FindOrCreatePath`/`DeletePath`) that
      didn't exist before at all. `service` and `driver` (which previously
      called through `service.FindSubkey`/etc. for its own
      `CriticalDeviceDatabase` merging) were both updated to call the new
      `regf.Key` methods directly instead of duplicating/routing through
      `service`; `service/keys.go` and `service/encoding.go` were deleted
      outright rather than kept as compatibility shims (this is a
      single-project monorepo with no external consumers to break). Tested:
      new `regf/key_test.go` covers case-insensitive subkey/value lookup,
      find-or-create idempotence, delete-subtree, and all three path-based
      operations (including missing-intermediate-component and
      empty-path edge cases); all of `service`'s and `driver`'s existing
      tests still pass unmodified in behavior (only call-site syntax
      changed, e.g. `FindSubkey(k, name)` -> `k.Subkey(name)`).
- [x] Extend `regf`/`service` (or a new package) to set/delete arbitrary
      registry values by full path and type (REG_SZ/REG_DWORD/REG_MULTI_SZ/
      etc.), matching generic `reg add`/`reg delete` semantics. Done
      (2026-07-13), folded into the same `regf/key.go` work above rather
      than as separate new API surface: `FindOrCreatePath`/`OpenPath`
      resolve an arbitrary full path to a `*Key`, which `SetValue`/
      `DeleteValue`/`Value` then operate on directly (e.g.
      `hive.Root.FindOrCreatePath(`SOFTWARE\Microsoft\Foo`).SetValue("Bar",
      regf.RegDWORD, regf.EncodeDWORD(1))`) -- no separate
      "set-value-by-path" function was needed once path resolution and
      value mutation both exist as composable `*Key` methods. Typed
      encode/decode helpers for REG_DWORD/REG_SZ/REG_MULTI_SZ
      (`EncodeDWORD`/`EncodeSZ`/`EncodeMultiSZ`, `Value.DWORD`/`SZ`/
      `MultiSZ`) were promoted from `service`'s private `uint32LEBytes`/
      `multiSZBytes`/`readDWORD`/`readSZ`/`readMultiSZ` helpers to exported
      `regf` functions/methods for the same reason -- one hive-agnostic
      implementation instead of a private per-package copy.
- [x] Implement a helper to locate/load the standard hive set from a WIM
      image path (`SYSTEM`, `SOFTWARE`, `DEFAULT`, `SAM`,
      `Users\Default\NTUSER.DAT`, `COMPONENTS`) via the path-based WIM read
      API, and write modified hives back via the path-based WIM write API.
      Done (2026-07-13): new `registry` module (`LoadHiveSet`/`Hive.Save`),
      mirroring the sibling `driver` package's `Install`/`Uninstall`
      relationship to `wim`'s blob table (dedup by hash, increment/decrement
      `RefCount`, never reclaim a zero-`RefCount` entry itself -- that's a
      whole-WIM-aware concern left to a higher-level caller) rather than
      inventing new conventions. `LoadHiveSet` walks the six standard image
      paths via `wim.DirEntry.Lookup`, treating a hive that simply isn't
      present in a given image (e.g. no `SAM`/`COMPONENTS` in a WinPE image)
      as normal, not an error -- only a hive that exists but fails to
      read/parse is one. `Hive.Save` re-serializes via `regf.Hive.AppendTo`,
      updates the hive's `wim.DirEntry` stream hash, and returns any
      genuinely-new blob content the caller must place when it eventually
      assembles/writes the output WIM file (this package does not do that
      itself, exactly as `driver.Install` doesn't either). Does not do
      registry navigation itself -- that's the `regf.Key`/`regf.Value`
      generalization above; this package only handles the WIM-image-path and
      blob-table integration. Tested against a real, serialized-and-reparsed
      WIM image built via the sibling `wim` package's own
      `Assemble`/`NewReader` (mirroring `wim`'s own `writer_test.go` fixture
      approach) containing hand-built hives (mirroring `regf`'s own
      `TestBuildFromStructLiterals` shape): partial hive-set loading (only
      hives actually present), an empty image with no standard hives at all,
      a real mutate-then-save round trip (verified three ways: the returned
      new blob re-parses to the mutated value, the `DirEntry`'s hash is
      updated, and `RefCount`s move correctly between old/new blob-table
      entries, including the old entry correctly reaching 0 rather than
      being removed outright), and a no-op save (unmodified hive -> no new
      blob, unchanged `RefCount`).

      A real `regf` parsing bug was found and fixed 2026-07-14 via this
      package's `LoadHiveSet` against a real, stock Windows 11 25H2 retail
      ISO's actual `COMPONENTS` hive (while testing an out-of-tree
      nano11-style debloat harness, not part of this repo):
      `regf.resolveValueData` misidentified an ordinary small value as a
      `"db"` big-data key purely because its first two bytes happened to
      read `"db"`, regardless of its real size (the format only uses `db`
      cells for values over 16344 bytes, per libregf's format spec) —
      producing a nonsensical, wildly out-of-bounds cell offset. See
      `regf/README.md`'s own "bug found and fixed" section for the full
      writeup and regression test.

      A second, far more serious real `regf` *writing* bug was found and
      fixed 2026-07-15, via the same out-of-tree harness: a fully rebuilt
      image installed and booted, but hung indefinitely (black screen,
      active CPU) at first-logon specialize — bisected, through several
      rounds of isolating every other debloat step (AppX removal,
      servicing-package removal, file cleanup, the WinSxS wipe, service
      removal — none of which were the cause) down to a true no-op rebuild
      (every hive loaded via `LoadHiveSet` and saved back via `Hive.Save`
      completely unchanged), which *still* hung. Root cause: `Hive.AppendTo`
      gave every key with a security descriptor its own brand-new `sk`
      cell, never deduplicating byte-identical descriptors the way Windows
      itself does (a real hive's tens of thousands of keys typically share
      only a handful of unique descriptors). Confirmed directly: parsing a
      real 76.5MB Windows 11 SOFTWARE hive and immediately re-serializing
      it with zero logical changes produced a 181MB file — more than
      double — that still parsed back correctly (fooling this package's
      own round-trip tests, all of which used small synthetic hives with
      at most one security descriptor) but apparently made some
      first-logon component choke enumerating tens of thousands of
      trivially-distinct one-node security lists instead of the small
      shared pool a real hive has. Fixed by a new `skPool` (`regf/skpool.go`)
      that deduplicates descriptors by exact byte match across the whole
      tree during `AppendTo`, reproducing Windows' own sharing (and
      correctly incrementing the shared cell's `refCount`); the same real
      SOFTWARE hive now round-trips to 72MB (smaller than the original,
      since this package still doesn't reproduce Windows' own bin/free-space
      layout — a separate, already-documented non-goal). See
      `regf/README.md`'s own writeup and `skpool_test.go`'s
      `TestAppendToDeduplicatesSecurityDescriptors` regression test.

## Image metadata extras

- [x] Extend `wim.XMLImage` parsing for the `WINDOWS`/`ARCH`,
      `PRODUCTNAME`, `EDITIONID`, and `LANGUAGES` fields DISM's
      `/Get-WimInfo` shows, if/where they're actually present in the WIM
      XML (verify against a real image's XML before assuming the shape).
      Done: `XMLImage.Windows *XMLWindows` (`Architecture`/
      `ArchitectureName()`, `ProductName`, `EditionID`, `Languages`,
      `Version`, etc.), verified against a real Windows 11 23H2
      `install.esd`.
- [x] Add a way to read an offline image's default UI language and
      processor architecture from its SOFTWARE hive (verify exactly which
      registry values DISM's `Get-Intl`/`Get-WimInfo` actually source these
      from first — don't assume they come from the WIM XML). Done
      (2026-07-14): `registry/imageinfo.go`'s `DefaultUILanguage`/
      `ProcessorArchitecture`. The "SOFTWARE hive" premise above was wrong —
      verified, not assumed, by extracting a real, pristine (never booted)
      Windows 11 23H2 factory `install.esd`'s actual `SYSTEM` and
      `SOFTWARE` hives (via `wimlib-imagex extract` from the project's
      `tiny11 23H2 x64.iso`) and finding both values already populated
      pre-boot in the **SYSTEM** hive, not SOFTWARE:
      `CurrentControlSet\Control\Nls\Language\InstallLanguage` (a 4-hex-digit
      LCID string, e.g. `"0409"`) and `CurrentControlSet\Control\Session
      Manager\Environment\PROCESSOR_ARCHITECTURE` (e.g. `"AMD64"`), both
      resolved via the sibling `service` package's existing
      `CurrentControlSet` (`Select\Default` → `ControlSetNNN`). Also
      officially documented: Microsoft Q&A content on changing a Windows
      installation's default UI language describes
      `HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Control\Nls\Language`'s
      `Default`/`InstallLanguage` as LCID-valued
      (https://learn.microsoft.com/en-us/answers/questions/4281742/), and
      Microsoft's "Determine the type of processor" support article
      documents `PROCESSOR_ARCHITECTURE` under `...\Session Manager\
      Environment`
      (https://learn.microsoft.com/en-us/troubleshoot/windows-server/setup-upgrade-and-drivers/determine-the-type-of-processor),
      including the caveat (relevant to why this is the right value for an
      image's architecture rather than exact CPU model) that it reflects
      only the instruction-set architecture, not vendor.

## Driver package additions

- [x] Implement `driver.ListInstalled`: enumerate driver packages already
      present in an image's DriverStore + registry (for "remove
      non-essential driver classes by pattern"-style operations). Done:
      `ListInstalled` returns `[]InstalledPackage{Name, Dir}`.
- [x] Implement `driver.Uninstall` (the reverse of `Install`): remove a
      driver's files from the image tree, delete its `Services\<name>` key
      (`service.Delete` already covers this part), and remove its
      `CriticalDeviceDatabase` entries. Done: idempotent (treats "already
      gone" as success), decrements blob refcounts without deleting entries.
- [x] Reverse-engineer the DriverStore FileRepository folder-hash scheme
      (previously an explicit non-goal: the `<infname>_<arch>_<hash>` folder
      naming under `Windows\System32\DriverStore\FileRepository\`, which
      forced `Install` callers to supply DIRID 13's destination path by
      hand). Done (2026-07-13), fully cracked and empirically validated —
      **the algorithm itself is now DIFFERENT from what the original
      non-goal note assumed** (it isn't any cryptographic hash of the INF at
      all).
      - **Confirmed genuinely undocumented before starting**, not just
        assumed: a dedicated research pass found no public documentation,
        forum reverse-engineering, or GitHub tooling (including
        DriverStoreExplorer/RAPR, which enumerates via the documented
        SetupAPI surface rather than recomputing names) that describes this
        scheme. The strongest source found is a 2008 Microsoft newsgroup
        thread
        (https://groups.google.com/g/microsoft.public.win32.programmer.kernel/c/Kz5g0KSVHkY)
        where a Microsoft engineer states plainly that no published
        description exists and recommends calling the documented
        `SetupGetInfDriverStoreLocation()` API instead of computing the path
        yourself — confirming this had to be reverse-engineered from
        scratch, not looked up.
      - **Real ground-truth corpus collected first**, before any
        disassembly: a background agent mounted the real Windows 11 23H2 VM
        read-only (`guestmount --ro`, never booted) and copied the full,
        byte-exact contents of 102 real `DriverStore\FileRepository`
        packages (out of 704 total, file-listed in full), sampled for
        diversity (every package with a `.cat` file, every package with >4
        files, plus an alphabetically-spread sample of plain 2-file
        packages). This became the empirical validation set for any
        hypothesis — exactly the same discipline as the PA30 SRC/FULLSRC
        research (real hash-verified corpus over unverified plausibility).
      - **A specific anomaly in that corpus drove the investigation**:
        `ntprint.inf_amd64_0234ee61ba44613e` and
        `ntprint.inf_x86_0234ee61ba44613e` have the *identical* 16-hex hash
        despite different architectures and completely different payload
        file sets (~28 printer-driver files each, under different
        subdirectories) — while `prnms003.inf_amd64_...` vs
        `prnms003.inf_x86_...` (same INF basename, different arch) have
        *different* hashes. Byte comparison resolved this immediately: the
        two `ntprint.inf` copies are MD5-identical (one shared
        multi-platform INF listing `NTx86,NTAMD64,NTIA64,NTARM,NTARM64`
        together in `[Manufacturer]`, copied verbatim into both arch
        folders) despite different `.cat` files per arch, while the two
        `prnms003.inf` copies genuinely differ byte-for-byte (different
        `[Manufacturer]`/model sections despite an identical `[Version]`
        section) — direct evidence the hash depends only on the INF's own
        bytes, not architecture, payload set, or catalog content.
      - **Cracked via clean-room disassembly** of the real `drvstore.dll`
        (extracted read-only from the VM via `virt-cat`, never booted; only
        machine code read, since Microsoft ships no source for it — ordinary
        black-box disassembly, not a licensing concern, same policy as the
        PA30/msdelta.dll work). The algorithm: a **base-39 polynomial
        rolling hash** (`h = h*0x27 + byte`, Horner's method) over the
        driver's raw, unmodified `.inf` file bytes — not a cryptographic
        hash at all, and not involving architecture, payload files, or
        catalog content, exactly as the corpus anomaly indicated. Core loop
        at drvstore.dll RVA `0x20c2c` (`imul rcx, rax, 0x27` /
        `add rax, rcx`); reached from a folder-name builder at RVA
        `0xb6328` that formats `"%ws_%ws_%ws"` (a string literal at RVA
        `0x110ff8`) from the INF's display name, an architecture string,
        and the hash's 8 bytes hex-encoded (little-endian/memory byte
        order) via a table at RVA `0x12cfe0`; reachable from
        `DriverStoreGetObjectPropertyW`'s property dispatcher, confirming
        it's a real, queryable per-package property, not install-time-only.
      - **A real red herring, investigated and ruled out**:
        `setupapi.dll`'s exported `pGetDriverPackageHash` (disassembled in
        full) turned out to call `CryptQueryObject`/
        `CryptCATAdminCalcHashFromFileHandle` — a genuine, but unrelated,
        SHA-1-based catalog/file thumbprint (20 bytes, not 8; used for
        UI/logging, not folder naming). An initial "strong name" string
        builder at drvstore.dll RVA `0x66b58`
        (`"%ws:*:%ws:%u.%u.%u.%u:%ws"`) was also hypothesized as the hashed
        input and ruled out once the corpus validation (below) proved the
        hash is simply over raw INF bytes.
      - **Empirically validated at 100% (102/102)** against the real
        corpus: every sampled real package's actual `.inf` bytes reproduce
        its real folder-name hash exactly.
      - **Implemented** in new `driver/driverstore.go`
        (`driverStoreHash`/`FileRepositoryDirName`), with the full
        provenance/validation trail in its doc comments (not just here).
        `Install` now computes DIRID 13's destination automatically (via
        `pkg.fileRepositoryDirName()`) when the caller doesn't supply an
        explicit override, resolving the limitation the original non-goal
        note described — callers can still override it explicitly (e.g. to
        match a specific real image's existing folder if reusing one, or
        for testing) since an explicit `destDirs` entry still takes
        priority. Tested: `TestDriverStoreHashRealSamples` embeds two real
        `.inf` files (`1394.inf`, `ntprint.inf`) as permanent regression
        fixtures reproducing their exact real hashes (including the
        architecture-independence the `ntprint.inf` anomaly demonstrated);
        `TestInstallComputesDriverStoreDir` confirms `Install` wires this in
        correctly end-to-end; `TestInstallMissingDestDir` was updated to
        target a different, still-required DIRID, since DIRID 13 alone no
        longer forces an explicit `destDirs` entry.

## AppX provisioned-package subsystem (new; research first)

- [x] **Research first:** how DISM's offline provisioned-package list is
      actually stored. Done (2026-07-10): a background research agent
      inspected a real Windows 11 23H2 `install.esd` directly
      (`/media/gavin-john/tiny11 23H2 x64`, via `wimlib-imagex extract` +
      `hivexregedit --export`, read-only). Findings:
      - `StateRepository-Machine.srd` **does not exist** on a pristine,
        un-booted image (`ProgramData\Microsoft\Windows\AppRepository`
        exists but its `Families`/`Packages` subfolders are empty) — the
        SQLite state-repository database is a runtime artifact created at
        first boot/specialize, not something present at offline-servicing
        time. Its schema remains undocumented, but is irrelevant to the
        offline use case, which resolves the TODO's original uncertainty.
      - The real offline source of truth is plain, well-formed XML:
        `ProgramData\Microsoft\Windows\AppxProvisioning.xml`, root element
        `AppxProvisionList` (xmlns
        `http://schemas.microsoft.com/appx/2013/appxprovisionpackage`),
        with `<Provisioned><Package FullName="..." .../></Provisioned>`
        (one entry per provisioned package/bundle/framework/resource
        package, flags `PackageType`/`ProvisionSourceIsBundle`/`IsLOBApp`)
        and `<EndOfLife><Package FamilyName="..."/></EndOfLife>` (blocked
        family names). Not documented by Microsoft under that filename;
        reverse-engineered by direct inspection of the real file
        (reproduced in full in the research agent's report).
      - Backed by a SOFTWARE-hive registry tree,
        `Microsoft\Windows\CurrentVersion\Appx\AppxAllUserStore\Applications\<PackageFullName>`
        (one subkey per package, `Path` value pointing at its manifest;
        nested subkeys for dependency packages) — confirmed present and
        populated pre-boot in the real hive dump.
      - The companion `Deprovisioned` marker-key mechanism (empty keys
        under `AppxAllUserStore\Deprovisioned\<PackageFamilyName>_<PublisherId>`
        that block reprovisioning on future updates) **is** officially
        documented: [Keep removed apps from returning during an update —
        Microsoft
        Learn](https://learn.microsoft.com/en-us/windows/application-management/remove-provisioned-apps-during-update).
      - `Remove-AppxProvisionedPackage`'s own reference page documents only
        its public parameters, not its internal mechanism: [Remove-AppxProvisionedPackage
        | Microsoft
        Learn](https://learn.microsoft.com/en-us/powershell/module/dism/remove-appxprovisionedpackage?view=windowsserver2025-ps).
      - Prior art: `tiny11builder`'s
        [`tiny11maker.ps1`](https://github.com/ntdevlabs/tiny11builder/blob/main/tiny11maker.ps1)
        (nano11's direct ancestor) just shells out to real DISM cmdlets
        against a live-mounted image — it does not reimplement the
        format, so no independent offline-format reimplementation exists
        to cross-check against; this would be new ground, but over an XML
        file + shallow registry tree, not an opaque binary blob.
      - **Verdict: feasible now**, not deferred. Reverse-engineered surface
        is narrowly scoped to (1) `AppxProvisioning.xml`'s shape, (2) the
        `AppxAllUserStore\Applications` registry-subtree shape, both with
        provenance = direct extraction from the real 23H2 image noted
        above.
- [x] Implement an `appx`-equivalent package to parse `AppxManifest.xml`.
      Done (2026-07-14): new `appx` module, `manifest.go`'s `Identity`/
      `Manifest`/`ParseManifest`. This part *is* officially documented:
      [Package manifest schema reference for Windows 10 — Microsoft
      Learn](https://learn.microsoft.com/en-us/uwp/schemas/appxpackage/uapmanifestschema/schema-root),
      specifically the `Identity` element
      ([reference](https://learn.microsoft.com/en-us/uwp/schemas/appxpackage/uapmanifestschema/element-identity)):
      `Name`, `Publisher`, `Version` (quad notation), `ProcessorArchitecture`,
      `ResourceId`, under namespace
      `http://schemas.microsoft.com/appx/manifest/foundation/windows10`.
      Only `Identity` is modeled — enough to identify a provisioned
      package's family name for pattern matching, not full manifest
      round-tripping. Verified against a real Windows 11 23H2
      `AppxManifest.xml` (`Microsoft.MicrosoftStickyNotes`, extracted
      2026-07-14 via a read-only `guestmount` of the project's win11 VM
      disk — see `appx/testdata/real_StickyNotes_AppxManifest.xml`).
- [x] Implement `PackageFamilyName` derivation: UTF-16-encode the
      `Publisher` string, SHA-256 it, take the first 8 bytes, Crockford
      Base32-encode them (65 bits: the 64 hash bits followed by one zero
      padding bit, split into 13 groups of 5 bits), lowercase. Done
      (2026-07-14): `appx/familyname.go`'s `PublisherID`/
      `PackageFamilyName`. Not published verbatim by Microsoft as prose
      (only exposed via the `PackageFamilyNameFromId` Win32 API); rather
      than reverse-engineer the bit-packing from scratch, per-instruction
      this was implemented by directly reading source, not guessing from
      prose: `git clone`d
      [russellbanks/package-family-name](https://github.com/russellbanks/package-family-name)
      (a standalone Rust reimplementation) to `/tmp/package-family-name`
      and ported `src/crockford.rs`'s `encode_lower` and
      `src/publisher_id.rs`'s `PublisherId::new` line-for-line into Go.
      Also cited (not directly read/ported, but corroborating the same
      algorithm): [Calculating hash part of MSIX Package Family Name —
      Marcin
      Otorowski](https://marcinotorowski.com/2021/12/19/calculating-hash-part-of-msix-package-family-name/),
      [What's That Gobbledygook in my Package Family Name? — Rafael
      Rivera](https://withinrafael.com/2018/01/28/whats-that-gobbledygook-in-my-package-family-name/),
      [Package Identity - Inside MSIX (Microsoft
      DevBlogs)](https://devblogs.microsoft.com/insidemsix/package-identity/).
      Verified against both the Rust repo's own test vectors
      (`appx/familyname_test.go`'s `TestPublisherIDReferenceValues`) and
      real data: the real StickyNotes `AppxManifest.xml`'s
      `Identity.Publisher` produces `8wekyb3d8bbwe`, matching every
      Microsoft-published package folder name observed in the real image
      (`TestPackageFamilyNameReal`).
- [x] Implement provisioned-package removal, offline, on the un-booted
      image. Done (2026-07-14): `appx/provisioning.go` (`ProvisionList`
      parse/serialize) and `appx/remove.go` (`FamilyNameFromFullName`,
      `RemoveProvisioned`, `Remove`). Re-confirmed the whole shape against
      a fresh real Windows 11 23H2 image (2026-07-14, read-only
      `guestmount` + `hivexregedit --export`, superseding the 2026-07-10
      research entry above): (1) removes the matching
      `<Package FullName="...">` entries (plus bundle/resource/dependency
      siblings, matched via `FamilyNameFromFullName` since a package
      `Name` cannot itself contain `_`) from `AppxProvisioning.xml`,
      optionally adding the family name to `<EndOfLife>` — see
      `appx/testdata/real_AppxProvisioning.xml`, whose real
      `Microsoft.Paint` family is exactly 4 such sibling entries; (2)
      deletes the matching `Applications\<PackageFullName>` key from the
      offline `SOFTWARE` hive, via the now-implemented `registry`/`regf`
      packages (see "Registry generalization" below); (3) adds a
      `AppxAllUserStore\Deprovisioned\<PackageFamilyName>` marker key
      (note: the real marker subkey observed is named exactly the
      package's `PackageFamilyName` itself, i.e. `<name>_<publisherId>` —
      this entry's original phrasing, `<PackageFamilyName>_<PublisherId>`,
      was a copy-paste error, corrected after re-checking the real
      `Deprovisioned` subtree via `hivexregedit --export`); (4) deletes
      `Program Files\WindowsApps\<PackageFullName>` via the sibling `wim`
      package's `DirEntry.Remove` path-based delete API, decrementing blob
      refcounts first (mirroring `driver`'s `Uninstall`/
      `decrementBlobRefs`). Explicitly out of scope: servicing an image
      that has already been through OOBE/specialize (i.e. a
      live-capture/backup image, not a factory ISO) — that would actually
      need the `StateRepository-Machine.srd` SQLite schema, which remains
      undocumented and unaddressed here.

## CBS/servicing package subsystem (new; research first, likely high-risk)

- [x] **Research first:** whether the `COMPONENTS` hive's
      servicing-stack-specific key schema is documented anywhere
      authoritative. Done (2026-07-10): confirmed **no**, definitively, via
      a background research agent that (a) searched official/community
      documentation and (b) directly parsed a real `COMPONENTS` hive
      byte-for-byte (extracted from `/media/gavin-john/tiny11 23H2 x64`'s
      `sources/install.esd` via `wimlib-imagex extract`, read with a
      from-scratch regf-format reader written only for this
      investigation). Findings:
      - No Microsoft Learn/MSDN/TechNet page documents the hive's internal
        key/value schema at all. Microsoft's own conceptual explainer,
        [Understanding Component-Based Servicing —
        AskPerf](https://techcommunity.microsoft.com/blog/askperf/understanding-component-based-servicing/373012),
        stays at the architecture level (CBS vs. CSI, TrustedInstaller)
        and never describes the hive layout.
      - The DFIR/forensics community — which would most want a schema —
        explicitly says it hasn't reverse-engineered one:
        [cybertriage.com's Windows Registry Forensics Cheat Sheet
        2026](https://cybertriage.com/blog/windows-registry-forensics-cheat-sheet-2026/)
        quotes (summarizing Harlan Carvey) *"there is a hive file...named
        'Components.'...nothing significant has been found from a
        forensics or incident response standpoint."*
      - The TODO's own assumed key list
        (`Deployments`/`Components`/`Detects`/`Owners`/`Winners`/`DerivedPlan`/`RM`/`StateProperties`)
        is **already stale**: the real Windows 11 23H2 hive's actual
        top-level layout (confirmed by direct parse) is
        `CanonicalData\{Catalogs (903 subkeys), Deployments (1724
        subkeys)}`, `DerivedData\{Components (16884 subkeys),
        TransformSettingsVersion, VersionedIndex}`, `Drivers\{amd64, x86}`,
        `Installers`, `NonCanonicalData`, `ServicingStackVersions`,
        `CCPInterface` — a different, restructured layout with no stable
        versioned spec anywhere. Individual entries use cryptic,
        unexplained value names (`p!`/`s!`/`i!`/`c!` string prefixes,
        `S256H` — 32 bytes, presumably a SHA-256 digest — and a `CF` DWORD)
        with no documentation of their semantics anywhere found.
      - An internal COM abstraction does exist —
        `ICbsSession` (GUID `{F568C899-AF4F-4EAA-B12A-B8E5F1B219DE}`,
        implemented in `CbsApi.dll`, used by the TrustedInstaller
        service) — per community reverse-engineering write-ups
        ([bsodtutorials.wordpress.com, "Understanding DISM / Servicing
        Stack
        Interaction"](https://bsodtutorials.wordpress.com/2022/07/26/understanding-dism-servicing-stack-interaction/),
        and the ["Windows CBS Image Assembly
        Process"](https://pivotman319-owo.github.io) paper) — but it has
        no public Microsoft Learn/MSDN page, and it only operates against
        a live, booted OS session via TrustedInstaller, so it offers no
        path forward for an offline image regardless.
      - `.mum`/`.manifest` files split cleanly into documented vs. not:
        the generic SxS `<assembly>`/`<assemblyIdentity>` schema (`asm.v1`)
        *is* documented ([Assembly manifests —
        Microsoft
        Learn](https://learn.microsoft.com/en-us/windows/win32/sbscs/assembly-manifests),
        [Application manifests —
        Microsoft
        Learn](https://learn.microsoft.com/en-us/windows/win32/sbscs/application-manifests),
        [Manifest file schema —
        Microsoft
        Learn](https://learn.microsoft.com/en-us/windows/win32/sbscs/manifest-file-schema)),
        but the CBS-specific servicing vocabulary actually used in real
        `.mum` files (`asm.v3` namespace's `<package>`, `<update>`,
        `<parent integrate="delegate">`, `<selectable disposition="staged">`,
        `<detectNone>`) is **not** documented anywhere found — it is plain,
        human-readable XML though, directly inferrable from real samples
        (1262 real `.mum` files extracted and inspected from
        `Windows\servicing\Packages` in the mounted image).
      - `/StartComponentCleanup`/`/ResetBase` are documented only as
        black-box external behavior: [Clean up the WinSxS folder —
        Microsoft
        Learn](https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/clean-up-the-winsxs-folder)
        states *"removes all superseded versions of every component..."*
        and warns existing update packages can no longer be uninstalled
        afterward — no file/registry-level detail, and the same page
        separately warns that manually touching WinSxS "may severely
        damage your system so that your PC might not boot." Grepping all
        1262 real `.mum` files found no `disposition="permanent"` marker
        anywhere, confirming "permanent" supersedence state is pure
        `COMPONENTS`-hive runtime bookkeeping with no manifest-level trace
        to reverse-engineer from.
      - **Verdict: full COMPONENTS-hive-aware removal is not feasible to
        do safely** — no authoritative schema, no independent reference
        implementation to check against (unlike `lzms`/`driver`'s
        precedents, which at least have wimlib to cross-check), a
        confirmed-stale key list, and cryptic undocumented value
        semantics. Recommended scope, adopted below: manifest parsing +
        WinSxS file deletion only, with the `COMPONENTS` hive left
        explicitly untouched/inconsistent as a documented, permanent
        limitation (same pattern as `driver`'s DriverStore-hash non-goal);
        `/ResetBase`-equivalent cleanup scoped out entirely (no offline
        equivalent exists — it requires a live TrustedInstaller/CBS
        session).
  - [x] **Second opinion (2026-07-10):** the user asked for an independent
        re-check, specifically noting the first pass's verification
        program did unusual-looking raw bit manipulation to read the
        registry instead of using this repo's own `regf` module or
        `hivexregedit`. A second background agent (Opus) re-verified
        every empirical claim above using `regf` (as a library, via a
        disposable Go program) and independently via `hivex`
        (`Win::Hivex`), and separately ran a harder documentation search
        aimed squarely at what the first pass might have missed.
      - **All structural claims confirmed exactly** (top-level key names
        and order; `CanonicalData\Catalogs`=903, `\Deployments`=1724;
        `DerivedData\Components`=16884; `Drivers\amd64`=688, `\x86`=2;
        the `appid`/`CatalogThumbprint`/`p!`/`s!`/`i!` value tokens under
        `Deployments`; the `c!`/`S256H`/`identity`/`CF` tokens under
        `Components`) by both `regf` and `hivex` independently reading the
        same real hive.
      - **A real, confirmed bug was found in this repo's own `regf`
        module** in the course of this cross-check (see "Bug found and
        fixed" below) — ironically, the first pass's hand-rolled parser
        had actually gotten the one thing right (real key names) that
        this repo's tested `regf` module got wrong, which is why it
        looked unusual/suspicious in the first place.
      - **Harder documentation search still found nothing authoritative**,
        which strengthens rather than weakens the original verdict.
        Checked and confirmed to have nothing on the COMPONENTS
        hive/CBS internals:
        [geoffchappell.com](https://geoffchappell.com/notes/windows/index.htm)
        (a well-respected undocumented-Windows-internals reference —
        covers Startup/Licensing/IE only, no CBS/servicing content at
        all). Found real but process/algorithm-level-only Microsoft
        patents (no on-disk schema): [US7310801B2 "Servicing a
        component-based software
        product"](https://patents.google.com/patent/US7310801),
        US20040093593A1, US7562346B2, US8060871B2. Found a leaked
        internal C++ class/method name via a Microsoft Q&A support
        thread — `ComponentStore::CRawStoreLayout::OpenCanonicalDataKey`
        from `base\wcp\componentstore\storelayout.cpp` ("WCP" = "Windows
        Componentization Platform") — genuine evidence the internal
        implementation is called `wcp.dll`/wcp, but not a schema.
        Confirmed no CBS-hive-specific plugin exists in
        [EricZimmerman/Registry](https://github.com/EricZimmerman/Registry)
        (the standard DFIR registry-parsing toolkit) or any of
        [ColinFinck/nt-hive](https://github.com/ColinFinck/nt-hive),
        `regipy`, `python-registry`. Confirmed the only offline
        package-management tools that touch anything nearby
        ([himselfv/cbsenum](https://github.com/himselfv/cbsenum),
        `win6x_registry_tweak`) only flip a `Visibility` flag in the
        *SOFTWARE* hive's `Component Based Servicing\Packages` key and
        delegate the actual work to real DISM/`pkgmgr` — none of them
        write the COMPONENTS hive itself. `nano11`/`tiny11builder`
        likewise only read COMPONENTS as metadata and remove packages via
        real DISM (deepwiki.com/ntdevlabs/nano11/4.2 and
        `.../tiny11builder/3.3`). The only software found with apparent
        genuine COMPONENTS write-schema knowledge is closed-source
        (Sysnative's SFCFix / ComponentsScanner).
      - **Verdict unchanged, now confirmed by a second, harder pass**:
        proceed with the manifest-parsing + WinSxS-file-deletion fallback
        scope below; leave the COMPONENTS hive untouched as a documented
        permanent limitation; scope out `/ResetBase`-equivalent cleanup
        entirely.

### Bug found and fixed during this research (2026-07-10)

While cross-checking the COMPONENTS hive with this repo's own `regf`
module, the second-opinion agent found that `regf` silently failed to read
**any** key name from a real Windows-produced hive (it read subkey counts
and values correctly, but every subkey name came back empty). Root cause,
confirmed by direct code inspection: in the `nk` (named-key) cell layout,
`regf/nk.go` had the key-name-size and class-name-size fields swapped —
reading/writing key-name length from offset 74:76 and class-name length
from 72:74, when the documented layout (Joachim Metz's REGF format notes;
also see Project Zero's "The Windows Registry Adventure #4: Hives and the
registry layout",
[projectzero.google/2024/10](https://projectzero.google/2024/10/)) has it
the other way around: offset 72:74 (0x48) is key-name size, 74:76 (0x4a) is
class-name size. This was invisible to `regf`'s own tests because
`AppendTo` wrote the same swap back consistently, so self-round-trip tests
never caught it — it only breaks on real, independently-produced hives
(and any hive built by a *different*, spec-correct writer). Fixed in
`regf/nk.go` (`parseNKCell` and `nkCell.appendTo`); `regf/regf_test.go`'s
hand-built fixture (`buildMinimalHiveBytes`) had independently encoded the
same swap and was corrected too. Re-verified after the fix: `regf` now
reads the real `COMPONENTS` hive's key names correctly (confirmed via a
disposable Go program against the same real hive extracted from
`/media/gavin-john/tiny11 23H2 x64`), and the full workspace
(`gofmt`/`go build`/`go test` across all 10 modules) is clean.

### Suggested next steps if COMPONENTS-hive reverse-engineering is ever authorized

Not started, and not recommended without an explicit, separate decision to
accept the risk — but if a future instruction does authorize it, the
research above points to a concrete starting path rather than a blank
slate:

1. **Differential hive diffing**, not blind guessing: extract the
   `COMPONENTS` hive before and after a real, single, well-understood
   servicing operation (e.g. installing one specific, small update on a
   real or VM'd Windows install, or running `DISM /Add-Package` with one
   package) and diff the two hives key-by-key/value-by-value (now
   practical, since `regf` can correctly read real hives after the fix
   above). This is the same empirical methodology already used
   successfully elsewhere in this project (WIM integrity-table range,
   `HdrFlagCompressXPRESS2`, etc.) and is far more reliable than guessing
   at `p!`/`s!`/`i!`/`c!`/`S256H`/`CF` semantics from naming convention
   alone.
2. **Correlate `Deployments`/`Components` entries against known-good data**
   the meaning of which is already independently known: e.g. confirm
   `S256H` really is SHA-256 of a specific manifest/payload by computing
   SHA-256 of candidate inputs (the `.mum` file bytes, the component
   payload files, the assembly identity string) and checking for a match;
   confirm `CatalogThumbprint` against the real `.cat` catalog files'
   actual signing certificate thumbprints (this repo's own `cat` package
   can already parse those).
3. **Pursue the leaked internal symbol trail**: `wcp.dll`
   (`base\wcp\componentstore\storelayout.cpp`,
   `ComponentStore::CRawStoreLayout::OpenCanonicalDataKey`) is the real
   internal component responsible for this hive. Microsoft's public
   symbol server (`msdl.microsoft.com`) sometimes publishes PDBs for
   system DLLs with private function names even without source — pulling
   `wcp.dll`'s PDB (if available) and listing its exported/internal
   symbol names via a tool like `cvdump`/`llvm-pdbutil` could reveal
   function names (and sometimes struct layouts in type info) describing
   exactly what each hive key/value means, without needing leaked source.
   This was not attempted in either research pass (no confirmed PDB
   availability was found) but is a legitimate, quasi-documented lead
   Microsoft itself provides for exactly this kind of debugging.
4. **Treat any resulting schema as reverse-engineered, not documented**,
   per the project's standing policy: any conclusions from steps 1-3 must
   be written up with full provenance (exact hive bytes/offsets compared,
   exact operations performed to produce the "before"/"after" pair, exact
   tool versions used) in the same style as this TODO's existing
   DriverStore-hash and WIM-integrity-range precedents — never asserted
   as if it were documented Microsoft behavior.
5. Given the real risk of silently producing an inconsistent
   `COMPONENTS` hive that boots but breaks future servicing operations in
   an obscure, hard-to-detect way, **any write support** resulting from
   this research should ship behind an explicit opt-in and with a loud,
   permanent disclaimer — not as a default code path.
### Value-name prefix reverse engineering (2026-07-13)

Explicitly authorized by the user as reverse engineering (not documented
fact) per the project's standing policy — findings below are inferences
from real data, not from any Microsoft source, and are flagged with
confidence levels accordingly.

**Data source:** `~/components-dump.reg`, a full `.reg` export of the live
`HKLM\COMPONENTS` and `HKLM\SOFTWARE` hives from a real, running Windows
system (provided directly by the user, not from the `tiny11` image used in
the 2026-07-10 research above). UTF-16LE text, converted to UTF-8 via
`iconv -f UTF-16LE -t UTF-8` for analysis (final version: 351,355,504 bytes,
2,737,808 lines). Analysis performed with `grep`/`awk` for structural
counts and a disposable Python script parsing the `.reg` text format
(handling multi-line `hex:` continuations) to group values by key and by
base name.

- **`p!` / `s!` / `i!` (under `CanonicalData\Deployments`):** confirmed
  1401 base names where all three prefixes co-occur, and in every one of
  those 1401 cases the three values' data is byte-identical (a
  `uint32 length + uint32 flag(=1) + ASCII name` struct holding the
  original mixed-case package/deployment name, e.g.
  `Package_1_for_KB5028948~31bf3856ad364e35~amd64~~10.0.9176.1.5028948-2_neutral8`).
  Separately, 1013 base names have **only `p!` present** — and every one
  of those belongs to `kb5066133` packages, while every base name for the
  older `kb5028948`/`kb5031274` has the full `p`+`s`+`i` triplet (verified
  by grouping all `Deployments` value base-names by their embedded `kb\d+`
  substring: `5028948 → {i,p,s}`, `5031274 → {i,p,s}`, `5066133 → {p}`
  only).
  - **Corroborating evidence found elsewhere in the same dump:**
    `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based
    Servicing\SessionsPending\<id>` contains real, plaintext CBS session
    lifecycle timestamps on this machine, e.g.
    `"1_Started"="2026/07/09/10:24:40"`, `"1_Planned"=...`,
    `"1_Resolved"=...`, `"1_Staged"=...`, `"1_Installed"=...`,
    `"1_Complete"=...` — six named phases, consistently present across all
    6 `SessionsPending` subkeys in the dump (`Started`×6, `Planned`×6,
    `Resolved`×5, `Staged`×5, `Installed`×5, `Complete`×6).
  - **Inference (moderate-high confidence):** `p!`/`s!`/`i!` are per-package
    state markers named after three of these same six lifecycle phases —
    **P**lanned, **S**taged, **I**nstalled — written progressively as a
    deployment advances (a newer, not-yet-fully-progressed KB has only
    `p!`; older, settled KBs have all three). Not confirmed by a direct
    before/after diff of one deployment transitioning through the states
    (the methodology suggested in the 2026-07-10 "next steps" section
    below) — that remains the real test.
- **`c!` (under both `CanonicalData\Catalogs` and `DerivedData\Components`):**
  every occurrence found has **empty data** (`"c!<name>"=hex:` with no
  bytes), unlike `p!`/`s!`/`i!`. The `<name>` portion is always the outer
  key name of a *different* record — e.g. inside
  `DerivedData\Components\amd64_022bd29263008e5688235b714058746f_..._d13fd75b426163b5`,
  the value `"c!022bd292630..b714058746f_b77a5c561934e089_4.0.15912.251_d13fd75b426163b5"`
  names exactly the sibling `CanonicalData\Deployments` key that owns this
  component.
  - **Inference (moderate confidence):** `c!` is a reverse-index/backlink
    marker — "this Deployment touched this Component" / "this Catalog
    covers this identity" — where the value's mere presence (not its data)
    is the signal, structurally distinct from the `p`/`s`/`i` lifecycle
    stamps. Best label guess: **c**omponent cross-reference. Not
    independently confirmed against a second data source the way `p/s/i`
    was.
- **`S256H` (32-byte value, presumed SHA-256 digest):** tested the
  hypothesis that it's `SHA256(identity string)` directly, using the exact
  bytes of a real sibling `"identity"` value
  (`022bd29263008e5688235b714058746f, Culture=neutral, Version=4.0.15912.251,
  PublicKeyToken=b77a5c561934e089, ProcessorArchitecture=amd64,
  versionScope=NonSxS`) against the real `S256H` value found alongside it
  (`72d0a662ad2721ba2a5df925a958c064eacd3fa7e58f95217a662f6c4f9eb1d0`).
  **Ruled out**: no match for ASCII, ASCII+NUL, UTF-16LE, UTF-16LE+NUL,
  upper/lowercased, name-only, comma-without-space, or attribute-order
  variants (10 encoding/format variants tried, all computed and compared
  by script, none matched). `S256H` is therefore *not* a hash of the
  identity string in any straightforward textual form.
  - **Follow-up same day, against the real source VM**: the user granted
    read-only access to the actual shut-down VM
    (`/var/lib/libvirt/images/win11.qcow2`) this dump came from, mounted
    via `guestmount --ro -m /dev/sda3` (NTFS partition; read access to the
    root-owned `.qcow2` required `pkexec chmod +r`, at the user's explicit
    direction — no write/modification made to the guest itself). Located
    and hashed the two real on-disk files matching this exact identity:
    `Windows\servicing\Packages\Package_1_for_KB5028948~31bf3856ad364e35~amd64~~10.0.9176.1.mum`
    (SHA-256 `c92b718796e7461836a93ae1d5d3f5acea1a3ad22a0b9ac314bfda43d2816717`)
    and
    `Windows\WinSxS\Manifests\amd64_022bd29263008e5688235b714058746f_b77a5c561934e089_4.0.15912.251_none_d13fd75b426163b5.manifest`
    (SHA-256 `abf63a1d66cba122aadb1cb019d0b6c59db3076b53104c32d6b1b26c856dc8ab`).
    **Neither matches** the target `S256H`
    (`72d0a662ad2721ba2a5df925a958c064eacd3fa7e58f95217a66...`), ruling out
    "whole-file hash of either the `.mum` or the raw on-disk `.manifest`
    bytes" too.
  - **`S256H` mystery resolved (2026-07-13, via the working `pa30` decoder
    below):** once `pa30.DecodeWithSource` could fully decode this exact
    file (identity `022bd29263008e5688235b714058746f`,
    `4.0.15912.251`/`b77a5c561934e089`/amd64 — the same file used for the
    S256H test above), `sha256sum` of the **decompressed** 659-byte manifest
    XML content is
    `72d0a662ad2721ba2a5df925a958c064eacd3fa7e58f95217a662f6c4f9eb1d0` —
    an **exact match** for the target `S256H` value. So: `S256H` is SHA-256
    of the decompressed WinSxS `.manifest` XML content, not of the raw
    on-disk (PA30-compressed) file bytes, and not of the `identity` string
    in any form — the thing this section's original hypothesis tests
    couldn't check because a working PA30 decoder didn't exist yet at the
    time. This closes out the `S256H` line of investigation from both the
    2026-07-10 and earlier-2026-07-13 passes.
  - **Significant unplanned discovery in the course of this test**: the
    `.manifest` file is not plain XML at all — `xxd` shows it begins
    `44 43 4d 01 50 41 33 30` (`"DCM"` + `\x01` + `"PA30"`), and a sample of
    50 real files from this VM's `Windows\WinSxS\Manifests\` came back
    **50/50 DCM-headered, 0 plain-XML**, while every sampled
    `Windows\servicing\Packages\*.mum` file (e.g. the KB5028948 one above)
    is confirmed plain UTF-8 XML (`<?xml version="1.0"...?><assembly
    xmlns="urn:schemas-microsoft-com:asm.v3"...`). Web research (queries:
    `"PA30" manifest compression WinSxS MS-PATCH delta format`, `DCM PA30
    header WinSxS compressed manifest decompress`) confirms this is a real,
    named format: since Windows 8, WinSxS `.manifest` files are
    **null-delta compressed** using Microsoft's Patch API delta format
    (`PA19`/legacy, `PA30`/CBS-era, `PA31`/later refinement — [Decoding
    Windows CBS Manifests: Reversing the DCM/PA30 Delta Format —
    Cobalt.io](https://www.cobalt.io/blog/decoding-windows-cbs-manifests-reversing-the-dcm/pa30-delta-format)).
    That article, checked directly, states plainly: *"the on-the-wire byte
    layout is not officially documented, which is why all existing tooling
    ... works at the API level rather than parsing the bytes directly"* —
    i.e. even the best public write-up found does not give a portable
    decoding algorithm, only confirms which Win32 API
    (`ApplyDeltaB`/`msdelta.dll`, or `UpdateCompression.dll` on Windows 11)
    actually performs the decompression. Cross-checked three real existing
    tools by cloning them (`git clone`, not GitHub API, per repo policy) to
    confirm none reimplement PA30 portably: `wcpex`
    ([smx-smx/wcpex](https://github.com/smx-smx/wcpex)) and `SXSEXP`
    ([hfiref0x/SXSEXP](https://github.com/hfiref0x/SXSEXP)) both require a
    genuine Windows `wcp.dll`/`msdelta.dll` at runtime (Windows-only,
    confirmed from `wcpex`'s own README: *"Requires a wcp.dll from the
    servicing stack in the search path"*); a third,
    [martinosani/Win-CBS-Manifest-Decoder](https://github.com/martinosani/Win-CBS-Manifest-Decoder)
    (found via the Cobalt.io article), likewise wraps the same Windows API
    via `ctypes` rather than reimplementing the format.
  - **Consequence for this project's plans**: the existing "Implement
    parsing of servicing package manifests (`.mum`/`.manifest` XML)" TODO
    item below conflated two actually-different, differently-shaped
    formats. `.mum` files (`Windows\servicing\Packages\*.mum`) are
    confirmed plain XML, parseable in pure Go today with a normal XML
    decoder. WinSxS `.manifest` files
    (`Windows\WinSxS\Manifests\*.manifest`) are PA30-delta-compressed
    binary, and — per the research above — **no portable, non-Windows-API
    decoder for PA30 is known to exist anywhere**, which would block any
    pure-Go attempt to read them short of reimplementing an undocumented
    Microsoft delta-compression format from scratch (a much larger, riskier
    undertaking than originally scoped, on the same order of difficulty as
    the already-rejected COMPONENTS-hive schema reverse-engineering). This
    doesn't block the currently-planned scope (manifest parsing was always
    meant to read `.mum` files primarily, per the "asm.v3" research on
    2026-07-10 which itself sampled real `.mum` files, not `.manifest`
    files) but it does mean **any future plan to read WinSxS `.manifest`
    files directly should be treated as a new, separate, likely-infeasible
    research item**, not an assumed extension of `.mum` parsing.
  - `CF` (a DWORD, observed value `0x0000000c` on one real entry) remains
    completely untested — no candidate hypothesis proposed yet.
- **Update (2026-07-13), user-provided lead:** the user pointed at
  [smilingthax/msdelta-pa30-format](https://github.com/smilingthax/msdelta-pa30-format),
  which **contradicts** the "no portable, non-Windows-API decoder for PA30
  is known to exist anywhere" conclusion just above. Cloned (`git clone`,
  not GitHub's API, per repo policy) to
  `/tmp/claude-1000/.../scratchpad/clones/msdelta-pa30-format` and read in
  full, plus built it locally (`make`, succeeds with only harmless
  `%llx`/`int64_t` format-string warnings on this machine's glibc/gcc).
  This is a genuine, from-scratch, pure-C, no-Windows-API decoder — not
  another `msdelta.dll`/`wcp.dll` wrapper like `wcpex`/`SXSEXP` were found
  to be:
  - Its `README.md` documents the on-the-wire `PA30` bitstream format in
    real detail: the outer header (`GetDeltaInfo`-shaped:
    `FileTypeSet`/`FileType`/`Flags`/`TargetSize`/`TargetHash`), the LSB-first
    bit-packed integer/buffer encodings, the composite Huffman-parameter
    format (three concatenated canonical Huffman trees — `main`/`length`/
    `aligned offset`, explicitly modeled on **MS-PATCH**'s documented LZX
    Delta canonical-Huffman convention, cited directly:
    [\[MS-PATCH\]: LZX DELTA Compression and Decompression —
    Microsoft](https://interoperability.blob.core.windows.net/files/MS-PATCH/%5bMS-PATCH%5d.pdf)),
    the match-type encoding (literal / `SRC` delta / `FULLSRC` / LRU-repeat
    / `DST` offset, with slot-based offset-bit-width coding closely
    mirroring this repo's own `lzx` module's slot scheme), and RLE-delta
    Huffman-length coding. Corroborated against real Microsoft patents (now
    expired): [US6938109B1](https://patents.google.com/patent/US6938109B1/en)
    (prepend-old-data dictionary priming), [US6466999](https://patents.google.com/patent/US6466999/en)
    ("rift table" preprocessing), and cross-checked strings extracted
    directly from a real `msdelta.dll` (a `wrap`/`BitReaderOpen`/
    `CheckBuffersIdentity`-style internal "processing graph" string dump),
    which is itself a legitimate, if informal, reverse-engineering
    technique (reading literal internal string constants shipped in the
    real binary, not guessing).
  - Read `dump.c`/`getdeltainfo.c`/`bitreader/`/`plzx/` directly (not just
    the README) to verify the README's claims are backed by real code, not
    aspirational: `dpa_GetDeltaInfo` fully parses the `PA30` header per the
    documented layout; `dump_patch` fully decodes the composite-Huffman
    compressor bitstream end-to-end, correctly handling all five match
    types (`LITERAL`/`SRC`/`FULLSRC`/`LRU`/`DST`) including LRU-queue update
    semantics the code explicitly flags as differing from LZX Delta's
    partial LRU support ("NOTE: this is a full implementation, unlike lzx
    delta!").
  - **Real, honestly-documented limitations found by reading the code**,
    not claimed anywhere as complete: (1) `dump_patch` explicitly bails
    out (`"non-empty base rift table not yet supported"`) whenever the
    patch's base rift table is non-empty — i.e. it only handles the
    no-source-file / null-delta case, not general source-to-target
    diffing. This is exactly the WinSxS-manifest-compression case this
    project cares about (manifests are null-delta/self-compressed, not
    diffed against a prior version, matching the "many PA30-files don't
    use/set those transforms... (source is empty buffer)" observation the
    TODO's earlier PA30 research already made), so the limitation may not
    actually block this project's narrow use case — but this has **not
    been verified** against a real WinSxS `.manifest` file yet (the
    `tiny11` image was not mounted at the time of this check; the tool was
    only build-tested, not run against real data). (2) `dump_patch`
    *prints* the decoded match stream (literal bytes, copy operations) but
    does not itself materialize a reconstructed output buffer — turning it
    into an actual "decompress to bytes" function is a small, mechanical
    change (replace the `printf` calls with writes into an output buffer),
    not a research gap. (3) The "Preprocessing buffer" section (file-type-
    specific normalization, e.g. for PE executables) is explicitly marked
    `TODO` in the README and unimplemented — irrelevant for manifests
    (non-PE files), so likely a non-issue for this project's use case, but
    would matter for any future attempt to decode PA30 patches over `.exe`/
    `.dll` payloads specifically. (4) No `LICENSE` file exists in the repo
    — reuse/redistribution terms are unclear and would need to be
    resolved (e.g. by asking the author) before vendoring or porting any
    of this code into `gowim` itself.
  - **Revises the CBS-manifest conclusion above**: "no portable,
    non-Windows-API decoder for PA30 is known to exist anywhere" is now
    known to be **false** — a real one exists, is buildable, and (by code
    reading) appears to correctly implement the exact null-delta case this
    project would need for WinSxS `.manifest` files, though this project
    has not yet independently verified that against real manifest bytes.
    This means "read WinSxS `.manifest` files directly" should be
    downgraded from "likely-infeasible, treat as separate research" (as
    concluded just above) back to **plausible**, contingent on: (a)
    resolving licensing, (b) verifying `dump_patch`'s output against a
    real `.manifest` file's known-correct decompressed XML (e.g. cross-
    checked via a real Windows `ApplyDeltaB`/`msdelta.dll` call on a
    Windows host, or via one of the wrapper tools `wcpex`/`SXSEXP` found
    earlier, as ground truth), and (c) either porting this C
    implementation to Go or writing a from-scratch Go port cross-checked
    against it output-for-output — not a small effort, but a substantially
    more tractable one than "reverse-engineer PA30 from zero," which was
    the premise of the earlier "likely-infeasible" framing.
- **Also found and cross-checked while researching this:** [Jon Wiswall's
  "What's that awful directory name under WindowsWinSxS?" (2005, Microsoft
  archived
  blog)](https://learn.microsoft.com/en-us/archive/blogs/jonwis/whats-that-awful-directory-name-under-windowswinsxs),
  found by the user — a genuine ex-Microsoft-CBS-engineer source (rare tier
  above community RE, though still not a formal schema doc, and predates
  the modern Deployments/Components hive design). Confirms the *outer* key
  naming ("keyform") scheme actually observed in this dump:
  `procarch_name_pkt_version_culture_hash`, with `name` truncated to 64
  chars (middle replaced by `..`) and a trailing non-cryptographic hash
  covering everything not captured in the truncated text — matching
  exactly the shape of real keys like
  `amd64_022bd29263008e5688235b714058746f_b77a5c561934e089_4.0.15912.251_none_d13fd75b426163b5`.
  Explicitly states the hash algorithm itself is undocumented by design and
  "must be assumed to change" — and says nothing about the internal
  `p!`/`s!`/`i!`/`c!`/`S256H`/`CF` value-name/data schema, which remains
  this session's original research, not covered by this source.

### PA30 code-reading methodology (2026-07-13)

Working method adopted for `msdelta-pa30-format`, and intended to be reused
for any future deep dive into a third-party reference implementation in this
project: read the repo's own `README.md` first as the primary spec, then
dispatch a background subagent with a specific list of open questions
(usually the README's own "TODO"/"(?)" markers) to answer by reading the
actual source, citing file/function for each claim — rather than reading the
whole codebase inline and burning main-session context on C implementation
detail that's only relevant to answering a handful of pointed questions.

Questions asked of `msdelta-pa30-format` (cloned at
`/tmp/.../scratchpad/clones/msdelta-pa30-format`, see the 2026-07-13 entry
above) and the code-cited answers:

- **Rift table encoding** (README leaves this a bare "TODO" beyond a leading
  `isNonEmpty` bit): unimplemented in the reference tool. `dump.c` reads only
  that one bit; if set, the code prints "non-empty base rift table not yet
  supported" and bails — no interval/pair decoding exists anywhere in the
  repo.
- **Rift-table Huffman coding**: not implemented anywhere; the only Huffman
  paths in the repo (`bitreader/huffman.c`, `plzx/huffdec.c`) are for the
  PLZX main/length/aligned trees and the length-RLE pre-tree, unrelated to
  rift tables.
- **Match type 4 (`FULLSRC`)**, marked "(TODO??)" in the README: the
  bitstream-level symbol/slot is recognized (`plzx/plzxtypes.h`,
  `plzx/huffdec.c`'s `_dpa_plzxhuffdec_read_match`), but no code computes
  `rift_offset(output_position)` or performs the actual cross-rift-segment
  byte copy — `dump.c` only prints the decoded length, it never materializes
  output bytes for this match type (the tool is a bitstream *dumper*, not a
  full decompressor).
- **Preprocessing buffer**: entirely unimplemented/stubbed.
  `getdeltainfo.c`'s `dpa_GetDeltaInfo` only extracts its byte span; `dump.c`
  explicitly prints "TODO: support preproc info" whenever that span is
  non-empty and does nothing further.
- **Does any of the above matter for the null-delta (empty source buffer)
  case WinSxS manifests actually use?** Largely no, by inference (not an
  explicit code check): `dump.c`'s own inline comment notes the rift-merge
  logic "seems to translate outpos=srcSize to 0" for the empty-source case,
  i.e. degenerates to identity. `SRC`/`FULLSRC` match types exist to
  reference a *source* buffer, which is empty for manifests, so a real
  encoder should never emit them for this case — meaning only `LITERAL`,
  `LRU`, and `DST` match types (plus the trivial `isNonEmpty=0` rift bit)
  should be load-bearing for manifest decoding, and the unimplemented rift
  table/Huffman/`FULLSRC`-semantics gaps above can likely be ignored for that
  narrow use case. Flagged explicitly as an inference, not something the
  code proves outright: the decoder has no `if srcSize==0, skip SRC/FULLSRC`
  guard, it would still try to parse those symbols if a Huffman code
  happened to select them.
- **License**: confirmed, still, no `LICENSE`/`COPYING` file and no
  copyright/license text anywhere in `.c`/`.h`/`.md` files (grepped
  directly) — reuse terms remain unresolved, per the licensing item below.

### Component store implementation plan (added 2026-07-13)

Concrete build order, given the 2026-07-13 PA30-decoder lead above changed
"read WinSxS `.manifest` files" from likely-infeasible back to plausible.
Each step should re-verify its own assumptions against real data before the
next step depends on them, same discipline as the research above.

- [x] Resolve `msdelta-pa30-format`'s licensing question. Done (2026-07-13):
      sidestepped entirely via the clean-room approach — the implementer
      never read that repo's C source directly (still no `LICENSE` file
      there, confirmed unchanged); a background research agent read it
      instead and answered a fixed list of implementation questions in
      prose (citing file/function per answer, no substantial code quoted
      back), which combined with the README's own worked example was
      enough to implement independently. See the "PA30 code-reading
      methodology" entry above and `pa30/doc.go`/`pa30/README.md` for the
      full trail.
- [x] Implement a `pa30` Go module: `GetDeltaInfo`-shaped header parser
      (`FileTypeSet`/`FileType`/`Flags`/`TargetSize`/`TargetHash`), LSB-first
      bit reader, the composite canonical-Huffman parameter decoder
      (concatenated `main`/`length`/`aligned-offset` trees, modeled on
      MS-PATCH's documented LZX Delta convention), and the match-type
      decoder (`LITERAL`/`SRC`/`FULLSRC`/`LRU`/`DST`) — but, unlike the
      reference tool, materializing an actual output byte buffer instead of
      printing the match stream. Scoped to the null-delta/empty-base-rift-
      table case, since that's the only case WinSxS manifests need
      (self-compressed, not diffed against a prior version); non-empty rift
      tables, SRC/FULLSRC matches, and non-empty preprocessing all return
      explicit errors rather than being decoded. Done: new `pa30` module
      (`pa30.Decode`), clean-room implemented per the licensing item above.
      Tested against: the reference README's own worked bit-level example
      (real ground truth, not self-derived) for the bit reader/integer
      decoder; hand-computed canonical Huffman codewords for the Huffman
      engine; hand-verified default-length-formula arithmetic; and a full
      synthetic PA30 file (via a matching test-only bit writer) exercising
      the whole pipeline end-to-end (literal + DST-match decoding, rift-
      table rejection). **Not yet verified against a real WinSxS
      `.manifest` file** — see the next item, still open.
- [x] Verify the `pa30` decoder's output against real
      `Windows\WinSxS\Manifests\*.manifest` bytes using an independent
      ground truth (a real `ApplyDeltaB`/`msdelta.dll` call on a Windows
      host, or the `wcpex`/`SXSEXP` wrapper tools as an oracle) before
      trusting it for anything downstream. **Done (2026-07-13) for the
      scope this package actually implements** (DST/LRU back-references
      into a source buffer) — a real Huffman-construction bug was found and
      fixed, a major scope discovery was made (real files aren't
      null-delta), the shared source buffer was located/extracted, and a
      real file was fully, correctly decoded end-to-end for the first
      time, cross-validated against independent data. SRC/FULLSRC matches
      remain unimplemented (separate item below) — full findings below.
      - **Method:** mounted `/var/lib/libvirt/images/win11.qcow2` read-only
        (`guestmount --ro`), pulled real `.manifest` files from
        `Windows\WinSxS\Manifests`, and used the already-cloned/built
        `msdelta-pa30-format`'s `dump` binary as a **black-box oracle**
        (ran the compiled tool, read only its stdout — did not read/consult
        its source for this pass, consistent with the clean-room policy
        above). All real `.manifest` files begin with an 8-byte `"DCM"` +
        1 version byte prefix before the `PA30` signature (confirmed by
        `xxd`); this prefix must be stripped before either decoder sees the
        data.
      - **Header parsing confirmed byte-perfect against the oracle:** for
        every sampled file, this package's `Header` (`FileTypeSet`,
        `FileType`, `Flags`, `TargetSize`, `TargetFileTime`,
        `TargetHashAlgId`) matched the oracle's `GetDeltaInfo` printout
        exactly, field-for-field, including the byte offset/length of the
        extracted `patchBuffer` (`patch start: 23, len: 127` matched
        exactly). `Flags=0x20000` matched on every one of 36 sampled real
        files, which also happens to be the exact value the reference
        README's own `DELTA_HEADER_INFO` example uses — strong independent
        corroboration. The base-rift-table `isNonEmpty` bit read as 0 and
        matched the oracle's own "non-empty: 0" printout exactly.
      - **Major scope-changing discovery: real WinSxS `.manifest` files are
        NOT null-delta.** Sampled 36 real files (the smallest ~30 by disk
        size, plus a handful from the original 2026-07-13 research) and ran
        the oracle against all of them: **every single one** failed with
        "bad dst match offset" referencing an offset of roughly 9000-10000
        at output positions in the 3-130 range — i.e., every real manifest
        is delta-compressed against a large (~9-10KB), evidently
        **shared/common** source buffer, not an empty one. (30 of the 36
        failed at the *exact* identical offset **9016** and output position
        **130**, strongly suggesting a single universal baseline template
        — likely the common XML boilerplate/namespace declarations shared
        by every manifest — used across the board, not a per-file
        previous-version diff.) This invalidates the original "many
        PA30-files don't use/set those transforms... (source is empty
        buffer)" assumption **for this specific file class**: the
        null-delta-only scope this package deliberately committed to
        covers a case real `.manifest` files essentially never hit past the
        first ~130 bytes. Full `.manifest` decoding will need this shared
        baseline source buffer identified and obtained before SRC/FULLSRC
        matches can be resolved; that's new, separate scope, not previously
        anticipated.
      - **Update (2026-07-13), user-provided leads — the shared source
        buffer's origin is now identified, not just suspected.** The user
        pointed at two Cobalt.io articles
        ([part 1](https://www.cobalt.io/blog/decoding-windows-cbs-manifests-reversing-the-dcm/pa30-delta-format),
        [part 2](https://www.cobalt.io/blog/part-2-decoding-windows-cbs-manifests-building-the-decoder))
        plus two forum threads
        ([msfn.org](https://msfn.org/board/topic/175704-windows-update-package-structure-analysis/),
        [ghisler.ch](https://www.ghisler.ch/board/viewtopic.php?t=49446) —
        both checked and confirmed to contain no PA30 bitstream-level
        detail, just high-level format mentions). Part 2, fetched and read,
        **confirms the shared dictionary directly**: *"Resource type 0x266
        (decimal 614), name 1[, in `wcp.dll`]... This is the base buffer
        that the delta is applied against"* — loaded internally via a
        function the article identifies by its mangled C++ symbol calling
        `Windows::Rtl::LoadFirstResourceLanguageAgnostic`. The article
        confirms its contents begin with `<?xml version='1.0'`, consistent
        with this session's own empirical finding (a large shared XML
        boilerplate prefix common to every manifest). Neither Cobalt.io
        article covers Huffman/bitstream-level detail (both explicitly
        focus on the DLL-call pipeline, not the on-the-wire byte layout;
        part 1 states outright *"the on-the-wire byte layout is not
        officially documented"*), so this does not resolve the decode-bug
        item below, but it does turn "where does the shared buffer come
        from" from an open question into a concrete, actionable one:
        extract PE resource type 0x266, name 1, language-agnostic, from a
        real `wcp.dll` (this repo already has a `pe` package that could
        plausibly do this), and use its bytes as the prepended SRC/FULLSRC
        source buffer once the decode-bug item below is resolved. Not yet
        attempted.
      - **A real, reproducible, currently-unresolved bug found in this
        package's content-Huffman decode:** on every sampled file, this
        package's `Decode` desyncs at the very first main-tree symbol,
        while the oracle correctly decodes the first three bytes as
        literals `0xEF 0xBB 0xBF` (a UTF-8 BOM) before *it* also hits the
        shared-source-buffer limitation above. This package instead
        decodes a match-type symbol immediately at output position 0 (e.g.
        symbol 327), which is impossible to satisfy at position 0 by
        construction — proof of a genuine decode bug, independent of the
        shared-source-buffer issue. A same-session debugging pass tried and
        ruled out several concrete hypotheses against this exact real
        bitstream (with the known-correct `[0xEF,0xBB,0xBF]` prefix as a
        checkable target): reversing the default-length short/long split
        direction, exhaustively scanning every possible split threshold
        (0-600) in both directions, both LSB-first and MSB-first codeword
        bit-accumulation, and with/without an assumed 3-bit pad prefix at
        the start of `patchBuffer`'s nested bitstream — **none reproduced
        the correct symbol sequence**, meaning the bug is not simply a
        split-point or bit-order-direction mixup in the default-Huffman
        path. A separate "isDefault polarity is flipped" hypothesis (bit
        value 1 actually means *custom* blocks, not *default*) looked
        initially promising — it parses a structurally plausible
        `num_blocks=2`, in-range block-start offsets, and a
        plausible-looking 39-symbol pretree — but then fails partway
        through the RLE-coded length decode, so it isn't confirmed either.
      - **Bug found and fixed (2026-07-13).** Handed the exact failure (real
        bitstream bytes, the oracle's confirmed-correct
        `[0xEF,0xBB,0xBF]`-then-fails-on-shared-source trace, and the list
        of ruled-out hypotheses above) to a fresh background research
        subagent, which read and temporarily instrumented its own copy of
        `msdelta-pa30-format`'s C source (`dump.c`, `plzx/composite.c`,
        `plzx/huffdec.c`, `bitreader/huffman.c`), ran the reference `dump`
        binary against the exact real bytes, and additionally wrote an
        independent Python port to confirm its explanation byte-for-byte —
        all per the same clean-room "describe, don't quote" policy as the
        original code-reading pass. Root cause: this package's canonical
        Huffman code construction used the textbook DEFLATE-style
        **bottom-up** threshold recurrence (shortest code length gets the
        smallest code values, built by doubling as length increases).
        PA30's actual construction (`huffman.c:dpa_huffdec_from_lengths`) is
        **top-down**: the *longest* code length gets the smallest code
        values (starting at 0), built by a *halving* recurrence from the
        longest length down to the shortest. Symbol-to-array-slot ordering
        itself (ascending length, then ascending symbol index) is
        unchanged from the textbook version — only the per-length
        code-value threshold differs, which also means decode must check a
        full two-sided range (`first[l] <= code < first[l]+count[l]`), not
        just an upper-bound difference, since a growing code prefix is no
        longer guaranteed to stay above `first[l]` as more bits are read.
        Fixed in `pa30/huffman.go` (`buildHuffmanTree`/`decode`); re-ran
        against the real file and got the exact reference-confirmed
        literal/match sequence (`0xEF`, `0xBB`, `0xBF`, then the same
        shared-dictionary reference the oracle also fails on, at the exact
        same output offset). Added `TestDecodeRealManifestSample`
        (`pa30/pa30_test.go`) as a permanent regression test against this
        real file + the oracle-confirmed expected header/failure-point, and
        corrected the two existing hand-computed-codeword tests
        (`TestHuffmanDecodeHandComputedCodes`, `pa30_test.go`'s
        `canonicalCodeword` helper) which had encoded the old, wrong
        scheme's expected values. **Net result: this package's header
        parsing and Huffman/literal/back-reference decoding now match the
        real, independent reference oracle exactly** on every real file
        tried — the only remaining gap is the shared-dictionary buffer
        itself (next item), not a decode bug.
      - **Shared dictionary extracted, and a real file fully decoded
        end-to-end (2026-07-13).** Tasked a background research agent
        (given explicit permission to mount/read the VM) to obtain the
        actual dictionary bytes identified above (PE resource type 614/
        0x266, name 1, in `wcp.dll`). It mounted the VM read-only, found
        `wcp.dll` only inside WinSxS (not directly under `System32` on this
        build) at
        `Windows\WinSxS\amd64_microsoft-windows-servicingstack_31bf3856ad364e35_10.0.22621.6120_none_e967976c42c72025\wcp.dll`,
        and extracted resource 614/name 1/language 1033 (no neutral-language
        entry existed) via `wrestool` (icoutils) — a standard, documented
        PE-resource-directory extraction, not reverse engineering (only the
        resource *ID* to look for came from the Cobalt.io article). Result:
        exactly **9066 bytes**, starting with `<?xml version="1.0"
        encoding="UTF-8" standalone="yes"?>` — confirming the Cobalt.io
        description almost exactly (double vs. single quotes aside). Saved
        as `pa30/testdata/wcp_dictionary.bin`.

        This 9066-byte size independently self-confirms against the DST
        offsets observed earlier: `dictSize + targetPosition` equals the
        exact "bad dst match offset" values the oracle reported (e.g.
        `9066 + 3 = 9069`, matching "offset 9069 at 3" for the first sampled
        file exactly; `9066 + 130 = 9196`, and `9196 - 9016 = 180 < 9066`,
        a valid in-dictionary reference, for the 30 files that hit "offset
        9016 at 130"). This confirms the coordinate model: the dictionary
        is conceptually prepended before the target buffer, and back-
        reference offsets are measured from the current position in that
        combined space.

        Wired this into `pa30`: `parsePatchBuffer`/`decodeContent` now take
        an optional `source []byte`, prepended to the internal output
        buffer (stripped back off before returning) so DST/LRU matches can
        reach into it; a new exported `DecodeWithSource(data, source)`
        wraps this (`Decode` is now `DecodeWithSource(data, nil)`).
        SRC/FULLSRC matches (slots 0-3) still return an explicit error
        either way — their delta/rift-offset addressing scheme remains
        unimplemented, independent of whether a source buffer is present.

        Ran `DecodeWithSource` against the same real file used throughout
        this investigation, with the extracted dictionary: it now decodes
        **fully and correctly**, exactly 659 bytes matching `TargetSize`,
        parseable by the `mum` package as a well-formed `<assembly>`
        manifest (`assemblyIdentity name="022bd29263008e5688235b714058746f"
        version="4.0.15912.251"`, plus `<deployment>` and
        `<dependency>`/`<dependentAssembly>` elements) — **the first real
        WinSxS `.manifest` file ever fully decoded by this project.**
        Cross-validated independently: this decoded output's SHA-256
        (`72d0a662ad2721ba2a5df925a958c064eacd3fa7e58f95217a662f6c4f9eb1d0`)
        is an **exact match** for the `S256H` registry value this project
        separately found (and could not explain) for the same component
        identity while reverse-engineering the `COMPONENTS` hive back on
        2026-07-13 — see the "S256H mystery resolved" entry above. Two
        unrelated research threads in this project now corroborate each
        other, strong independent evidence the decoder is genuinely
        correct, not just self-consistent. Added
        `TestDecodeWithSourceRealManifestFullSuccess` (embeds both the real
        manifest and the extracted dictionary) as a permanent regression
        test for this. The 5 other originally-sampled real files still fail
        (now cleanly, with an explicit "SRC/FULLSRC match ... not
        supported" error rather than a wrong decode) — they need
        SRC/FULLSRC support to fully decode, confirmed to be a real,
        separate scope gap, not a bug.
- [x] Implement SRC/FULLSRC match decoding (the delta/rift-offset addressing
      scheme distinct from the DST/LRU back-references `pa30` already
      supported). Done (2026-07-13) — implemented per best available
      evidence (disassembly, no reference implementation existed), then a
      real bug in that first pass was caught and fixed by measuring full
      real-corpus coverage, after which **all 17189 real `.manifest` files
      in a real Windows 11 23H2 image decode successfully, each
      cryptographically hash-verified** — see the coverage measurement
      entry below for the fix and its validation.
      - **No reference implementation existed to clean-room-read from**,
        unlike everything else in `pa30`: the `msdelta-pa30-format` reference
        tool's own `dump.c` recognizes SRC/FULLSRC's bitstream symbols but
        never computes a real source address for them (only prints decoded
        length) — confirmed by a research agent as a genuine gap in the
        reference tool itself, not a documentation-reading miss on this
        project's part.
      - **Patents exhausted, found not to apply.** The two patents the
        reference tool cites (US6466999 "rift table"; US6938109B1
        "prepend-old-data" LZ77 window priming) were read in full by a
        background agent. Neither describes PA30's actual slot/delta
        bitstream encoding: US6466999 covers an *offline preprocessing*
        step (rewriting jump/call targets between old/new executables using
        a symbol-table-derived correction, not decode-time match
        addressing); US6938109B1 only illustrates window pre-loading with a
        trivial example, no offset arithmetic. Both are older/adjacent
        mechanisms PA30's implementer evidently borrowed *terminology* from
        (confirmed: the reference tool's own README cites both patents by
        number), not the mechanism itself.
      - **User explicitly redirected away from booting the VM mid-session**
        (a tool-permission denial: "instead of booting the VM, inspect the
        binaries in a subagent to clean-room RE it") when a subagent's first
        step attempted `virsh`-based VM boot/execution to use the real,
        documented `ApplyDeltaB` Win32 API (`msdelta.dll`) as a live
        black-box oracle. Pivoted, per that instruction, to static
        disassembly instead: extracted the real `msdelta.dll` read-only via
        `virt-cat` (no mount, no boot — same read-only libguestfs-family
        tooling as all prior VM research in this project), confirmed its
        `ApplyDeltaB`/`CreateDeltaB` exports, and had a background agent
        trace `ApplyDeltaB`'s call graph and disassemble its match-dispatch
        and copy-address logic (Capstone/pefile toolchain). This is ordinary
        black-box disassembly of a shipped binary Microsoft provides no
        source for — not a licensing concern, unlike the earlier
        `msdelta-pa30-format` clean-room policy.
      - **Finding, implemented in `match.go`/`patch.go`:** no persistent
        source cursor exists — each match resolves `sourcePos = targetPos -
        distance` fresh, where `distance = delta` for SRC (slots 0-2, using
        the same signed field this package already decoded) and `distance =
        0` for FULLSRC (slot 3, which the bitstream already encodes with no
        extra parameters). The rift table that would otherwise perturb this
        was confirmed, via embedded pipeline-description strings in
        `msdelta.dll` itself (`AddRiftEntry(emptyTable, sourceSize, 0)`), to
        be an identity no-op for RAW/manifest content specifically — so in
        practice `distance` is numerically interchangeable with the `offset`
        DST/LRU matches (slots 4+) already use, and the implementation
        reuses that same back-reference/bounds-check/LRU-update machinery
        rather than adding a parallel code path.
      - **Two pieces the disassembling agent itself flagged as not fully
        confirmed**, implemented per its best reading but explicitly called
        out in code comments (`matchParams` doc comment in `match.go`) and
        `doc.go`'s new "SRC/FULLSRC" section: (1) slot 2's (18-bit delta)
        bias — disassembly showed an unconditional `+0xa000`, changed from
        this package's prior signed-conditional ±0xa000 (which was only ever
        an assumption extrapolated from slots 0/1's pattern, never verified
        either); (2) whether SRC/FULLSRC matches update the DST/LRU
        repeat-offset queue — disassembly traced both dispatch arms into the
        same LRU-update code DST matches use ("medium-high confidence, not
        exhaustively proven" per the agent), contradicting this package's
        earlier doc comment that only DST/LRU-repeat matches touch the
        queue; implemented as "yes, they do update it" per that finding.
      - **FULLSRC's `distance=0` flagged as suspicious turned out to be a
        real bug in the initial implementation, caught by measuring full
        real-corpus coverage (2026-07-13).** Ran the decoder (with the
        SRC/FULLSRC formula above) against *every* file in a real Windows
        11 23H2 image's `Windows\WinSxS\Manifests` — 17189 files, via
        `guestmount --ro` — and got only ~1% success, essentially all
        failures being `"invalid back-reference offset 0 (slot 3)"` at
        output offset 0, exactly the self-referential FULLSRC case flagged
        as suspicious when first implemented. Root cause: the
        disassembly's "target position" in `sourcePos = targetPos -
        distance` is measured target-content-only, not `len(out)`'s
        source-prefixed count — so the actual back-reference offset
        `copyMatch` needs is `sourceLen + distance`, not `distance` alone.
        Fixed in `patch.go`; **confirmed by rerunning the full 17189-file
        corpus: 100% now decode successfully**, each one cryptographically
        hash-verified via `DecodeWithSource`'s own internal `TargetHash`
        check (not merely self-consistent output) — a very strong
        empirical validation, going well beyond what a single hand-picked
        sample could show. Added `TestDecodeWithSourceRealFULLSRCSample` in
        `pa30_test.go` as a permanent regression fixture (the specific real
        file whose first symbol is FULLSRC at output position 0, which
        previously failed outright), embedded as
        `pa30/testdata/real_manifest_fullsrc_sample.manifest`.
      - **One piece still open:** slot 2's (18-bit delta) unconditional
        `+0xa000` bias remains unconfirmed either way — the 17189-file pass
        is strong evidence the overall approach is right, but can't prove
        this specific branch was ever exercised, since it can't distinguish
        "this file never took that path" from "this file took it and got
        the right answer by luck." No known sample is confirmed to need it.
      - Non-empty base rift table remains unsupported regardless (still an
        explicit error) — independent scope gap, not touched by this work.
      - **Two more real, unrelated bugs found by the same full-corpus
        coverage measurement, both fixed:**
        1. `mum.Dependency.Discoverable` (and `NestedPackage.Contained`,
           `DetectNone.Default`) used plain Go `bool` fields, which
           `encoding/xml` only accepts as literal `"true"`/`"false"` — but
           real `discoverable` attributes overwhelmingly use `"no"`/`"yes"`
           instead (7043 "no" + 4400 "yes" vs. only 368 "false", zero
           literal "true" observed), causing ~29% of files that decoded
           fine via `pa30` to still fail `mum.Parse`. Fixed by adding a
           `yesNoBool` type (`mum.go`) accepting both spellings on
           unmarshal, always emitting `"true"`/`"false"` on marshal.
        2. 193 of the 17189 real `.manifest` files (older, pre-CBS runtime
           component manifests, e.g. the VC++ 8.0/9.0 CRT's) turned out to
           be plain, uncompressed XML with no PA30 layer at all — not a
           decode failure, a genuinely different real file shape. Worse,
           these use the older, Microsoft-documented `asm.v1` namespace
           (not `asm.v3`), which `mum.Manifest`'s hardcoded-namespace
           `XMLName` tag rejected outright. Fixed two things: (a)
           `component.ParseManifest` (`build.go`) now only runs `pa30`
           decoding when the `"DCM"` prefix is present, parsing the rest as
           already-plain XML directly; (b) `mum.Manifest` gained a custom
           `UnmarshalXML` (delegating to a namespace-agnostic
           `manifestAlias` type) accepting `asm.v1`/`v2`/`v3` roots, while
           `Serialize` still always emits `asm.v3` (its `XMLName` tag stays
           pinned to that namespace, which only governs Marshal now).
        After all three fixes, `component.ParseManifest` (the full
        real-world entry point: `pa30` decode when needed, then
        `mum.Parse`) succeeds on **all 17189 files, 100%**, confirmed by
        rerunning the same full-corpus measurement end-to-end.
- [x] Research whether a PA30 *encoder* is actually needed at all: if
      component removal only ever deletes `.manifest` files wholesale rather
      than rewriting modified ones, no encoder is required and this can be
      dropped from scope entirely. Only implement one if a real use case
      needs to write a modified manifest back. Documented (2026-07-14): for
      the removal scope actually being implemented right now (see below), no
      encoder is needed — `component`'s new removal support only ever
      deletes whole `.manifest`/`.mum`/`.cat` files and their WinSxS payload
      directories; nothing rewrites or re-serializes a `.manifest` in place.
      **Left explicitly open, not closed, for one specific reason:** the
      user has stated a longer-term (low-priority, not currently being
      worked on — see the new "component installation" item below) goal of
      eventually *installing* components too, and installing necessarily
      means writing a brand-new `.manifest` file into `WinSxS\Manifests`,
      which is where a PA30 encoder would actually become necessary — real,
      Microsoft-produced files there are overwhelmingly PA30-compressed
      (see `pa30`'s README). Whether an encoder actually turns out to be
      required even then is still unconfirmed either way: this project's own
      100%-of-17189-files real-corpus survey (see the `pa30`/`mum` entries
      above) found 193 real `.manifest` files that are plain, uncompressed
      XML with no PA30 layer at all, proving the on-disk loader accepts
      uncompressed manifests for *some* real components — but every one of
      those 193 is a pre-existing legacy file from Microsoft's own build
      process (old VC++ runtime CRT manifests), not something added to a
      previously-PA30-only image by a third party after the fact, so this
      does not by itself confirm a *newly added* uncompressed manifest would
      be accepted the same way. Deferred until the component-installation
      goal is actually picked up — see that item's own note on what
      would need re-checking first.
- [x] Implement a manifest parser/serializer for plain-XML `.mum` files
      (`Windows\servicing\Packages\*.mum`): the documented base SxS
      `<assembly>`/`assemblyIdentity` schema, plus the empirically-inferred
      `asm.v3` CBS extensions above (`<package>`, `<update>`,
      `<parent>`, `<installerAssembly>`, `<selectable>`/`<detectNone>`,
      `<declareCapability>`/`<dependency>`, `<component>`). Done: new `mum`
      module (`mum.Parse`/`Manifest.Serialize`), verified against 4 real
      `.mum` files covering each modeled shape, copied verbatim from a real
      Windows 11 23H2 VM (`guestmount --ro` against
      `/var/lib/libvirt/images/win11.qcow2`, 2026-07-13) chosen after
      surveying all distinct top-level elements across all 2532 real `.mum`
      files in that VM's `Windows\servicing\Packages`. Deliberately does not
      model every element found in that survey (`<driver>`,
      `<satelliteInfo>`, `<MutualExclusionGroup>`, vendor extensions like
      `<mum2:customInformation>`, etc. — see `mum/README.md`) — scoped to
      what's needed for package identity/dependency-edge resolution, not a
      lossless round-trip of arbitrary `.mum` content; a test
      (`TestSerializeDropsUnmodeledExtensions`) documents this limitation
      explicitly rather than leaving it implicit. Does NOT read PA30-encoded
      WinSxS `.manifest` files — that remains blocked on the `pa30` module
      above; once available, `.manifest` files decode to the same XML shape
      and should parse with this same package.
- [x] Implement a component-store module that ties the above together:
      enumerate `WinSxS\Manifests\*.manifest` + `servicing\Packages\*.mum`,
      parse identities from each, resolve package→component dependency
      edges from manifest content, and expose a queryable view (by name
      pattern, by KB, by architecture) — this is the "actual Windows
      component module" that everything else feeds. Done: new `component`
      module (`ParseMUM`/`ParseManifest`/`Build`/`BuildFromImage`,
      `Store.ByName`/`Lookup`/`ByArchitecture`/`ByKB`/`ResolveDependencies`),
      depending on `mum`, `pa30`, and `wim` (for `MatchName` and
      image-tree enumeration via `BuildFromImage`). Per-file parse/decode
      failures are recorded on that `Entry`'s `Err` field rather than
      aborting a build, since most real `.manifest` files still can't be
      decoded (see `pa30`'s SRC/FULLSRC gap below) — a `Store` is
      necessarily built from whatever fraction of a real image's files
      current `pa30` support can decode. Dependency-edge resolution only
      follows `AssemblyIdentity` references (`Parent`, `Update.Package`/
      `Update.Component`, `Dependency.DependentAssembly`), not
      `DeclareCapability` capability tokens (a different identity
      namespace) — a known, documented scope limit, not an oversight.
      Required extending the sibling `mum` package first: component-level
      `.manifest` files turned out to use a different vocabulary
      (`<deployment/>`, `<dependency>`, `<dependentAssembly>`) than
      package-level `.mum` files' `<package>`/`<update>`, confirmed against
      19 real `.manifest` files decoded via `pa30` (2026-07-13) — see
      `mum/README.md`. Tested against real `.mum`/`.manifest` fixtures
      copied from the `mum`/`pa30` packages' own testdata.
- [x] Implement component/package removal as a best-effort fallback only:
      given a resolved identity or pattern, delete its WinSxS payload
      directory, `Manifests` entry, and `servicing\Packages` `.mum`/`.cat`
      files. Explicitly leave the `COMPONENTS` hive untouched/inconsistent,
      documented plainly as a permanent, known limitation (per the research
      verdict above) rather than attempting hive mutation. Done
      (2026-07-14): `component/remove.go`'s `Remove`. Verified two pairing
      assumptions against a real Windows 11 23H2 image before implementing,
      rather than assuming either: (1) every one of 1266 real `.mum` files
      in `servicing\Packages` has an exact same-base-name `.cat`, and vice
      versa, zero exceptions; (2) only 12975 of 17189 real `.manifest`
      files in `WinSxS\Manifests` have a corresponding `WinSxS\<name>`
      payload directory — the other 4216 (policy/metadata-only components)
      have none, so `Remove` treats a missing payload directory as a normal
      no-op, not an error, exactly like the "already gone" case. Given a
      `*component.Entry` (from `Store.ByName`/`Lookup`/etc), `Remove`
      deletes: for `KindPackage`, the `.mum` file plus its paired `.cat`;
      for `KindComponent`, the `.manifest` entry plus its (optional) WinSxS
      payload directory. Decrements blob refcounts first (mirroring
      `driver`'s `Uninstall` and `appx`'s `Remove`), via the same duplicated
      `decrementBlobRefs` helper pattern. A caller resolves "a pattern" to
      remove via the existing `Store.ByName` and calls `Remove` once per
      matched `Entry` — no separate bulk-removal function was added, since
      that composition is already trivial with what `Store` exposes.
- [x] Explicitly scope out a `/StartComponentCleanup /ResetBase`-equivalent
      permanent supersedence cleanup entirely — per the research above, its
      mechanism is undocumented `COMPONENTS`-hive-internal accounting
      requiring a live TrustedInstaller/CBS session, with no offline
      equivalent to implement. Done (2026-07-14): this is a documentation-only
      scoping decision, not something with code to write. Confirmed out of
      scope, permanently, for this project: (1) the research above already
      established there is no authoritative schema for the `COMPONENTS`
      hive's supersedence-tracking keys/values to safely mutate; (2) even if
      that schema were known, "permanent" component removal is defined by
      Microsoft's own documentation
      ([Clean up the WinSxS folder — Microsoft
      Learn](https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/clean-up-the-winsxs-folder))
      as an operation that runs through the live TrustedInstaller/CBS
      session (`ICbsSession`, see the research entry above) — there is no
      offline, file/registry-level equivalent to reimplement, only a live
      RPC-driven service call this project's whole premise (no real Windows
      host, no admin rights) excludes by construction. `component`/`appx`'s
      removal functions (`RemoveProvisioned`, and the new component removal
      below) delete files directly instead, which is exactly the
      "best-effort fallback" scope the research verdict recommended.
- [ ] **Future goal, low priority, not currently being worked on:**
      component *installation* (the reverse of the removal above) — given a
      new component's `.manifest`/`.mum`/`.cat`/payload files, add them to
      `WinSxS\Manifests`/`servicing\Packages`/the payload directory tree,
      mirroring `driver.Install`'s shape for driver packages. In scope for
      this project eventually (stated 2026-07-14), but explicitly not
      started and not blocking anything else. Before picking this up,
      re-check the PA30-encoder question above: specifically, whether a
      *newly added* uncompressed (plain-XML) `.manifest` file is actually
      accepted by real Windows the same way the 193 pre-existing legacy
      uncompressed manifests are (unconfirmed — see that entry) — if so, no
      PA30 encoder is needed even for install; if not, an encoder becomes a
      prerequisite for this item. Also unaddressed: whatever minimal,
      best-effort `COMPONENTS`-hive bookkeeping (if any) is actually
      necessary for a newly installed component to be recognized at all, vs.
      being silently ignored — the removal-side research above established
      the hive's schema is undocumented and its *supersedence* accounting is
      out of reach, but did not establish whether install-side runtime
      behavior tolerates a component with zero `COMPONENTS`-hive footprint;
      this needs its own "research first" pass before implementation, not an
      assumption either way.

## ISO image creation subsystem (new)

- [ ] Implement an ISO 9660 (+ Joliet and/or UDF bridge format, matching
      what `oscdimg -udfver102` produces) filesystem writer.
- [ ] Implement El Torito boot catalog support with two boot entries (BIOS
      boot sector + no-emulation UEFI boot image), matching `oscdimg`'s
      `-bootdata:2#p0,e,b<bios-boot>#pEF,e,b<efisys.bin>`. The boot-sector
      blobs themselves (`etfsboot.com`, `efisys.bin`) are reused verbatim
      from the source image, not generated.

## Top-level orchestration

- [ ] Build a top-level tool stringing all of the above together to
      reproduce nano11builder's workflow end-to-end (pick an
      install.wim/esd image index, strip AppX/CBS packages and files by
      rule, edit the standard registry hives, rebuild `boot.wim`, export to
      ESD, and author the final bootable ISO), purely on the WIM/registry
      formats in memory — no real DISM/WOF mount, no Administrator/
      Windows-host requirement.
