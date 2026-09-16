package sqlitepath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireSymlink skips when the OS refuses to create symbolic links (common
// on Windows without the symlink privilege).
func requireSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink %s -> %s: %v", link, target, err)
	}
}

func TestW9ARequirePhysicalRoot(t *testing.T) {
	dir := t.TempDir()

	if err := RequirePhysicalRoot(filepath.Join(dir, "missing"), "usage root"); err != nil {
		t.Fatalf("missing root must be allowed, got %v", err)
	}
	if err := RequirePhysicalRoot(dir, "usage root"); err != nil {
		t.Fatalf("physical directory must pass, got %v", err)
	}

	file := filepath.Join(dir, "afile.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RequirePhysicalRoot(file, "usage root")
	if err == nil || !strings.Contains(err.Error(), "must be a directory") || !strings.Contains(err.Error(), "usage root") {
		t.Fatalf("plain file must fail with directory requirement, got %v", err)
	}

	link := filepath.Join(dir, "linkdir")
	requireSymlink(t, dir, link)
	err = RequirePhysicalRoot(link, "usage root")
	if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("symlink root must fail, got %v", err)
	}
}

func TestW9AListUsageShardFiles(t *testing.T) {
	dir := t.TempDir()
	shard := filepath.Join(dir, "2024", "05", "06", "usage-20240506-s1.sqlite3")
	if err := os.MkdirAll(filepath.Dir(shard), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shard, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-matching neighbours must be ignored.
	noise := []string{
		filepath.Join(dir, "2024", "05", "06", "usage-20240506-s1.sqlite3-wal"),
		filepath.Join(dir, "2024", "05", "06", "other.txt"),
		filepath.Join(dir, "2024", "05", "07", "usage-20240507-s2.sqlite3.tmp"),
		filepath.Join(dir, "notes", "usage-20240506-s1.sqlite3"),
	}
	for _, n := range noise {
		if err := os.MkdirAll(filepath.Dir(n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(n, []byte("n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ListUsageShardFiles(dir)
	if err != nil {
		t.Fatalf("ListUsageShardFiles error: %v", err)
	}
	if len(got) != 1 || filepath.ToSlash(got[0]) != filepath.ToSlash(shard) {
		t.Fatalf("expected exactly the matching shard, got %v", got)
	}

	// Missing root yields an empty result without error.
	missing, err := ListUsageShardFiles(filepath.Join(dir, "nope"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing root: got %v err %v, want empty nil", missing, err)
	}
}

func TestW9AListUsageShardFilesRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	dayDir := filepath.Join(dir, "2024", "05", "06")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real.sqlite3")
	if err := os.WriteFile(target, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dayDir, "usage-20240506-s9.sqlite3")
	requireSymlink(t, target, link)
	_, err := ListUsageShardFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("symlink shard must fail, got %v", err)
	}
}

func TestW9AListUsageShardFilesDateMismatch(t *testing.T) {
	dir := t.TempDir()
	shard := filepath.Join(dir, "2024", "05", "07", "usage-20240506-s1.sqlite3")
	if err := os.MkdirAll(filepath.Dir(shard), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shard, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ListUsageShardFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "does not match filename") {
		t.Fatalf("date mismatch must fail, got %v", err)
	}
}

func TestW9ASameFile(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "left.db")
	right := filepath.Join(dir, "right.db")
	if err := os.WriteFile(left, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(right, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	same, err := SameFile(left, left)
	if err != nil || !same {
		t.Fatalf("same file must be reported, got %v err %v", same, err)
	}
	same, err = SameFile(left, right)
	if err != nil || same {
		t.Fatalf("different files must not compare same, got %v err %v", same, err)
	}

	// Both paths missing but canonically equal -> same.
	same, err = SameFile(filepath.Join(dir, "missing"), filepath.Join(dir, ".", "missing"))
	if err != nil || !same {
		t.Fatalf("identical missing path must be same, got %v err %v", same, err)
	}
	// One missing, different canonical path -> not same.
	same, err = SameFile(left, filepath.Join(dir, "missing"))
	if err != nil || same {
		t.Fatalf("existing vs missing must differ, got %v err %v", same, err)
	}

	// Same file through a hard link is also detected via os.SameFile.
	hard := filepath.Join(dir, "hard.db")
	if err := os.Link(left, hard); err == nil {
		same, err = SameFile(left, hard)
		if err != nil || !same {
			t.Fatalf("hard link must compare same, got %v err %v", same, err)
		}
	}

	// A path containing a NUL byte fails stat with an error that is not
	// ErrNotExist, which must be surfaced.
	_, err = SameFile(left, "bad\x00path")
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid path must surface non-NotExist error, got %v", err)
	}
}

func TestW9ACanonicalPath(t *testing.T) {
	if _, err := CanonicalPath(""); err == nil {
		t.Fatal("empty path must fail")
	}
	if _, err := CanonicalPath("   "); err == nil {
		t.Fatal("blank path must fail")
	}

	dir := t.TempDir()
	got, err := CanonicalPath(dir)
	if err != nil {
		t.Fatalf("existing path: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("canonical path must be absolute: %q", got)
	}

	// Missing path with existing parent resolves through the parent.
	missing := filepath.Join(dir, "missing.db")
	got, err = CanonicalPath(missing)
	if err != nil {
		t.Fatalf("missing path with parent: %v", err)
	}
	wantParent, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(wantParent) && filepath.Dir(got) != filepath.Clean(wantParent) &&
		!strings.EqualFold(filepath.Dir(got), filepath.Clean(wantParent)) {
		t.Fatalf("unexpected canonical missing path %q, want parent %q", got, wantParent)
	}

	// Several missing levels still resolve to the nearest existing ancestor.
	deep := filepath.Join(dir, "a", "b", "c.db")
	got, err = CanonicalPath(deep)
	if err != nil {
		t.Fatalf("deep missing path: %v", err)
	}
	if base := filepath.Base(got); base != "c.db" {
		t.Fatalf("suffix lost: %q", got)
	}

	// Relative path input becomes absolute.
	got, err = CanonicalPath("relative-file.db")
	if err != nil {
		t.Fatalf("relative path: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("relative input must yield absolute path: %q", got)
	}
}

func TestW9ACanonicalPathThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	requireSymlink(t, realDir, link)

	got, err := CanonicalPath(filepath.Join(link, "file.db"))
	if err != nil {
		t.Fatalf("symlinked parent: %v", err)
	}
	resolvedReal, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(filepath.Clean(resolvedReal))) {
		t.Fatalf("canonical path %q must resolve under %q", got, resolvedReal)
	}
}

func TestW9APathWithin(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "child", "leaf.db")
	sibling := filepath.Join(dir, "sibling.db")

	root := filepath.Dir(filepath.Dir(inside)) // dir
	ok, err := PathWithin(root, inside)
	if err != nil || !ok {
		t.Fatalf("inside path must be within, got %v err %v", ok, err)
	}
	ok, err = PathWithin(dir, dir)
	if err != nil || !ok {
		t.Fatalf("root itself must be within, got %v err %v", ok, err)
	}
	ok, err = PathWithin(filepath.Join(dir, "child"), filepath.Join(dir, "child", "leaf.db"))
	if err != nil || !ok {
		t.Fatalf("direct child must be within, got %v err %v", ok, err)
	}
	ok, err = PathWithin(filepath.Join(dir, "child"), sibling)
	if err != nil || ok {
		t.Fatalf("sibling must be outside, got %v err %v", ok, err)
	}
	ok, err = PathWithin(filepath.Join(dir, "child"), dir)
	if err != nil || ok {
		t.Fatalf("parent must be outside, got %v err %v", ok, err)
	}
	if _, err = PathWithin(dir, ""); err == nil {
		t.Fatal("empty candidate must fail")
	}
	if _, err = PathWithin("", dir); err == nil {
		t.Fatal("empty root must fail")
	}
}

// TestW9AInvalidPathErrors covers non-NotExist stat failures (e.g. paths
// containing NUL bytes fail with EINVAL on Windows) that must be surfaced
// instead of being treated as "missing".
func TestW9AInvalidPathErrors(t *testing.T) {
	bad := "bad\x00path"

	err := RequirePhysicalRoot(bad, "usage root")
	if err == nil || errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "usage root") {
		t.Fatalf("RequirePhysicalRoot invalid path: got %v", err)
	}

	if _, err = ListUsageShardFiles(bad); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ListUsageShardFiles invalid root: got %v", err)
	}

	dir := t.TempDir()
	if _, err = SameFile(bad, dir); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SameFile invalid left: got %v", err)
	}
	if _, err = SameFile(dir, bad); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SameFile invalid right: got %v", err)
	}

	if _, err = CanonicalPath(bad); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CanonicalPath invalid path: got %v", err)
	}
}

