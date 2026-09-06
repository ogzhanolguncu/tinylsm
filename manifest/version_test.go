package manifest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The three records from the flush walkthrough: counter-only, then two flushes.
func TestApplyFoldsEdits(t *testing.T) {
	edits := []VersionEdit{
		{NextFileNum: 3, LastSeq: 0},
		{AddFiles: []FileMeta{{Level: 0, FileNum: 4}}, NextFileNum: 5, LastSeq: 5000},
		{AddFiles: []FileMeta{{Level: 0, FileNum: 6}}, NextFileNum: 7, LastSeq: 10000},
	}

	v := &Version{}
	for _, e := range edits {
		v = v.Apply(e)
	}

	require.Equal(t, []FileMeta{{Level: 0, FileNum: 4}, {Level: 0, FileNum: 6}}, v.Files[0])
	require.Equal(t, uint64(7), v.NextFileNum)
	require.Equal(t, uint64(10000), v.LastSeq)
}

func TestApplyDeletesThenAdds(t *testing.T) {
	v := &Version{}
	v = v.Apply(VersionEdit{AddFiles: []FileMeta{{0, 3}, {0, 5}}, NextFileNum: 6})
	old := v
	v = v.Apply(VersionEdit{
		DelFiles:    []FileMeta{{0, 3}, {0, 5}},
		AddFiles:    []FileMeta{{1, 7}},
		NextFileNum: 8,
	})

	require.Empty(t, v.Files[0])
	require.Equal(t, []FileMeta{{1, 7}}, v.Files[1])
	require.Equal(t, []FileMeta{{0, 3}, {0, 5}}, old.Files[0], "Apply mutated the old version")
}

func TestApplyZeroCounterMeansUnchanged(t *testing.T) {
	v := (&Version{}).Apply(VersionEdit{NextFileNum: 9, LastSeq: 42})
	v = v.Apply(VersionEdit{AddFiles: []FileMeta{{0, 8}}})

	require.Equal(t, uint64(9), v.NextFileNum)
	require.Equal(t, uint64(42), v.LastSeq)
}
