package loginpath

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	out := []byte("Welcome!\n" + begin + "/opt/homebrew/bin:/usr/bin" + end + "\nbye\n")
	if got := Parse(out); got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("%q", got)
	}
	if Parse([]byte("no markers")) != "" || Parse([]byte(begin+"unterminated")) != "" {
		t.Fatal("garbage parsed")
	}
}

func TestMerge(t *testing.T) {
	got := Merge("/a:/b", "/b:/usr/bin:", "", "/c:/a")
	if got != strings.Join([]string{"/a", "/b", "/usr/bin", "/c"}, string(os.PathListSeparator)) {
		t.Fatalf("%q", got)
	}
}

// A fake login shell with banners; a slow one; a missing one.
func TestFromShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login shells")
	}
	dir := t.TempDir()
	sh := filepath.Join(dir, "fakesh")
	// called as "fakesh -ilc <script>": banner, the PATH login files would
	// set, then the script, then more noise
	if err := os.WriteFile(sh, []byte("#!/bin/sh\necho 'Last login: today'\nPATH=/from/login:/usr/bin\nexport PATH\neval \"$2\"\necho trailing\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fromShell(sh); got != "/from/login:/usr/bin" {
		t.Fatalf("%q", got)
	}
	slow := filepath.Join(dir, "slowsh")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Timeout
	Timeout = 300 * time.Millisecond
	defer func() { Timeout = old }()
	start := time.Now()
	if got := fromShell(slow); got != "" || time.Since(start) > 3*time.Second {
		t.Fatalf("slow shell: %q after %v", got, time.Since(start))
	}
	if fromShell(filepath.Join(dir, "missing")) != "" {
		t.Fatal("missing shell")
	}
}

// What a Finder-launched app gets fixed.
func TestFixFromMinimalPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login shells")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := filepath.Join(home, "fakesh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\nPATH=/login/bin:/usr/bin\nexport PATH\neval \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	got := filepath.SplitList(Fix())
	if len(got) == 0 || got[0] != "/login/bin" {
		t.Fatalf("login PATH first: %v", got)
	}
	has := func(d string) bool { return slices.Contains(got, d) }
	if !has("/bin") || !has(filepath.Join(home, ".local", "bin")) || has(filepath.Join(home, ".bun", "bin")) {
		t.Fatalf("merged: %v", got)
	}
	if os.Getenv("PATH") != strings.Join(got, string(os.PathListSeparator)) {
		t.Fatal("PATH not set for this process")
	}
}
