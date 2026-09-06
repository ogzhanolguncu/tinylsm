package manifest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Payload-only codec: no crc/len frame here. Framing is the writer's job.

func TestCodecRoundTrip(t *testing.T) {
	cases := map[string]VersionEdit{
		"empty":         {},
		"counters only": {NextFileNum: 3, LastSeq: 0},
		"one flush":     {AddFiles: []FileMeta{{0, 6}}, NextFileNum: 7, LastSeq: 10000},
		"compaction": {
			DelFiles:    []FileMeta{{0, 3}, {0, 5}},
			AddFiles:    []FileMeta{{1, 7}},
			NextFileNum: 8,
		},
		"big numbers": {
			AddFiles:    []FileMeta{{6, 1 << 62}},
			NextFileNum: 1<<64 - 1,
			LastSeq:     1<<56 - 1,
		},
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := decode(encode(in))
			require.NoError(t, err)
			// nil and empty slices must compare equal: normalise both sides.
			require.Equal(t, norm(in), norm(out))
		})
	}
}

func norm(e VersionEdit) VersionEdit {
	if len(e.AddFiles) == 0 {
		e.AddFiles = nil
	}
	if len(e.DelFiles) == 0 {
		e.DelFiles = nil
	}
	return e
}

func TestCodecPreservesFileOrder(t *testing.T) {
	in := VersionEdit{AddFiles: []FileMeta{{0, 9}, {0, 4}, {0, 6}}}
	out, err := decode(encode(in))
	require.NoError(t, err)
	require.Equal(t, in.AddFiles, out.AddFiles, "decode must not reorder files")
}

func TestDecodeRejectsUnknownTag(t *testing.T) {
	_, err := decode([]byte{0xFF})
	require.ErrorIs(t, err, ErrMalformed)
}

// A cut that lands mid-varint must be an error, not a wrong number.
func TestDecodeRejectsCutInsideField(t *testing.T) {
	full := encode(VersionEdit{NextFileNum: 300}) // 300 needs a 2-byte uvarint
	require.Len(t, full, 3, "tag + 2-byte uvarint")
	_, err := decode(full[:2])
	require.ErrorIs(t, err, ErrMalformed)
}

func FuzzDecodeNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add(encode(VersionEdit{AddFiles: []FileMeta{{0, 6}}, NextFileNum: 7, LastSeq: 10000}))
	f.Add([]byte{0xFF, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		decode(data) // error is fine; panic fails the run
	})
}

func FuzzCodecRoundTrip(f *testing.F) {
	f.Add(uint32(0), uint64(6), uint64(7), uint64(10000))
	f.Add(uint32(6), uint64(0), uint64(0), uint64(0))
	f.Fuzz(func(t *testing.T, level uint32, fileNum, next, seq uint64) {
		in := VersionEdit{
			AddFiles:    []FileMeta{{level % NumLevels, fileNum}},
			DelFiles:    []FileMeta{{level % NumLevels, fileNum + 1}},
			NextFileNum: next,
			LastSeq:     seq,
		}
		out, err := decode(encode(in))
		require.NoError(t, err)
		require.Equal(t, in, out)
	})
}
