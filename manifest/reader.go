package manifest

import (
	"fmt"
	"os"

	"github.com/ogzhanolguncu/tinylsm/pkg/frame"
)

// Fold reads a MANIFEST file from start to end, decoding each
// framed VersionEdit and applying it to build the current Version.
// Torn tail at EOF is tolerated (same as WAL replay).
func Fold(path string) (*Version, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	v := NewVersion()
	off := 0
	fileEnd := len(data)

	for off < fileEnd {
		size, ok := frame.FrameSize(data[off:])
		if !ok {
			break
		}

		frameEnd := off + size
		if frameEnd > fileEnd {
			break
		}

		payload, _, err := frame.ValidateFrame(data[off:])
		if err != nil {
			if frameEnd == fileEnd {
				break
			} else {
				return nil, err
			}
		}

		e, err := decode(payload)
		if err != nil {
			return nil, fmt.Errorf("manifest: corrupt record at offset %d: %w", off, err)
		}

		v = v.Apply(e)
		off = frameEnd

	}
	return v, nil
}
