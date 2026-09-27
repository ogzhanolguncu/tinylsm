package wal

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/ogzhanolguncu/tinylsm/pkg/frame"
)

type Writer struct {
	f    *os.File
	path string
	err  error
}

func NewWriter(path string) (*Writer, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	f, err := openWALFile(dir, name, os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, fmt.Errorf("init wal file: %w", err)
	}
	return &Writer{
		f:    f,
		path: path,
	}, nil
}

func FileName(num uint64) string {
	return fmt.Sprintf("%09d.wal", num)
}

func OpenWriter(path string) (*Writer, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	f, err := openWALFile(dir, name, 0)
	if err != nil {
		return nil, fmt.Errorf("open wal file: %w", err)
	}
	return &Writer{
		f:    f,
		path: path,
	}, nil
}

func (w *Writer) Append(e Entry) error {
	if w.err != nil {
		return fmt.Errorf("writer is broken: %w", w.err)
	}
	fail := func(e error) error { w.err = e; return e }
	enc, err := encode(e)
	if err != nil {
		return fmt.Errorf("encode entry: %w", err)
	}
	n, err := w.f.Write(enc)
	if err != nil {
		return fail(fmt.Errorf("write wal after %d bytes: %w", n, err))
	}
	if n < len(enc) {
		return fail(io.ErrShortWrite)
	}
	if err := w.f.Sync(); err != nil {
		return fail(fmt.Errorf("fsync wal (not durable): %w", err))
	}
	return nil
}

// openWALFile opens dir/name for appending. flag decides create-new
// (O_CREATE|O_EXCL, fails if the file exists) from open-existing (fails if it
// does not). Creating adds a directory entry, so that case fsyncs the parent
// dir too — otherwise a crash can lose the file's name while keeping its bytes.
func openWALFile(dir, name string, flag int) (file *os.File, err error) {
	perms := os.O_WRONLY | os.O_APPEND | flag

	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open wal dir: %w", err)
	}
	defer func() {
		_ = d.Close()
	}()
	f, err := os.OpenFile(filepath.Join(dir, name), perms, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("wal file %s already exists: %w", name, err)
		}
		return nil, fmt.Errorf("open wal file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	if flag&os.O_CREATE != 0 {
		if err = d.Sync(); err != nil {
			return nil, fmt.Errorf("sync wal dir: %w", err)
		}
	}
	return f, nil
}

func Replay(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("replay wal: %w", err)
	}

	var entries []Entry
	off := 0
	fileEnd := len(data)

	// A crash can only ever break the FINAL record: the writer is append-only,
	// fsyncs every record, and never writes again after a failure.
	dropBrokenTail := func() ([]Entry, error) {
		if err := truncateAndFsync(path, off); err != nil {
			return nil, err
		}
		log.Printf("wal: dropped broken tail of %s at offset %d", path, off)
		return entries, nil
	}

	for off < fileEnd {
		size, ok := frame.FrameSize(data[off:])
		if !ok {
			return dropBrokenTail() // not even a full header left
		}

		frameEnd := off + size
		if frameEnd > fileEnd {
			return dropBrokenTail() // record cut short by crash
		}

		e, n, err := decode(data[off:])
		if err != nil {
			brokenRecordIsLast := frameEnd == fileEnd
			if brokenRecordIsLast {
				return dropBrokenTail() // crash broke the final record
			}
			// broken record mid file
			return nil, fmt.Errorf("replay: corrupt record at offset %d: %w", off, err)
		}

		entries = append(entries, e)
		off += n
	}
	return entries, nil // case 1: clean EOF, every record accounted for
}

func truncateAndFsync(path string, off int) error {
	if err := os.Truncate(path, int64(off)); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reopen for fsync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync after truncate: %w", err)
	}
	return nil
}

func (w *Writer) Path() string {
	return w.path
}

func (w *Writer) Close() error {
	var errs []error
	if err := w.f.Sync(); err != nil {
		errs = append(errs, fmt.Errorf("sync wal file %s: %w", w.path, err))
	}
	if err := w.f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close wal file %s: %w", w.path, err))
	}
	err := errors.Join(errs...)
	if err != nil {
		w.err = err
	}
	return err
}
