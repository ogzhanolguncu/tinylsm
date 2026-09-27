package manifest

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentRoundTrip(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, WriteCurrent(dir, "MANIFEST-000001"))
	require.NoError(t, WriteCurrent(dir, "MANIFEST-000002"))

	got, err := ReadCurrent(dir)
	require.NoError(t, err)
	require.Equal(t, "MANIFEST-000002", got)
}

func TestReadCurrentMissing(t *testing.T) {
	_, err := ReadCurrent(t.TempDir())
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestReadCurrentRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"MANIFEST-000001", // no trailing newline
		"garbage\n",       // wrong prefix
		"MANIFEST-../x\n", // escapes dir
	} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "CURRENT"), []byte(in), 0o644))
		_, err := ReadCurrent(dir)
		require.ErrorIsf(t, err, ErrBadCurrent, "input %q", in)
	}
}
