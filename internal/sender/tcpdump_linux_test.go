package sender

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledTCPDumpSelection(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private", "tcpdump")
	system := filepath.Join(dir, "tcpdump")
	if err := os.Mkdir(filepath.Dir(private), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{private, system} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	path, err := findTCPDump(private)
	if err != nil || path != private {
		t.Fatal("private binary must take priority", path, err)
	}
	if err := os.Chmod(private, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = findTCPDump(private); err == nil {
		t.Fatal("damaged private binary must not silently fall back")
	}
	if err := os.Remove(private); err != nil {
		t.Fatal(err)
	}
	path, err = findTCPDump(private)
	if err != nil || path != system {
		t.Fatal("missing private binary should use system tcpdump", path, err)
	}
	if err := os.Remove(system); err != nil {
		t.Fatal(err)
	}
	if _, err = findTCPDump(private); err == nil {
		t.Fatal("missing capture executables must report an error")
	}
}
