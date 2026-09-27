package manifest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ogzhanolguncu/tinylsm/pkg/frame"
)

// Writer appends VersionEdit records to a MANIFEST file.
// Each record is framed with CRC32-C + length (same format as WAL).
//
// TODO: frame/validateFrame live in wal/codec.go but are unexported.
// Decision needed: export them, move to pkg/frame, or copy here.
type Writer struct {
	f *os.File
}

func Create(path string) (*Writer, error) {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open manifest dir: %w", err)
	}
	defer func() {
		_ = d.Close()
	}()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create manifest file: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sync manifest dir: %w", err)
	}
	return &Writer{
		f: f,
	}, nil
}

// Append encodes a VersionEdit, wraps it in a CRC+len frame,
// writes it to the file, and fsyncs.
func (w *Writer) Append(e VersionEdit) error {
	enc := encode(e)
	fm := frame.Frame(enc)
	n, err := w.f.Write(fm)
	if err != nil {
		return fmt.Errorf("write manifest after %d bytes: %w", n, err)
	}
	if n < len(fm) {
		return io.ErrShortWrite
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync manifest file: %w", err)
	}
	return nil
}

func (w *Writer) Close() error {
	return w.f.Close()
}
