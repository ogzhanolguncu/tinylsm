// Command tinylsm is an interactive shell for poking at the engine and watching
// its insides move: memtable filling, flushes landing in L0, reads slowing as
// L0 piles up, and recovery after a real kill -9.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tinylsm "github.com/ogzhanolguncu/tinylsm"
	"github.com/ogzhanolguncu/tinylsm/keys"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	purple = "\033[35m"
	cyan   = "\033[36m"
)

// lastWordsFile holds the final writes before a `crash`; the next start checks them.
const lastWordsFile = ".lastwords.json"

type benchRun struct {
	tables int
	avg    time.Duration
}

type shell struct {
	dir     string
	opts    tinylsm.Options
	db      *tinylsm.DB
	flushes int
	recent  map[string]string // last writes, in case of `crash`
	order   []string
	benches []benchRun
	keyMax  int // highest user:N written by fill, so bench can hit real keys
}

func main() {
	dir := flag.String("dir", "./data", "database directory")
	threshold := flag.Uint64("threshold", 16*1024, "memtable bytes before a flush")
	flag.Parse()

	sh := &shell{dir: *dir, opts: tinylsm.Options{MemtableThreshold: *threshold}, recent: map[string]string{}}
	banner()
	sh.open()
	sh.keyMax = sh.probeKeyMax()
	sh.checkLastWords()
	sh.stats()

	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("%s%stinylsm>%s ", bold, purple, reset)
		if !in.Scan() {
			if err := in.Err(); err != nil {
				fmt.Printf("%sstdin:%s %v\n", red, reset, err)
			}
			fmt.Println()
			sh.quit()
			return
		}
		if !sh.run(strings.Fields(in.Text())) {
			return
		}
	}
}

func banner() {
	fmt.Print(cyan + `
  _   _             _
 | |_(_)_ __  _   _| |___ _ __ ___
 | __| | '_ \| | | | / __| '_ ` + "`" + ` _ \
 | |_| | | | | |_| | \__ \ | | | | |
  \__|_|_| |_|\__, |_|___/_| |_| |_|
              |___/   ` + reset + dim + `your LSM, live. type 'help'` + reset + "\n\n")
}

func (sh *shell) open() {
	start := time.Now()
	db, err := tinylsm.Open(sh.dir, sh.opts)
	if err != nil {
		fmt.Printf("%sopen failed:%s %v\n", red, reset, err)
		os.Exit(1)
	}
	sh.db = db
	s := db.Stats()
	fmt.Printf("%s✓ opened%s %s in %s%s%s  (replayed WAL + folded MANIFEST, %d L0 tables, seq %d)\n",
		green, reset, sh.dir, bold, time.Since(start).Round(time.Microsecond), reset, len(s.L0), s.NextSeq)
}

func (sh *shell) run(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "put", "set":
		if len(rest) < 2 {
			return usage("put <key> <value>")
		}
		sh.put(rest[0], strings.Join(rest[1:], " "))
	case "get":
		if len(rest) != 1 {
			return usage("get <key>")
		}
		sh.get(rest[0])
	case "del", "delete":
		if len(rest) != 1 {
			return usage("del <key>")
		}
		sh.del(rest[0])
	case "fill":
		sh.fill(intArg(rest, 2000))
	case "bench":
		sh.bench(intArg(rest, 2000))
	case "stats", "ls":
		sh.stats()
	case "crash":
		sh.crash()
	case "reset":
		sh.reset()
	case "scan":
		if len(rest) > 0 && rest[0] == "--raw" {
			sh.rawScan(intArg(rest[1:], 30))
		} else {
			locked("scan", "Phase 7 level 2 — dedup")
		}
	case "compact":
		locked("compact", "Phase 8 — compaction")
	case "help", "?":
		help()
	case "exit", "quit", "q":
		sh.quit()
		return false
	default:
		fmt.Printf("%s?%s unknown command %q, try 'help'\n", yellow, reset, cmd)
	}
	return true
}

func (sh *shell) put(k, v string) {
	before := len(sh.db.Stats().L0)
	start := time.Now()
	if err := sh.db.Put([]byte(k), []byte(v)); err != nil {
		fmt.Printf("%serror:%s %v\n", red, reset, err)
		return
	}
	sh.remember(k, v)
	fmt.Printf("%sok%s %s%s%s\n", green, reset, dim, time.Since(start).Round(time.Microsecond), reset)
	sh.announceFlushes(before)
}

