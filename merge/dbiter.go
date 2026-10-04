package merge

import (
	"bytes"
	"fmt"

	"github.com/ogzhanolguncu/tinylsm/keys"
)

// DBIter is the user's view: it wraps a merged stream of internal keys and
// yields each live user key once, with its newest value. Older versions and
// deleted keys never come out. LevelDB calls this DBIter (db/db_iter.cc).
//
// Key returns the USER key (no seq, no kind), Value the newest value.
type DBIter struct {
	it       Iterator
	key, val []byte
	valid    bool
	err      error
}

func NewDBIter(it Iterator) *DBIter {
	return &DBIter{
		it: it,
	}
}

func (d *DBIter) findNextLive() {
	for {
		if !d.it.Valid() {
			d.valid = false
			return
		}

		uk, _, kind, err := keys.Decode(d.it.Key())
		if err != nil {
			d.fail(err)
			return
		}
		switch kind {
		case keys.KindPut:
			d.key = bytes.Clone(uk)
			d.val = bytes.Clone(d.it.Value())
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

// fail stops iteration for good; the caller finds out why through Error.
func (d *DBIter) fail(err error) {
	d.valid = false
	d.err = err
}

// Error reports why iteration stopped early. Check it after the loop:
// a corrupt key ends the scan, and without this it looks like the end of data.
func (d *DBIter) Error() error { return d.err }

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
