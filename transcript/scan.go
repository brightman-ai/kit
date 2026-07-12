// Package transcript — scan.go: the shared primitives for FINDING transcript files and
// READING the part of them you actually need.
//
// Both runtimes store JSONL that grows without bound (a single codex rollout reaches tens of
// MB). Most consumers do not need the whole file:
//
//   - "is there any transcript here at all?"        → HasAnyFile (stops at the first hit)
//   - "what are the most recent sessions?"          → NewestFiles (bounded, newest-first)
//   - "what is the latest X the runtime recorded?"  → ScanTail (reads only the end)
//
// Before these existed, each consumer wrote its own walker and its own full-file read; the
// quota probe re-read a 12 MB rollout every 60 seconds to find one object near its end.
package transcript

import (
	"bufio"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File naming shared by the runtimes: codex names each session rollout-<ts>-<uuid>.jsonl,
// and both runtimes write JSONL.
const (
	RolloutPrefix = "rollout-"
	JSONLSuffix   = ".jsonl"
)

// DefaultTailBytes is a sane tail window for "the newest record in this transcript": large
// enough to span the last few minutes of events, small enough to stay cheap at poll rates.
const DefaultTailBytes int64 = 1 << 20 // 1 MiB

// HasAnyFile reports whether root holds at least one file with the given suffix, anywhere in
// its tree. It stops at the first hit.
//
// The bar is deliberately a FILE, not a directory: both CLIs leave dated directory skeletons
// (sessions/2026/07/12/) behind even when they never wrote a transcript there, so "the
// directory exists" proves nothing about whether this account was ever used.
func HasAnyFile(root, suffix string) bool {
	found := false
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr — an unreadable subtree is simply not evidence
		}
		if strings.HasSuffix(d.Name(), suffix) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// NewestFiles returns up to n files under root matching prefix+suffix, newest first by mtime.
// n <= 0 returns every match. A missing root is an honest empty list, never an error — an
// absent runtime must not degrade the caller.
func NewestFiles(root, prefix, suffix string, n int) []string {
	type entry struct {
		path string
		mod  time.Time
	}
	var found []entry
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr — tolerate unreadable subtrees; keep walking
		}
		name := d.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			return nil
		}
		if info, e := d.Info(); e == nil {
			found = append(found, entry{path, info.ModTime()})
		}
		return nil
	})
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	if n > 0 && len(found) > n {
		found = found[:n]
	}
	paths := make([]string, 0, len(found))
	for _, e := range found {
		paths = append(paths, e.path)
	}
	return paths
}

// ScanTail calls fn for each COMPLETE line in the last tailBytes of path (the whole file when
// it is smaller). A leading fragment produced by seeking into the middle of a line is dropped
// rather than handed to fn as if it were a record. fn returning false stops the scan.
//
// Lines arrive in file order, so a caller looking for "the latest record" should keep the last
// match rather than the first.
func ScanTail(path string, tailBytes int64, fn func(line []byte) bool) error {
	f, err := os.Open(path) //nolint:gosec — read-only transcript scan
	if err != nil {
		return err
	}
	defer f.Close()

	fragment := false
	if fi, err := f.Stat(); err == nil && tailBytes > 0 && fi.Size() > tailBytes {
		if _, err := f.Seek(fi.Size()-tailBytes, io.SeekStart); err == nil {
			fragment = true // the first line we read is the tail of a longer one
		}
	}

	br := bufio.NewReaderSize(f, 256*1024)
	for first := true; ; first = false {
		line, readErr := br.ReadBytes('\n')
		if !(first && fragment) && len(line) > 0 {
			if !fn(bytes.TrimRight(line, "\r\n")) {
				return nil
			}
		}
		if readErr != nil {
			return nil //nolint:nilerr — EOF (or a truncated final line) ends the scan normally
		}
	}
}
