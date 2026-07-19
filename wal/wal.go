package wal

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

type Writer struct {
	f    *os.File
	path string
	err  error
}

func NewWriter(path string) (*Writer, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	f, err := createWALFile(dir, name)
	if err != nil {
		return nil, fmt.Errorf("init wal file: %w", err)
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

func createWALFile(dir, name string) (file *os.File, err error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open wal dir: %w", err)
	}
	defer func() {
		_ = d.Close()
	}()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_APPEND|os.O_EXCL|os.O_CREATE, 0o644)
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
	if err = d.Sync(); err != nil {
		return nil, fmt.Errorf("sync wal dir: %w", err)
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

	torn := func() ([]Entry, error) {
		if err := truncateAndFsync(path, off); err != nil {
			return nil, err
		}
		log.Printf("wal: torn tail in %s at offset %d, truncated", path, off)
		return entries, nil
	}

	for off < len(data) {
		rem := len(data) - off

		if rem < headerSize {
			return torn() // data is short
		}

		size, ok := frameSize(data[off:])
		if !ok {
			return torn() // header is short
		}

		frameEnd := off + size
		if frameEnd > len(data) {
			return torn() // frame extends EOF
		}

		e, n, err := decode(data[off:])
		if err != nil {
			if frameEnd == len(data) {
				return torn()
			}
			return nil, fmt.Errorf("replay: corrupt record at offset %d: %w", off, err)
		}

		entries = append(entries, e)
		off += n
	}
	return entries, nil
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
