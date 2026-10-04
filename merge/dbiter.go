package merge

import (
	"bytes"
	"fmt"

	"github.com/ogzhanolguncu/tinylsm/keys"
)

type DBIter struct {
	it        Iterator
	key, val  []byte
	valid     bool
	err       error
	snapshot  uint64 // entries with seq >= snapshot were written after the scan
	currentIk []byte
}

// NewDBIter shows the data as of snapshot: pass the DB's next seq at the
// moment the scan starts. keys.MaxSeq means "see everything".
func NewDBIter(it Iterator, snapshot uint64) *DBIter {
	return &DBIter{
		it:       it,
		snapshot: snapshot,
	}
}

func (d *DBIter) findNextLive() {
	for {
		if !d.it.Valid() {
			d.valid = false
			return
		}

		uk, seq, kind, err := keys.Decode(d.it.Key())
		if err != nil {
			d.fail(err)
			return
		}

		if seq >= d.snapshot {
			d.it.Next() // written after the scan began: step past just this entry
			continue
		}

		switch kind {
		case keys.KindPut:
			d.key = bytes.Clone(uk)
			d.val = bytes.Clone(d.it.Value())
			d.currentIk = bytes.Clone(d.it.Key())
			d.valid = true
			return
		case keys.KindDelete:
			d.skipUserKey(uk)
		default:
			d.fail(fmt.Errorf("%w: %d", keys.ErrUnknownKind, kind))
			return
		}
	}
}

func (d *DBIter) fail(err error) {
	d.valid = false
	d.err = err
}

func (d *DBIter) Error() error {
	if d.err != nil {
		return d.err
	}
	if e, ok := d.it.(interface{ Error() error }); ok {
		return e.Error()
	}
	return nil
}

func (d *DBIter) skipUserKey(uk []byte) {
	for d.it.Valid() {
		cur, _, _, err := keys.Decode(d.it.Key())
		if err != nil || !bytes.Equal(cur, uk) {
			return
		}
		d.it.Next()
	}
}

func (d *DBIter) SeekToFirst() { d.err = nil; d.it.SeekToFirst(); d.findNextLive() }
func (d *DBIter) Valid() bool  { return d.valid }
func (d *DBIter) Next() {
	d.skipUserKey(d.key)
	d.findNextLive()
}
func (d *DBIter) Key() []byte   { return d.key }
func (d *DBIter) Value() []byte { return d.val }

func (d *DBIter) Seek(userKey []byte) {
	if len(userKey) == 0 {
		d.SeekToFirst()
		return
	}
	d.err = nil
	ik, err := keys.Encode(userKey, keys.MaxSeq, keys.KindPut)
	if err != nil {
		d.fail(err)
		return
	}

	d.it.Seek(ik)
	d.findNextLive()
}

func (d *DBIter) InternalKey() []byte { return d.currentIk }
