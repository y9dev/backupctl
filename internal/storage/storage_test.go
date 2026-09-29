package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteRenameChecksum(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "sub", "2026-09-29_13-00-00.tar.zst")
	aw, err := NewAtomicWriter(final)
	if err != nil {
		t.Fatal(err)
	}
	aw.Write([]byte("hello backup"))
	sum, size, err := aw.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if size != 12 {
		t.Fatalf("size %d", size)
	}
	if sum == "" {
		t.Fatal("empty checksum")
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatal(err)
	}
	got, n, err := ChecksumFile(final)
	if err != nil || got != sum || n != size {
		t.Fatalf("checksum mismatch %v %v", got, sum)
	}
}

func TestSanitize(t *testing.T) {
	if Sanitize("../../etc/passwd") == "../../etc/passwd" {
		t.Fatal("not sanitized")
	}
	if Sanitize("") == "" {
		t.Fatal("empty should map to unnamed")
	}
}
