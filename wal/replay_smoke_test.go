package wal

import (
	"path/filepath"
	"testing"
)

// Smoke test: write a few records, replay, dump whatever comes back.
// No assertions on purpose — this is for eyeballing return values.
func TestReplaySmoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "000000001.wal")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	puts := []Entry{
		{key: []byte("cat"), value: []byte("purr"), seq: 1, kind: KindPut},
		{key: []byte("dog"), value: []byte("woof"), seq: 2, kind: KindPut},
		{key: []byte("cat"), value: nil, seq: 3, kind: KindDelete},
	}
	for _, e := range puts {
		if err := w.Append(e); err != nil {
			t.Fatalf("Append(%s): %v", e.key, err)
		}
	}

	entries, err := Replay(path)
	t.Logf("Replay returned err = %v", err)
	t.Logf("Replay returned %d entries", len(entries))
	for i, e := range entries {
		t.Logf("  [%d] seq=%d kind=%s key=%q value=%q", i, e.seq, e.kind, e.key, e.value)
	}
}