func (sh *shell) get(k string) {
	start := time.Now()
	val, found, err := sh.db.Get([]byte(k))
	took := time.Since(start).Round(time.Microsecond)
	switch {
	case err != nil:
		fmt.Printf("%serror:%s %v\n", red, reset, err)
	case !found:
		fmt.Printf("%s(not found)%s %s%s, searched mem + %d tables%s\n", yellow, reset, dim, took, len(sh.db.Stats().L0), reset)
	default:
		fmt.Printf("%s%q%s %s%s%s\n", bold, val, reset, dim, took, reset)
	}
}

func (sh *shell) del(k string) {
	before := len(sh.db.Stats().L0)
	if err := sh.db.Delete([]byte(k)); err != nil {
		fmt.Printf("%serror:%s %v\n", red, reset, err)
		return
	}
	sh.remember(k, "")
	fmt.Printf("%s🪦 tombstone written%s for %s\n", dim, reset, k)
	sh.announceFlushes(before)
}

func (sh *shell) announceFlushes(before int) {
	s := sh.db.Stats()
	for _, t := range s.L0[before:] {
		sh.flushes++
		fmt.Printf("  %s⚡ FLUSH%s memtable → %s%09d.sst%s (%s)  L0 now has %d tables\n",
			yellow, reset, bold, t.FileNum, reset, size(t.Bytes), len(s.L0))
	}
}

func (sh *shell) fill(n int) {
	before := len(sh.db.Stats().L0)
	start := time.Now()
	val := make([]byte, 100)
	for i := range n {
		k := fmt.Sprintf("user:%08d", sh.keyMax+i)
		for j := range val {
			val[j] = byte('a' + rand.IntN(26))
		}
		if err := sh.db.Put([]byte(k), val); err != nil {
			fmt.Printf("\n%serror:%s %v\n", red, reset, err)
			return
		}
		if i%500 == 0 || i == n-1 {
			fmt.Printf("\r  %s %d/%d  %s%d flushes%s", bar(float64(i+1)/float64(n), 30, green),
				i+1, n, yellow, len(sh.db.Stats().L0)-before, reset)
		}
		if n-i <= 5 {
			sh.remember(k, string(val))
		}
	}
	sh.keyMax += n
	took := time.Since(start)
	after := len(sh.db.Stats().L0)
	sh.flushes += after - before
	fmt.Printf("\n%s✓ %d puts%s in %s → %s%.0f ops/s%s, %d new sstables (L0: %d)\n",
		green, n, reset, took.Round(time.Millisecond), bold, float64(n)/took.Seconds(), reset, after-before, after)
	sh.milestones(after)
}

func (sh *shell) milestones(tables int) {
	switch {
	case tables >= 100:
		fmt.Printf("  %s🔥 %d tables in L0. Every miss reads all of them. This is the pain compaction (Phase 8) kills.%s\n", red, tables, reset)
	case tables >= 20:
		fmt.Printf("  %s👀 L0 is getting tall. Try 'bench' and watch reads slow down.%s\n", yellow, reset)
	}
}

func (sh *shell) bench(n int) {
	if sh.keyMax == 0 {
		fmt.Printf("%sno fill data yet%s — run 'fill' first\n", yellow, reset)
		return
	}
	tables := len(sh.db.Stats().L0)
	var hit, miss time.Duration
	for range n {
		k := fmt.Sprintf("user:%08d", rand.IntN(sh.keyMax))
		start := time.Now()
		if _, _, err := sh.db.Get([]byte(k)); err != nil {
			fmt.Printf("%serror:%s %v\n", red, reset, err)
			return
		}
		hit += time.Since(start)

		start = time.Now()
		_, _, _ = sh.db.Get(fmt.Appendf(nil, "nope:%08d", rand.IntN(1<<30)))
		miss += time.Since(start)
	}
	avgHit, avgMiss := hit/time.Duration(n), miss/time.Duration(n)
	fmt.Printf("  hit  avg %s%s%s\n  miss avg %s%s%s  %s(a miss must check every one of %d tables)%s\n",
		bold, avgHit.Round(time.Nanosecond*100), reset, bold, avgMiss.Round(time.Nanosecond*100), reset, dim, tables, reset)

	sh.benches = append(sh.benches, benchRun{tables: tables, avg: avgMiss})
	if len(sh.benches) > 1 {
		fmt.Printf("\n  %smiss latency vs L0 height%s\n", bold, reset)
		worst := slices.MaxFunc(sh.benches, func(a, b benchRun) int { return int(a.avg - b.avg) }).avg
		for _, b := range sh.benches {
			fmt.Printf("  %4d tables %s %s\n", b.tables,
				bar(float64(b.avg)/float64(worst), 30, red), b.avg.Round(time.Nanosecond*100))
		}
	}
}

