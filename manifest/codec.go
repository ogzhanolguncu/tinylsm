package manifest

import (
	"encoding/binary"
	"errors"
)

var ErrMalformed = errors.New("manifest: malformed payload")

const (
	tagNextFileNum byte = 1
	tagLastSeq     byte = 2
	tagAddFile     byte = 3
	tagDelFile     byte = 4
)

func encode(e VersionEdit) []byte {
	var p []byte

	for _, f := range e.AddFiles {
		p = append(p, tagAddFile)
		p = binary.AppendUvarint(p, uint64(f.Level))
		p = binary.AppendUvarint(p, uint64(f.FileNum))
	}

	for _, f := range e.DelFiles {
		p = append(p, tagDelFile)
		p = binary.AppendUvarint(p, uint64(f.Level))
		p = binary.AppendUvarint(p, uint64(f.FileNum))
	}

	if e.NextFileNum > 0 {
		p = append(p, tagNextFileNum)
		p = binary.AppendUvarint(p, e.NextFileNum)
	}

	if e.LastSeq > 0 {
		p = append(p, tagLastSeq)
		p = binary.AppendUvarint(p, e.LastSeq)
	}

	return p
}

func decode(p []byte) (VersionEdit, error) {
	var e VersionEdit

	for len(p) > 0 {
		tag := p[0]
		p = p[1:]

		switch tag {
		case tagNextFileNum:
			v, n := binary.Uvarint(p)
			if n <= 0 {
				return VersionEdit{}, ErrMalformed
			}
			e.NextFileNum = v
			p = p[n:]
		case tagLastSeq:
			v, n := binary.Uvarint(p)
			if n <= 0 {
				return VersionEdit{}, ErrMalformed
			}
			e.LastSeq = v
			p = p[n:]
		case tagAddFile:
			var fmt FileMeta
			v, n := binary.Uvarint(p)
			if n <= 0 {
				return VersionEdit{}, ErrMalformed
			}
			fmt.Level = uint32(v)
			p = p[n:]

			v, n = binary.Uvarint(p)
			if n <= 0 {
				return VersionEdit{}, ErrMalformed
			}
			fmt.FileNum = v
			e.AddFiles = append(e.AddFiles, fmt)
			p = p[n:]
		case tagDelFile:
			var fmt FileMeta
			v, n := binary.Uvarint(p)
			if n <= 0 {
				return VersionEdit{}, ErrMalformed
			}
			fmt.Level = uint32(v)
			p = p[n:]

			v, n = binary.Uvarint(p)
			if n <= 0 {
				return VersionEdit{}, ErrMalformed
			}
			fmt.FileNum = v
			e.DelFiles = append(e.DelFiles, fmt)
			p = p[n:]
		default:
			return VersionEdit{}, ErrMalformed
		}
	}
	return e, nil
}
