package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		val  []byte
		seq  uint64
		kind Kind
	}{
		{"basic", []byte("cat"), []byte("purr"), 7, KindPut},
		{"empty value (tombstone)", []byte("cat"), nil, 42, KindDelete},
		{"empty key", nil, []byte("orphan"), 1, KindPut},
		{"both empty (min record)", nil, nil, 0, KindPut},
		{"long key forces 2-byte varint", bytes.Repeat([]byte("k"), 200), []byte("v"), 9, KindPut},
		{"long value forces 2-byte varint", []byte("k"), bytes.Repeat([]byte("v"), 300), 9, KindPut},
		{"binary data with zero bytes", []byte{0x00, 0xFF, 0x00}, []byte{0x00, 0x00}, 1 << 60, KindPut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := mustEncode(t, tc.key, tc.val, tc.seq, tc.kind)
			e, err := decode(rec)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !bytes.Equal(e.key, tc.key) {
				t.Errorf("key = %q, want %q", e.key, tc.key)
			}
			if !bytes.Equal(e.value, tc.val) {
				t.Errorf("value = %q, want %q", e.value, tc.val)
			}
			if e.seq != tc.seq {
				t.Errorf("seq = %d, want %d", e.seq, tc.seq)
			}
			if e.kind != tc.kind {
				t.Errorf("kind = %#x, want %#x", e.kind, tc.kind)
			}
		})
	}
}

// Every prefix of a valid record is a torn write. Decode must return an
// error for all of them and panic for none.
func TestTruncatedPrefixes(t *testing.T) {
	rec := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	for i := 0; i < len(rec); i++ {
		if _, err := decode(rec[:i]); err == nil {
			t.Errorf("decode(rec[:%d]): want error, got nil", i)
		}
	}
}

// Trailing bytes after a record are the next record, not corruption.
func TestTrailingBytesIgnored(t *testing.T) {
	rec := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	buf := append(append([]byte{}, rec...), []byte("garbage that is really the next record")...)
	e, err := decode(buf)
	if err != nil {
		t.Fatalf("decode with trailing bytes: %v", err)
	}
	if !bytes.Equal(e.key, []byte("cat")) || !bytes.Equal(e.value, []byte("purr")) {
		t.Errorf("entry = %q/%q, want cat/purr", e.key, e.value)
	}
}

// Flip each byte in turn: every flip must be rejected. Flips inside the
// payload must be caught specifically by the checksum.
func TestSingleBitCorruption(t *testing.T) {
	orig := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	for i := 0; i < len(orig); i++ {
		rec := append([]byte{}, orig...)
		rec[i] ^= 0xFF
		_, err := decode(rec)
		if err == nil {
			t.Errorf("byte %d flipped: want error, got nil", i)
			continue
		}
		if i >= headerSize && !errors.Is(err, ErrChecksum) {
			t.Errorf("payload byte %d flipped: want ErrChecksum, got %v", i, err)
		}
	}
}

// length field lies small: buffer is intact, claim describes a payload too
// short to hold the fixed prefix. CRC is recomputed over the lying span so
// the structural guard — not the checksum — must reject it.
func TestLengthLiesSmall(t *testing.T) {
	rec := mustEncode(t, []byte("cat"), []byte("purr"), 7, KindPut)
	binary.LittleEndian.PutUint32(rec[offLen:offSeq], 5)
	binary.LittleEndian.PutUint32(rec[offCRC:offLen], crc32.Checksum(rec[headerSize:headerSize+5], Castagnoli))
	if _, err := decode(rec); !errors.Is(err, ErrMalformed) {
		t.Errorf("want ErrMalformed, got %v", err)
	}
}

// keyLen claims more bytes than the payload holds, checksum valid.
func TestKeyLenLies(t *testing.T) {
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = append(p, byte(KindPut))
	p = binary.AppendUvarint(p, 200) // keyLen claims 200...
	p = append(p, 'x')               // ...one byte follows
	rec := frame(p)
	if _, err := decode(rec); !errors.Is(err, ErrMalformed) {
		t.Errorf("want ErrMalformed, got %v", err)
	}
}

// Payload ends in the middle of a varint (continuation bit set, no
// terminator). Uvarint reports n == 0; decode must not loop or accept.
func TestIncompleteVarint(t *testing.T) {
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = append(p, byte(KindPut))
	p = append(p, 0x80, 0x80) // varint never terminates
	rec := frame(p)
	if _, err := decode(rec); !errors.Is(err, ErrMalformed) {
		t.Errorf("want ErrMalformed, got %v", err)
	}
}

// Decode is strict: a kind no encoder produces is corruption, and replaying
// an operation we don't understand is worse than stopping. Format evolution
// belongs in a version field, not in tolerated mystery bytes.
func TestUnknownKindRejected(t *testing.T) {
	p := binary.LittleEndian.AppendUint64(nil, 7)
	p = append(p, 0xFF) // kind no encoder would produce
	p = binary.AppendUvarint(p, 1)
	p = append(p, 'k')
	p = binary.AppendUvarint(p, 1)
	p = append(p, 'v')
	if _, err := decode(frame(p)); !errors.Is(err, ErrMalformed) {
		t.Errorf("want ErrMalformed, got %v", err)
	}
}

// Tombstones must not carry a value; encode is the write-side gate.
func TestEncodeRejectsTombstoneWithValue(t *testing.T) {
	if _, err := encode([]byte("cat"), []byte("oops"), 7, KindDelete); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("want ErrInvalidInput, got %v", err)
	}
}

func mustEncode(t *testing.T, key, val []byte, seq uint64, kind Kind) []byte {
	t.Helper()
	rec, err := encode(key, val, seq, kind)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return rec
}

// frame wraps a hand-built payload with a valid crc+len header, so tests
// can craft structurally-broken payloads that pass the checksum.
func frame(p []byte) []byte {
	rec := binary.LittleEndian.AppendUint32(nil, crc32.Checksum(p, Castagnoli))
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(p)))
	return append(rec, p...)
}

// Decode must never panic, whatever bytes arrive.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	if rec, err := encode([]byte("cat"), []byte("purr"), 7, KindPut); err == nil {
		f.Add(rec)
	}
	f.Add(frame([]byte{0x80, 0x80, 0x80}))
	f.Fuzz(func(t *testing.T, data []byte) {
		decode(data) // any error is fine; a panic fails the fuzz run
	})
}

// Whatever goes in must come out.
func FuzzRoundtrip(f *testing.F) {
	f.Add([]byte("cat"), []byte("purr"), uint64(7), byte(1))
	f.Add([]byte{}, []byte{}, uint64(0), byte(0))
	f.Fuzz(func(t *testing.T, key, val []byte, seq uint64, kindByte byte) {
		kind := Kind(kindByte % 2) // only legal kinds reach encode
		rec, err := encode(key, val, seq, kind)
		if kind == KindDelete && len(val) > 0 {
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("tombstone with value: want ErrInvalidInput, got %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		e, err := decode(rec)
		if err != nil {
			t.Fatalf("decode of freshly encoded record: %v", err)
		}
		if !bytes.Equal(e.key, key) || !bytes.Equal(e.value, val) || e.seq != seq || e.kind != kind {
			t.Fatalf("roundtrip mismatch: got %q/%q/%d/%v", e.key, e.value, e.seq, e.kind)
		}
	})
}
