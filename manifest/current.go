package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrBadCurrent = errors.New("manifest: bad CURRENT file")

// WriteCurrent atomically writes the CURRENT file pointing to the
// named MANIFEST. Atomic = write tmp → fsync → rename → fsync dir.
func WriteCurrent(dir, manifestName string) error {
	tmp, final := filepath.Join(dir, "CURRENT.tmp"), filepath.Join(dir, "CURRENT")
	data := []byte(manifestName + "\n")

	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open manifest dir: %w", err)
	}
	defer func() {
		_ = d.Close()
	}()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp manifest file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	_, err = f.Write(data)
	if err != nil {
		return fmt.Errorf("write temp manifest file: %w", err)
	}
	err = f.Sync()
	if err != nil {
		return fmt.Errorf("fsync temp manifest file: %w", err)
	}

	err = os.Rename(tmp, final)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("rename temp manifest file: %w", err)
	}

	_ = f.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync manifest dir: %w", err)
	}
	return nil
}

// ReadCurrent reads the CURRENT file and returns the MANIFEST
// filename it points to.
func ReadCurrent(dir string) (string, error) {
	final := filepath.Join(dir, "CURRENT")
	data, err := os.ReadFile(final)
	if err != nil {
		return "", fmt.Errorf("read CURRENT file: %w", err)
	}

	s := string(data)
	if !strings.HasSuffix(s, "\n") {
		return "", ErrBadCurrent
	}
	name := strings.TrimSuffix(s, "\n")
	if !strings.HasPrefix(name, "MANIFEST-") {
		return "", ErrBadCurrent
	}
	if filepath.Base(name) != name {
		return "", ErrBadCurrent
	}

	return name, nil
}
