package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/stretchr/testify/require"
)

// isCorruption reports whether err is one of the three sentinels a reader is
// allowed to fail with. Any other error is unclassifiable by a caller: the read
// path must be able to tell "this file is broken" from "this key is absent".
func isCorruption(err error) bool {
	return errors.Is(err, ErrBlockChecksum) ||
		errors.Is(err, ErrBlockCorrupt) ||
		errors.Is(err, ErrBadMagic)
}

// probe runs fn and turns a panic into an error, so one bad offset does not
// hide the other thousands.
func probe(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return fn()
}

// lookup is one Get and the answer the golden table gives for it.
type lookup struct {
	userKey string
	target  []byte
	state   LookupState
	val     []byte
}

// readAll runs the three reader entry points against the (corrupted) table at
// path and returns one string per violation. Each entry point takes a
// different path through the file, so all three run against every flip:
// openTable touches only the footer and the index, Get adds one data block,
// a full scan adds all of them.
func readAll(path string, lookups []lookup, wantKeys [][]byte) []string {
	var out []string

	var tbl *Table
	if err := probe(func() (err error) { tbl, err = openTable(path); return }); err != nil {
		if !isCorruption(err) {
			out = append(out, "openTable: "+err.Error())
		}
		return out
	}
	defer func() { _ = tbl.Close() }()

	// A read may fail, but it may not lie. "absent" is not an allowed answer
	// for a key that was written: the read path would move to an older table
	// and serve stale data.
	for _, l := range lookups {
		var val []byte
		var state LookupState
		err := probe(func() (err error) { val, state, err = tbl.Get(l.target); return })
		switch {
		case err != nil && !isCorruption(err):
			out = append(out, fmt.Sprintf("Get(%s): %v", l.userKey, err))
		case err != nil:
		case state != l.state || (state == Found && !bytes.Equal(val, l.val)):
			out = append(out, fmt.Sprintf("Get(%s): answered state %d value %x, want state %d value %x",
				l.userKey, state, prefix(val), l.state, prefix(l.val)))
		}
	}

	var got [][]byte
	var scanErr error
	if err := probe(func() error {
		it := tbl.NewIterator()
		for it.SeekToFirst(); it.Valid(); it.Next() {
			got = append(got, bytes.Clone(it.Key()))
		}
		scanErr = it.Error()
		return nil
	}); err != nil {
		out = append(out, "scan: "+err.Error())
		return out
	}

	if scanErr != nil && !isCorruption(scanErr) {
		out = append(out, "scan: "+scanErr.Error())
		return out
	}
	// A scan that stops early must have yielded a correct prefix; one that
	// finishes clean must have yielded the whole table.
	want := wantKeys
	if scanErr != nil {
		if len(got) > len(want) {
			out = append(out, fmt.Sprintf("scan: yielded %d keys before failing, table holds %d", len(got), len(want)))
			return out
		}
		want = want[:len(got)]
	}
	if len(got) != len(want) {
		out = append(out, fmt.Sprintf("scan: yielded %d keys, want %d, err %v", len(got), len(want), scanErr))
		return out
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			out = append(out, fmt.Sprintf("scan: key %d is %x, want %x", i, got[i], want[i]))
			break
		}
	}
	return out
}

