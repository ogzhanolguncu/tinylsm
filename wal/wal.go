package wal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Writer struct {
	f    *os.File
	file string
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
		file: name,
	}, nil
}

func (w *Writer) Append(e Entry) error {
	if w.err != nil {
		return fmt.Errorf("writer is broken: %w", w.err)
	}
	enc, err := encode(e)
	if err != nil {
		return fmt.Errorf("encode entry: %w", err)
	}
	n, err := w.f.Write(enc)
	if err != nil {
		w.err = fmt.Errorf("write wal after %d bytes: %w", n, err)
		return w.err
	}
	if n < len(enc) {
		w.err = io.ErrShortWrite
		return w.err
	}
	if err := w.f.Sync(); err != nil {
		w.err = fmt.Errorf("fsync wal (not durable): %w", err)
		return w.err
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

func (w *Writer) Close() error {
	var errs []error
	if err := w.f.Sync(); err != nil {
		errs = append(errs, fmt.Errorf("sync wal file %s: %w", w.file, err))
	}
	if err := w.f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close wal file %s: %w", w.file, err))
	}
	err := errors.Join(errs...)
	if err != nil {
		w.err = err
	}
	return err
}
