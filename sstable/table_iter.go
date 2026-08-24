package sstable

import (
	"github.com/ogzhanolguncu/tinylsm/pkg/contract"
)

type Iter struct {
	t       *Table
	indexIt *blockIter
	block   *block
	dataIt  *blockIter
	err     error
}

func (it *Iter) SeekToFirst() {
	it.indexIt.i = 0
	if !it.indexIt.Valid() {
		return
	}
	err := it.loadBlock()
	if err != nil && it.err == nil {
		it.err = err
	}
}

func (it *Iter) Seek(target []byte) {
	it.indexIt.Seek(target)
	if !it.indexIt.Valid() {
		return
	}
	err := it.loadBlock()
	if err != nil {
		if it.err == nil {
			it.err = err
		}
		return
	}
	it.dataIt.Seek(target)
}

func (it *Iter) Next() {
	contract.Require(it.Valid(), "tableIter.Next: iterator is exhausted")
	it.dataIt.Next()
	if it.dataIt.Valid() {
		return
	}
	it.indexIt.Next()
	if !it.indexIt.Valid() {
		return
	}
	err := it.loadBlock()
	if err != nil {
		if it.err == nil {
			it.err = err
		}
		return
	}
}

func (it *Iter) Valid() bool {
	return it.err == nil && it.dataIt != nil && it.dataIt.Valid()
}

func (it *Iter) Key() []byte {
	contract.Require(it.Valid(), "tableIter.Key: iterator is exhausted")
	return it.dataIt.Key()
}

func (it *Iter) Value() []byte {
	contract.Require(it.Valid(), "tableIter.Value: iterator is exhausted")
	return it.dataIt.Value()
}

func (it *Iter) Error() error {
	return it.err
}

func (it *Iter) loadBlock() error {
	contract.Require(it.indexIt.Valid(), "tableIter.loadBlock: indexIt is not positioned")
	val := it.indexIt.Value()
	off, size, err := blockHandle(val)
	if err != nil {
		return err
	}

	blk, err := readBlock(it.t.f, off, size, it.t.fsize)
	if err != nil {
		return err
	}
	it.block = blk
	it.dataIt = &blockIter{b: blk, i: 0}
	return nil
}
