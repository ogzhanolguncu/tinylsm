# tinylsm

LSM-tree KV store written in Go for educational purposes. This one is some sort of continuation of [tinycask](https://github.com/ogzhanolguncu/tinycask). The design follows LevelDB's [impl.md](https://github.com/google/leveldb/blob/main/doc/impl.md): WAL, skiplist memtable, SSTables, MANIFEST, compaction, with some improvements on top.

Scans are snapshots and don't block writers. `Scan` holds the lock just long enough to grab its iterators and the current seq, then anything written later is skipped. Overwrites and deletes that land mid-scan don't leak into it.

Compaction can run mid-scan. Tables are reference counted, so a scan keeps the files it's reading alive, and the last reader closes them. Iterator errors propagate too, so a broken table fails the scan instead of silently cutting it short.

Crashing during a flush or compaction is safe. The MANIFEST edit is the commit point, and any file it doesn't list is cleaned up on open.

A torn tail in the WAL or MANIFEST is truncated back to the last good record. Corruption anywhere else refuses to open instead of guessing.

Bloom filters and an L0 compaction trigger at 4 tables keep reads cheap:

```sh
Get latency (avg), 88 tables in L0
                        missing key   existing key
before                   174.9 µs       84.6 µs
bloom filters only         6.8 µs           –
compaction only            1.7 µs        1.6 µs
```

`NoSync` skips the per-write fsync, ~210 → ~1,200 writes/s.

## Future Improvements

Some improvements I'd do if I had the energy

Block cache, every table read is a `pread` plus a CRC check, even for hot keys. I'd love to reach for `mmap` like in tinycask, but an LSM juggles far more files than Bitcask, with compaction deleting them mid-read, so a lazily done mmap would probably break things.

Leveled compaction, right now every compaction rewrites everything.

Background flush and compaction, so the unlucky `Put` doesn't pay for them.