// TestW9ABrokenSymlinkErrors covers EvalSymlinks failures on dangling links:
// Lstat sees the link itself, but resolving it must fail loudly.
func TestW9ABrokenSymlinkErrors(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	requireSymlink(t, filepath.Join(dir, "no-such-target"), link)

	if _, err := CanonicalPath(link); err == nil {
		t.Fatal("CanonicalPath through dangling link must fail")
	}
	// Missing child under dangling parent resolves the parent -> EvalSymlinks error.
	if _, err := CanonicalPath(filepath.Join(link, "child.db")); err == nil {
		t.Fatal("CanonicalPath missing child under dangling parent must fail")
	}
	// os.Stat follows the dangling link -> both sides look missing, then
	// CanonicalPath hits the dangling link and surfaces the error.
	same, err := SameFile(link, link)
	if err == nil {
		t.Fatalf("SameFile dangling link must fail, got same=%v", same)
	}
}

// TestW9APathWithinDifferentVolumes covers filepath.Rel failures for paths on
// different volumes (Windows drives).
func TestW9APathWithinDifferentVolumes(t *testing.T) {
	var volumes []string
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Lstat(root); err == nil {
			volumes = append(volumes, root)
		}
	}
	if len(volumes) < 2 {
		t.Skip("need at least two existing volumes to exercise cross-volume Rel error")
	}
	if _, err := PathWithin(volumes[0], filepath.Join(volumes[1], "x")); err == nil {
		t.Fatal("cross-volume PathWithin must fail")
	}
}

// TestW9AListUsageShardFilesWalkError covers the WalkDir callback error path
// for a walk error other than root-not-exist.
func TestW9AListUsageShardFilesWalkError(t *testing.T) {
	if _, err := ListUsageShardFiles("bad\x00walk"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid walk root must surface error, got %v", err)
	}
}

// TestW9ASameFilePartialCanonicalFailure covers the case where the first
// CanonicalPath succeeds but the second one fails (dangling link on one side).
func TestW9ASameFilePartialCanonicalFailure(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	requireSymlink(t, filepath.Join(dir, "no-such-target"), link)

	// os.Stat on the dangling link reports NotNotExist-style missing, while
	// CanonicalPath can still resolve the plain missing path but fails on
	// the dangling link.
	_, err := SameFile(filepath.Join(dir, "missing"), link)
	if err == nil {
		t.Fatal("SameFile with dangling right side must surface canonical error")
	}
}
