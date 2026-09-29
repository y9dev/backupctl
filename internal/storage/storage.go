package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var safeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func Sanitize(s string) string {
	s = strings.TrimSpace(s)
	s = safeName.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._")
	if s == "" {
		s = "unnamed"
	}
	if len(s) > 128 {
		s = s[:128]
	}
	return s
}

func Timestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02_15-04-05")
}

// JobDir returns storage/<server>/<job...> dir. jobPath may contain slashes for nested (e.g. postgres/production).
func JobDir(base, server, job string) string {
	return filepath.Join(base, Sanitize(server), jobSanitized(job))
}

func jobSanitized(job string) string {
	parts := strings.Split(job, "/")
	for i, p := range parts {
		parts[i] = Sanitize(p)
	}
	return filepath.Join(parts...)
}

func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// AtomicWriter writes to tmp file then renames on Commit.
type AtomicWriter struct {
	final string
	tmp   string
	f     *os.File
	hash  *hashWriter
	size  int64
}

type hashWriter struct {
	h hash.Hash
	n int64
}

func (w *hashWriter) Write(p []byte) (int, error) {
	_, _ = w.h.Write(p)
	w.n += int64(len(p))
	return len(p), nil
}

func NewAtomicWriter(final string) (*AtomicWriter, error) {
	if err := EnsureDir(filepath.Dir(final)); err != nil {
		return nil, err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", final, time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &AtomicWriter{final: final, tmp: tmp, f: f, hash: &hashWriter{h: sha256.New()}}, nil
}

func (w *AtomicWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if err == nil {
		w.hash.Write(p[:n])
		w.size += int64(n)
	}
	return n, err
}

func (w *AtomicWriter) Abort() {
	w.f.Close()
	os.Remove(w.tmp)
}

func (w *AtomicWriter) Commit() (checksum string, size int64, err error) {
	if err := w.f.Close(); err != nil {
		os.Remove(w.tmp)
		return "", 0, err
	}
	sum := w.hash.h.Sum(nil)
	checksum = hex.EncodeToString(sum)
	if err := os.Rename(w.tmp, w.final); err != nil {
		os.Remove(w.tmp)
		return "", 0, err
	}
	return checksum, w.size, nil
}

// ChecksumFile computes SHA-256 streaming.
func ChecksumFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// CleanupTmp removes *.tmp-* files under base.
func CleanupTmp(base string) (int, error) {
	removed := 0
	filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.Contains(filepath.Base(p), ".tmp") {
			if os.Remove(p) == nil {
				removed++
			}
		}
		return nil
	})
	return removed, nil
}

func FormatSize(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case n >= TB:
		return fmt.Sprintf("%.1f TB", float64(n)/float64(TB))
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