func prefix(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// Every byte of a real .sst, flipped in turn. For each flip the reader may
// succeed — a flip can land on a byte no read path looks at — but if it fails
// it must fail with one of the three sentinels, and it must never panic, hang
// or answer wrong.
//
// Contracts are ON in this build deliberately. A contract.Require that fires
// on a flipped byte is a misplaced contract, not a detection: disk corruption
// is an operating error, not a caller mistake, so it belongs in an error
// return. A panic here is a finding.
//
// A hang is caught by go test -timeout.
func TestTableSurvivesEveryByteFlip(t *testing.T) {
	// 30 records with 512-byte values span three data blocks, so the sweep
	// covers a first, middle and last block plus the index and the footer.
	// Kept small on purpose: the whole file is rewritten once per offset.
	recs := make([]rec, 30)
	for i := range recs {
		kind, val := keys.KindPut, bytes.Repeat([]byte{byte(i)}, 512)
		if i%5 == 3 {
			kind, val = keys.KindDelete, nil
		}
		recs[i] = rec{fmt.Sprintf("key%04d", i), uint64(i + 1), kind, val}
	}

	dir := t.TempDir()
	goldenPath := filepath.Join(dir, FileName(1))
	buildTable(t, goldenPath, recs)

	golden, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	idx, _, _ := parseTable(t, goldenPath)
	require.GreaterOrEqual(t, len(idx), 3, "sweep needs a table spanning several data blocks")

	lookups := make([]lookup, len(recs))
	wantKeys := make([][]byte, len(recs))
	for i, r := range recs {
		state := Found
		if r.kind == keys.KindDelete {
			state = Deleted
		}
		lookups[i] = lookup{
			userKey: r.userKey,
			target:  ik(t, r.userKey, snapshot, keys.KindPut),
			state:   state,
			val:     r.val,
		}
		wantKeys[i] = ik(t, r.userKey, r.seq, r.kind)
	}

	// Sanity: the sweep is only meaningful if the golden file passes it.
	require.Empty(t, readAll(goldenPath, lookups, wantKeys), "golden table must read clean")

	path := filepath.Join(dir, FileName(2))
	var findings []string
	for off := range golden {
		bad := bytes.Clone(golden)
		bad[off] ^= 0xff
		require.NoError(t, os.WriteFile(path, bad, 0o644))

		for _, f := range readAll(path, lookups, wantKeys) {
			findings = append(findings, fmt.Sprintf("offset %d/%d: %s", off, len(golden), f))
		}
	}

	if len(findings) > 0 {
		const show = 20
		listed := findings
		if len(listed) > show {
			listed = listed[:show]
		}
		t.Errorf("%d findings across %d offsets, first %d:\n%s",
			len(findings), len(golden), len(listed), bytes.Join(toBytes(listed), []byte("\n")))
	}
}

func toBytes(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

// The flip sweep cannot reach blockHandle's length guard: the index block is
// checksummed, so a flipped byte fails the CRC long before any handle is
// parsed. Reaching it takes a hand-built index block whose CRC is VALID and
// whose structure is legal, but where one entry's value is not 16 bytes. Such
// a table opens cleanly and only fails when that entry is followed.
//
// Both directions matter, and neither one panics without the guard.
// blockIter.Value is a zero-copy slice into the block, so a 15-byte value has
// capacity running to the end of the block: v[8:16] is legal Go and silently
// reads the NEXT entry's bytes as the block size. An over-long value decodes
// its first 16 bytes and looks fine. The guard is what makes a handle that is
// not exactly 16 bytes an error instead of an answer from bytes nobody
// validated.
func TestIndexEntryWithMalformedHandleIsRejectedOnUse(t *testing.T) {
	for _, handleLen := range []int{blockHandleSize - 1, blockHandleSize + 1} {
		t.Run(fmt.Sprintf("handle of %d bytes", handleLen), func(t *testing.T) {
			dir := t.TempDir()
			goldenPath := filepath.Join(dir, FileName(1))
			writeTable(t, goldenPath, 30)

			idx, raw, indexOff := parseTable(t, goldenPath)
			require.GreaterOrEqual(t, len(idx), 3, "need a damaged block that is not the first")

			const damaged = 1
			bb := newBlockBuilder()
			for i, e := range idx {
				v := make([]byte, blockHandleSize)
				binary.LittleEndian.PutUint64(v[0:8], e.off)
				binary.LittleEndian.PutUint64(v[8:16], e.size)
				if i == damaged {
					v = append(v, 0)[:handleLen]
				}
				bb.Add(e.lastKey, v)
			}
			index := bb.Finish()

			// The footer must still say the index ends exactly where the footer
			// begins, or openTable rejects the file before parsing any handle.
			footer := make([]byte, footerSize)
			binary.LittleEndian.PutUint64(footer[0:8], indexOff)
			binary.LittleEndian.PutUint64(footer[8:16], uint64(len(index)))
			binary.LittleEndian.PutUint64(footer[16:24], tableMagic)

			bad := append(append(bytes.Clone(raw[:indexOff]), index...), footer...)
			path := filepath.Join(dir, FileName(2))
			require.NoError(t, os.WriteFile(path, bad, 0o644))

			tbl, err := openTable(path)
			require.NoError(t, err, "the index block itself is well formed")
			t.Cleanup(func() { require.NoError(t, tbl.Close()) })

			// key0010 sits in the damaged block: idx[0] ends at key0007, idx[1] at key0015.
			_, state, err := tbl.Get(ik(t, "key0010", snapshot, keys.KindPut))
			require.ErrorIs(t, err, ErrBlockCorrupt)
			require.Equal(t, NotFound, state)

			it := tbl.NewIterator()
			it.SeekToFirst()
			got := scan(t, it)
			require.ErrorIs(t, it.Error(), ErrBlockCorrupt, "scan must fail, not report end of table")
			require.NotEmpty(t, got, "the first block is intact and must still be yielded")
			require.Less(t, len(got), 30, "the scan must stop at the damaged entry")
		})
	}
}
