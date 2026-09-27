package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/ogzhanolguncu/tinylsm/keys"
	"github.com/ogzhanolguncu/tinylsm/pkg/frame"
	"github.com/ogzhanolguncu/tinylsm/pkg/uvarint"
)

type Entry struct {
	Key, Value []byte
	Seq        uint64
	Kind       Kind
}

var (
	ErrMalformed    = errors.New("wal: malformed record")
	ErrInvalidInput = errors.New("wal: invalid input")
)

const (
	// payload-relative offsets — CRC covers all of payload
	pOffSeq    = 0            // uint64, 8 bytes
	pOffKind   = pOffSeq + 8  // byte,   1 byte
	pOffKeyLen = pOffKind + 1 // varint begins — last fixed offset (= 9)
)

const minRecordSize = frame.HeaderSize + pOffKeyLen + 2 // two 1-byte varints: keyLen=0, valLen=0

var Castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Alias, not a new type — keys owns the definition, so no conversion at the
// boundary.
type Kind = keys.Kind

const (
	KindPut    = keys.KindPut
	KindDelete = keys.KindDelete
)

func encode(entry Entry) ([]byte, error) {
	p, err := buildPayload(entry)
	if err != nil {
		return nil, fmt.Errorf("payload construction: %w", err)
	}
	return frame.Frame(p), nil
}

func buildPayload(entry Entry) ([]byte, error) {
	key, value, kind, seq := entry.Key, entry.Value, entry.Kind, entry.Seq

	if kind != KindPut && kind != KindDelete {
		return nil, ErrInvalidInput
	}
	if kind == KindDelete && len(value) > 0 {
		return nil, ErrInvalidInput
	}

	p := make([]byte, 0, pOffKeyLen+uvarint.SizeUvarint(uint64(len(key)))+len(key)+uvarint.SizeUvarint(uint64(len(value)))+len(value))
	p = binary.LittleEndian.AppendUint64(p, seq)
	p = append(p, byte(kind))
	p = binary.AppendUvarint(p, uint64(len(key)))
	p = append(p, key...)
	p = binary.AppendUvarint(p, uint64(len(value)))
	p = append(p, value...)
	return p, nil
}

// decode parses one framed record from rec
// zero-copy, Entry.key and Entry.value alias rec's memory.
// They are valid only until caller modifies or reuses rec
// Callers that retain the Entry should copy to be safe
func decode(rec []byte) (Entry, int, error) {
	payload, length, err := frame.ValidateFrame(rec)
	if err != nil {
		return Entry{}, 0, fmt.Errorf("validate frame: %w", err)
	}
	if int(length) < minRecordSize-frame.HeaderSize {
		return Entry{}, 0, ErrMalformed
	}

	seq := binary.LittleEndian.Uint64(payload[pOffSeq:pOffKind])
	kind := Kind(payload[pOffKind])
	if kind > KindDelete {
		return Entry{}, 0, ErrMalformed
	}
	off := pOffKeyLen

	keyLen, n := binary.Uvarint(payload[off:])
	if n <= 0 {
		return Entry{}, 0, ErrMalformed
	}
	off += n
	// Payload size - bytes consumed so far
	rem := uint64(len(payload) - off)
	if keyLen > rem {
		return Entry{}, 0, ErrMalformed
	}
	key := payload[off : off+int(keyLen)]
	off += int(keyLen)

	valLen, n := binary.Uvarint(payload[off:])
	if n <= 0 {
		return Entry{}, 0, ErrMalformed
	}
	if kind == KindDelete && valLen != 0 {
		return Entry{}, 0, ErrMalformed
	}
	off += n

	rem = uint64(len(payload) - off)
	if valLen > rem {
		return Entry{}, 0, ErrMalformed
	}
	value := payload[off : off+int(valLen)]
	off += int(valLen)

	if off != int(length) {
		return Entry{}, 0, ErrMalformed
	}

	return Entry{
		Key:   key,
		Value: value,
		Seq:   seq,
		Kind:  kind,
	}, off + frame.HeaderSize, nil
}