func (sh *shell) rawScan(limit int) {
	n, shown := 0, 0
	var prevUser []byte
	start := time.Now()
	sh.db.RawScan(func(ik, val []byte) bool {
		n++
		if shown >= limit {
			return true // keep counting
		}
		shown++
		user, seq, kind, err := keys.Decode(ik)
		if err != nil {
			fmt.Printf("  %sbad key:%s %v\n", red, reset, err)
			return false
		}
		// older versions of the same key: what Level 2 will hide
		older := bytes.Equal(user, prevUser)
		prevUser = append(prevUser[:0], user...)
		switch {
		case kind == keys.KindDelete:
			fmt.Printf("  %s%-16s seq %-6d 🪦 tombstone%s\n", red, user, seq, reset)
		case older:
			fmt.Printf("  %s%-16s seq %-6d %s  (older version, shadowed)%s\n", dim, user, seq, preview(val), reset)
		default:
			fmt.Printf("  %s%-16s%s seq %-6d %s\n", bold, user, reset, seq, preview(val))
		}
		return true
	})
	if n > shown {
		fmt.Printf("  %s… %d more%s\n", dim, n-shown, reset)
	}
	fmt.Printf("%s✓ merged %d entries%s from memtable + %d tables in %s\n",
		green, n, reset, len(sh.db.Stats().L0), time.Since(start).Round(time.Microsecond))
}

func preview(v []byte) string {
	if len(v) > 24 {
		return fmt.Sprintf("%q…", v[:24])
	}
	return fmt.Sprintf("%q", v)
}

func (sh *shell) stats() {
	s := sh.db.Stats()
	fill := float64(s.MemBytes) / float64(s.MemThreshold)
	fmt.Printf("\n  %smemtable%s %s %s / %s\n", bold, reset, bar(fill, 30, cyan), size(int64(s.MemBytes)), size(int64(s.MemThreshold)))

	var total int64
	for _, t := range s.L0 {
		total += t.Bytes
	}
	fmt.Printf("  %sL0%s       %d tables, %s   %sseq %d · next file %d · %d flushes this session%s\n",
		bold, reset, len(s.L0), size(total), dim, s.NextSeq, s.NextFileNum, sh.flushes, reset)

	// newest on top: that's the order Get searches them
	const shown = 8
	for i := len(s.L0) - 1; i >= 0 && i >= len(s.L0)-shown; i-- {
		t := s.L0[i]
		fmt.Printf("           %s┃%s %09d.sst %s%s%s\n", yellow, reset, t.FileNum, dim, size(t.Bytes), reset)
	}
	if len(s.L0) > shown {
		fmt.Printf("           %s┃ … %d more%s\n", dim, len(s.L0)-shown, reset)
	}
	for _, lvl := range []string{"L1", "L2"} {
		fmt.Printf("  %s%s       🔒 empty until Phase 8%s\n", dim, lvl, reset)
	}
	fmt.Println()
}

// probeKeyMax finds how many user:N keys earlier fills left behind, so bench
// works after a restart. Fill writes them contiguously from 0, so search.
func (sh *shell) probeKeyMax() int {
	exists := func(i int) bool {
		_, found, err := sh.db.Get(fmt.Appendf(nil, "user:%08d", i))
		return err == nil && found
	}
	if !exists(0) {
		return 0
	}
	hi := 1
	for exists(hi) {
		hi *= 2
	}
	return sort.Search(hi, func(i int) bool { return !exists(i) })
}

