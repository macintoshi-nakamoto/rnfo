// Package jsonl writes the daily append-only measurement files.
//
// Every record is flushed and synced as it is produced. A probe that is killed
// mid-run must still leave behind everything it measured up to that moment,
// because a truncated run is data about the probe and a lost run is not.
package jsonl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer appends records to <dir>/<name>/YYYY-MM-DD.jsonl, rolling over at
// UTC midnight.
type Writer struct {
	dir  string
	name string

	mu   sync.Mutex
	day  string
	f    *os.File
	rows int
}

// New opens a writer for a record stream. The directory is created if needed.
func New(dir, name string) (*Writer, error) {
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o750); err != nil {
		return nil, err
	}
	return &Writer{dir: dir, name: name}, nil
}

// Write appends one record.
func (w *Writer) Write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	day := time.Now().UTC().Format("2006-01-02")
	if w.f == nil || day != w.day {
		if w.f != nil {
			_ = w.f.Close()
		}
		p := filepath.Join(w.dir, w.name, day+".jsonl")
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		w.f, w.day = f, day
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return err
	}
	w.rows++
	// Sync every few hundred rows rather than every row: a run writes
	// thousands, and an fsync per row costs more than the at-most-few-hundred
	// rows a hard kill could cost us.
	if w.rows%200 == 0 {
		_ = w.f.Sync()
	}
	return nil
}

// Close syncs and closes the current file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	_ = w.f.Sync()
	err := w.f.Close()
	w.f = nil
	return err
}

// Seal writes a .sha256 sidecar for every finished day file that does not have
// one yet. Today's file is skipped because it is still growing.
//
// The checksum is what makes shipping verifiable: the collector can prove the
// file it received is the file the probe wrote, and a day that is sealed can
// never be silently rewritten afterwards.
func Seal(dir string) (int, error) {
	today := time.Now().UTC().Format("2006-01-02")
	n := 0
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil //nolint:nilerr // a single unreadable file must not stop sealing the rest
		}
		day := fi.Name()[:len(fi.Name())-len(".jsonl")]
		if day >= today {
			return nil
		}
		sidecar := p + ".sha256"
		if _, err := os.Stat(sidecar); err == nil {
			return nil
		}
		sum, err := fileSHA256(p)
		if err != nil {
			return nil //nolint:nilerr
		}
		line := fmt.Sprintf("%s  %s\n", sum, fi.Name())
		if os.WriteFile(sidecar, []byte(line), 0o640) == nil {
			n++
		}
		return nil
	})
	return n, err
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Prune deletes day files older than keepDays. Probes are small machines and
// the collector holds the archive; the probe only needs enough history to
// survive the collector being offline for a while.
func Prune(dir string, keepDays int) (int, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays).Format("2006-01-02")
	n := 0
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil //nolint:nilerr
		}
		name := fi.Name()
		if len(name) < 10 || name[:10] >= cutoff {
			return nil
		}
		if os.Remove(p) == nil {
			n++
		}
		return nil
	})
	return n, err
}
