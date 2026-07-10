# regf

A Go implementation of the on-disk structure of Windows Registry hive files
(the "regf" format): SYSTEM, SOFTWARE, SAM, DEFAULT, NTUSER.DAT and similar
files, e.g. as found at `\Windows\System32\config\*` inside a Windows image,
or a loaded user hive.

Modeled on Joachim Metz's "Windows NT Registry File (REGF) format
specification" from the [libregf](https://github.com/libyal/libregf) project
(`documentation/Windows NT Registry File (REGF) format.asciidoc`), as fetched
at commit `fa7ed12674b308db80a003733e748fbcba4b6e4c` (2026-06-25), document
revision 0.0.31.

## Scope

This package handles the format version 1.2-and-later on-disk layout
(`CM_KEY_NODE` / `CM_KEY_VALUE` shapes), which is what every hive produced by
Windows NT 3.51 and later -- including all currently supported Windows
versions -- uses:

- the base block (`BaseBlock`): the 4096-byte file header, including the
  primary/secondary sequence numbers, version, root cell offset, hive bins
  data size, and the XOR-32 checksum
- hive bin framing (`HBin`): the 32-byte `"hbin"` header preceding a run of
  cells
- generic cell framing (allocated vs. free, 8-byte-aligned size)
- named-key cells (`Key`, backed by `nk` cells): flags, timestamps, class
  name, security, values, and subkeys
- value cells (`Value`, backed by `vk` cells): name, REG_* data type, and
  data -- including the "data in offset" inline convention for data no
  larger than 4 bytes, out-of-line data cells, and `"db"` big-data
  reassembly for data larger than 16344 bytes
- all four subkey-list cell shapes (`lf`/`lh`/`li`/`ri`) well enough to
  enumerate a key's subkeys, including the documented LH hash algorithm
- security cells (`sk`): reference count and previous/next links, with the
  descriptor itself preserved as an opaque byte blob

`Parse` builds a plain in-memory `Key` tree (mirroring `wim.DirEntry`'s
shape: a struct with a `Subkeys []*Key` field, built and walked directly, no
special "add node" API) from a raw hive byte slice, and `Hive.AppendTo`
serializes such a tree back to valid regf bytes.

It deliberately does **not** implement:

- The version 1.1 on-disk layout (Windows NT 3.1/3.5 only, whose nk/vk/sk
  cells carry an extra leading 4-byte field before their signature).
  `Parse` rejects it explicitly.
- Parsing the internal structure of a security descriptor's ACL/ACE bytes.
  `Key.Security` is an opaque, round-trippable byte blob -- exactly like the
  sibling `cat` package's stance on X.509 certificates.
- Transaction log replay (`.LOG`/`.LOG1`/`.LOG2` files and the dirty-page /
  sequence-number recovery mechanism). This package reads and writes a clean
  primary hive file only. The sequence-number and checksum fields are still
  modeled so a caller can inspect or set them.
- Byte-for-byte reproduction of an arbitrary real-world hive's bin/cell
  allocation layout. `Hive.AppendTo` always builds a single hive bin, simply
  packed by a deterministic post-order walk of the `Key` tree (see its doc
  comment in `hive.go`) -- it does not reverse-engineer Windows' own
  allocator. A hive this package produces itself round-trips byte-for-byte
  through `Parse` + `AppendTo`.
- Any higher-level "populate the registry with a driver's entries" semantics
  (`DriverDatabase`, `CriticalDeviceDatabase`, `INFCACHE.1`, etc.). That
  orchestration is future work for the sibling `driver` package, not this
  one.
- Any live registry API semantics (opening `HKLM`, transactions,
  notifications). This is purely an offline on-disk file-format package,
  like `wim`/`inf`/`cat`/`pe`.

## Layout

Everything lives in a single package, `regf`, one file per format concern:

| File | Responsibility |
|------|-----------------|
| `regf.go` | package doc, byte order, `wrapErr`, `NoCellOffset`, `HBinDataStart` |
| `baseblock.go` | `BaseBlock` (the 4096-byte file header) + checksum |
| `hbin.go` | `HBin` (the 32-byte hive bin header) |
| `cell.go` | generic cell framing (`readCell`) and the `cellArena` build helper |
| `nk.go` | `Key` (the public tree type) + the raw `nk` cell shape |
| `vk.go` | `Value` (the public value type) + the raw `vk` cell shape and the inline-data convention |
| `subkeylist.go` | `lf`/`lh`/`li`/`ri` subkey-list cells + the LH hash algorithm |
| `sk.go` | the opaque `sk` (security) cell |
| `bigdata.go` | `db` big-data cell + segment-list reassembly |
| `hive.go` | `Hive`, `Parse`, `Hive.AppendTo`, and the tree-walking glue between all of the above |
| `encoding.go` | UTF-16LE helpers |
| `regf_test.go` | round-trip tests |

## Usage

```go
data, _ := os.ReadFile("SYSTEM")

hive, err := regf.Parse(data)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("version 1.%d\n", hive.BaseBlock.MinorVersion)

var walk func(k *regf.Key, path string)
walk = func(k *regf.Key, path string) {
    for _, v := range k.Values {
        fmt.Printf("%s\\%s = %v\n", path, v.NameUTF8(), v.Data)
    }
    for _, sub := range k.Subkeys {
        walk(sub, path+`\`+sub.NameUTF8())
    }
}
walk(hive.Root, "")

// Build a fresh hive from scratch and serialize it.
fresh := &regf.Hive{
    BaseBlock: regf.BaseBlock{MajorVersion: 1, MinorVersion: regf.Version1_5},
    Root: &regf.Key{
        Flags: regf.KeyFlagHiveEntry,
        Values: []regf.Value{
            {Name: nil, Type: regf.RegSZ, Data: someUTF16LEBytes},
        },
    },
}
out, err := fresh.AppendTo(nil)
```

## Tests

```
go test ./...
```

The tests hand-construct a minimal but structurally valid hive (base block +
one hive bin + a root `nk` with a REG_DWORD value using the inline "data in
offset" convention, a REG_SZ value in an out-of-line data cell, and one
subkey reachable through an `lh` subkey list) and assert:

- `Parse` succeeds and the resulting `Key` tree has the expected shape
- `Parse` then `AppendTo` reproduces the fixture's bytes exactly
- building an equivalent `Hive` purely via Go struct literals (including a
  value large enough to require `db` big-data reassembly) and calling
  `AppendTo` produces bytes `Parse` reads back correctly, and that
  themselves round-trip through a second `AppendTo`

plus focused tests for `BaseBlock`'s checksum/round trip and the LH hash
algorithm.

## License

MIT OR Apache-2.0.
