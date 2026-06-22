package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestScanEntriesIncludesCommonDirsByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"))
	writeFile(t, filepath.Join(root, "sub", "b.txt"))
	writeFile(t, filepath.Join(root, ".git", "ignored.txt"))
	writeFile(t, filepath.Join(root, "sub", "node_modules", "ignored.txt"))

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:       root,
		TypeFilter: FilterAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{".git", ".git/ignored.txt", "a.txt", "sub", "sub/b.txt", "sub/node_modules", "sub/node_modules/ignored.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestScanEntriesIgnoresCommonDirsWhenRequested(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"))
	writeFile(t, filepath.Join(root, ".git", "ignored.txt"))
	writeFile(t, filepath.Join(root, ".terraform", "ignored.txt"))
	writeFile(t, filepath.Join(root, "node_modules", "ignored.txt"))
	writeFile(t, filepath.Join(root, "venv", "ignored.txt"))
	writeFile(t, filepath.Join(root, ".venv", "ignored.txt"))
	writeFile(t, filepath.Join(root, "__pycache__", "ignored.pyc"))
	writeFile(t, filepath.Join(root, ".tox", "ignored.txt"))
	writeFile(t, filepath.Join(root, ".cache", "ignored.txt"))

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:       root,
		TypeFilter: FilterAll,
		Ignored:    CommonIgnoredDirNames,
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{"a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestScanEntriesIgnoresCustomDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"))
	writeFile(t, filepath.Join(root, "target", "ignored.txt"))

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:       root,
		TypeFilter: FilterAll,
		Ignored:    []string{"target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{"a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestSkippableScanErrorAllowsPermissionErrors(t *testing.T) {
	err := &fs.PathError{Op: "open", Path: "blocked", Err: fs.ErrPermission}
	if !skippableScanError(err) {
		t.Fatal("permission error was not skippable")
	}
	err = &fs.PathError{Op: "open", Path: "gone", Err: fs.ErrNotExist}
	if !skippableScanError(err) {
		t.Fatal("not-exist error was not skippable")
	}
	if skippableScanError(errors.New("boom")) {
		t.Fatal("generic error was skippable")
	}
}

func TestScanEntriesMissingRootReturnsError(t *testing.T) {
	for _, followLinks := range []bool{false, true} {
		t.Run(fmt.Sprintf("follow-links-%v", followLinks), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "missing")
			entries, err := collectEntries(context.Background(), ScanOptions{
				Root:        root,
				TypeFilter:  FilterAll,
				FollowLinks: followLinks,
			})
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("err = %v, want fs.ErrNotExist", err)
			}
			if len(entries) != 0 {
				t.Fatalf("entries = %#v, want none", entries)
			}
		})
	}
}

func TestScanEntriesTypeFilters(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"))
	writeFile(t, filepath.Join(root, "dir", "b.txt"))

	tests := []struct {
		name   string
		filter TypeFilter
		want   []string
	}{
		{name: "files", filter: FilterFiles, want: []string{"a.txt", "dir/b.txt"}},
		{name: "dirs", filter: FilterDirs, want: []string{"dir"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := collectEntries(context.Background(), ScanOptions{
				Root:       root,
				TypeFilter: tt.filter,
			})
			if err != nil {
				t.Fatal(err)
			}
			sortEntries(entries, SortPath)
			if got := entryPaths(entries); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("paths = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestScanEntriesDoesNotFollowSymlinksByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "a.txt"))
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:       root,
		TypeFilter: FilterAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{"link", "real", "real/a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestScanEntriesFollowsSymlinkedDirectoriesWhenRequested(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "a.txt"))
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:        root,
		TypeFilter:  FilterAll,
		FollowLinks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{"link", "link/a.txt", "real", "real/a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestScanEntriesFollowsMultiplePathsToSameDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "a.txt"))
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:        root,
		TypeFilter:  FilterAll,
		FollowLinks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{"alias", "alias/a.txt", "real", "real/a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestScanEntriesFollowSymlinksAvoidsDirectoryCycles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "a.txt"))
	if err := os.Symlink(root, filepath.Join(root, "real", "loop")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:        root,
		TypeFilter:  FilterAll,
		FollowLinks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEntries(entries, SortPath)

	got := entryPaths(entries)
	want := []string{"real", "real/a.txt", "real/loop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestSortEntriesByMTimeOldestToLatest(t *testing.T) {
	old := time.Unix(10, 0)
	newer := time.Unix(20, 0)
	entries := []Entry{
		{Path: "new", ModTimeNS: modTimeNS(newer)},
		{Path: "old-b", ModTimeNS: modTimeNS(old)},
		{Path: "old-a", ModTimeNS: modTimeNS(old)},
	}

	sortEntries(entries, SortMTime)

	got := entryPaths(entries)
	want := []string{"old-a", "old-b", "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestScanEntriesSkipsModTimeWhenNotNeeded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"))

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:        root,
		TypeFilter:  FilterFiles,
		NeedModTime: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].ModTimeNS != 0 {
		t.Fatalf("mod time ns = %d, want zero", entries[0].ModTimeNS)
	}
}

func TestScanEntriesPopulatesModTimeWhenNeeded(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	writeFile(t, path)
	want := time.Unix(123, 0)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatal(err)
	}

	entries, err := collectEntries(context.Background(), ScanOptions{
		Root:        root,
		TypeFilter:  FilterFiles,
		NeedModTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].ModTimeNS != modTimeNS(want) {
		t.Fatalf("mod time ns = %d, want %d", entries[0].ModTimeNS, modTimeNS(want))
	}
}

func TestScanEntriesBatchesResults(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < scanBatchSize+1; i++ {
		writeFile(t, filepath.Join(root, "file-"+time.Unix(int64(i), 0).Format("150405.000000000")))
	}

	var batches, total int
	for result := range scanEntries(context.Background(), ScanOptions{
		Root:       root,
		TypeFilter: FilterFiles,
	}) {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		if len(result.Entries) == 0 {
			t.Fatal("empty scan batch")
		}
		batches++
		total += len(result.Entries)
	}
	if batches < 2 {
		t.Fatalf("batches = %d, want at least 2", batches)
	}
	if total != scanBatchSize+1 {
		t.Fatalf("entries = %d, want %d", total, scanBatchSize+1)
	}
}

func TestRelativeScanPathFast(t *testing.T) {
	root := filepath.Join("tmp", "root")
	path := filepath.Join(root, "sub", "file.txt")

	got, ok := relativeScanPathFast(root, path)
	if !ok {
		t.Fatal("relativeScanPathFast did not handle clean descendant")
	}
	want := filepath.Join("sub", "file.txt")
	if got != want {
		t.Fatalf("relative path = %q, want %q", got, want)
	}
}

func TestRelativeScanPathUnderRootUsesPrefix(t *testing.T) {
	root := filepath.Join("tmp", "root")
	prefix := filepath.Clean(root) + string(filepath.Separator)
	path := filepath.Join(root, "sub", "file.txt")

	got, err := relativeScanPathUnderRoot(root, prefix, path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join("sub", "file.txt"))
	if got != want {
		t.Fatalf("relative path = %q, want %q", got, want)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entryPaths(entries []Entry) []string {
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	return paths
}