func (sh *shell) remember(k, v string) {
	if _, ok := sh.recent[k]; !ok {
		sh.order = append(sh.order, k)
	}
	sh.recent[k] = v
	if len(sh.order) > 5 {
		delete(sh.recent, sh.order[0])
		sh.order = sh.order[1:]
	}
}

// crash dies by SIGKILL: no Close, no deferred flush, no goodbye. The next
// start reads the last writes back to prove the WAL did its job.
func (sh *shell) crash() {
	if len(sh.recent) == 0 {
		fmt.Printf("%swrite something first%s so there's something to lose\n", yellow, reset)
		return
	}
	data, _ := json.Marshal(sh.recent)
	if err := os.WriteFile(filepath.Join(sh.dir, lastWordsFile), data, 0o644); err != nil {
		fmt.Printf("%serror:%s %v\n", red, reset, err)
		return
	}
	fmt.Printf("%s💀 kill -9 in 3…2…1%s  (restart me to see what survived)\n", red, reset)
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
}

func (sh *shell) checkLastWords() {
	path := filepath.Join(sh.dir, lastWordsFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	_ = os.Remove(path)
	var want map[string]string
	if err != nil || json.Unmarshal(data, &want) != nil {
		return
	}
	fmt.Printf("\n%s🧟 back from a kill -9.%s checking the last %d writes…\n", bold, reset, len(want))
	ok := 0
	for k, v := range want {
		got, found, err := sh.db.Get([]byte(k))
		deleted := v == ""
		if err == nil && found != deleted && (deleted || string(got) == v) {
			ok++
			fmt.Printf("  %s✓%s %s\n", green, reset, k)
		} else {
			fmt.Printf("  %s✗ LOST%s %s (found=%v err=%v)\n", red, reset, k, found, err)
		}
	}
	if ok == len(want) {
		fmt.Printf("%s%s  %d/%d survived. Your WAL works. 🎉%s\n", bold, green, ok, len(want), reset)
	} else {
		fmt.Printf("%s%s  %d/%d survived. Found a durability bug, nice catch.%s\n", bold, red, ok, len(want), reset)
	}
}

func (sh *shell) reset() {
	_ = sh.db.Close()
	if err := os.RemoveAll(sh.dir); err != nil {
		fmt.Printf("%serror:%s %v\n", red, reset, err)
		return
	}
	*sh = shell{dir: sh.dir, opts: sh.opts, recent: map[string]string{}}
	fmt.Printf("%s🧹 wiped%s %s\n", yellow, reset, sh.dir)
	sh.open()
}

func (sh *shell) quit() {
	if err := sh.db.Close(); err != nil {
		fmt.Printf("%sclose:%s %v\n", red, reset, err)
	}
	fmt.Printf("%sbye. everything's on disk.%s\n", dim, reset)
}

func help() {
	fmt.Printf(`
  %sput%s <k> <v>     write a key          %sfill%s [n]      bulk-load n keys (default 2000)
  %sget%s <k>         read a key           %sbench%s [n]     time hits vs misses, chart vs L0 height
  %sdel%s <k>         write a tombstone    %sstats%s         memtable + L0 picture
  %scrash%s           SIGKILL myself, then restart to verify recovery
  %sreset%s           wipe the data dir    %squit%s          close cleanly
  %sscan --raw%s [n]  every version + tombstone, merged across all tables
  %sscan  compact   🔒 locked — unlock them by building Phase 7 and 8%s

`, cyan, reset, cyan, reset, cyan, reset, cyan, reset, cyan, reset, cyan, reset, red, reset, yellow, reset, cyan, reset, cyan, reset, dim, reset)
}

func locked(cmd, phase string) {
	fmt.Printf("%s🔒 '%s' is locked.%s Build %s%s%s to unlock it.\n", dim, cmd, reset, bold, phase, reset)
}

func usage(u string) bool {
	fmt.Printf("%susage:%s %s\n", yellow, reset, u)
	return true
}

func intArg(args []string, def int) int {
	if len(args) == 0 {
		return def
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func bar(frac float64, width int, color string) string {
	frac = min(max(frac, 0), 1)
	full := int(frac * float64(width))
	return color + strings.Repeat("█", full) + reset + dim + strings.Repeat("░", width-full) + reset
}

func size(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}
