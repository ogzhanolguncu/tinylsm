package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	tinylsm "github.com/ogzhanolguncu/tinylsm"
)

func main() {
	dir := flag.String("dir", "./data", "database directory")
	threshold := flag.Uint64("threshold", 256, "memtable bytes before a flush is forced (tiny, so flushes are visible)")
	flag.Parse()

	opts := tinylsm.Options{MemtableThreshold: *threshold}

	db := must(tinylsm.Open(*dir, opts))
	listFiles(*dir, "after open")

	for i := range 30 {
		check(db.Put(key(i), fmt.Appendf(nil, "v1-%02d", i)))
	}
	listFiles(*dir, "after 30 puts")

	check(db.Put(key(3), []byte("v2-03")))
	check(db.Delete(key(7)))
	show(db, "before close", 3, 7, 29)

	check(db.Close())

	db = must(tinylsm.Open(*dir, opts))
	listFiles(*dir, "after reopen")
	show(db, "after reopen", 3, 7, 29)

	check(db.Put(key(7), []byte("v3-07 resurrected")))
	show(db, "after resurrecting key 7", 7)

	check(db.Close())
}

func key(i int) []byte { return fmt.Appendf(nil, "key-%02d", i) }

func show(db *tinylsm.DB, label string, ids ...int) {
	fmt.Printf("== %s\n", label)
	for _, i := range ids {
		val, found, err := db.Get(key(i))
		check(err)
		if found {
			fmt.Printf("  %s = %q\n", key(i), val)
		} else {
			fmt.Printf("  %s   (not found)\n", key(i))
		}
	}
}

func listFiles(dir, label string) {
	entries := must(os.ReadDir(dir))
	fmt.Printf("== files %s\n", label)
	for _, e := range entries {
		info := must(e.Info())
		fmt.Printf("  %-16s %6d bytes\n", filepath.Base(e.Name()), info.Size())
	}
}

func must[T any](v T, err error) T {
	check(err)
	return v
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
