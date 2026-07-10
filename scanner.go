package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const scanBatchSize = 512

type EntryType int

const (
	TypeFile EntryType = iota + 1
	TypeDir
)

type Entry struct {
	Path      string
	ModTimeNS int64
	Type      EntryType
}

type TypeFilter int

const (
	FilterAll TypeFilter = iota
	FilterFiles
	FilterDirs
)

type SortMode string

const (
	SortPath  SortMode = "path"
	SortMTime SortMode = "mtime"
)

type ScanOptions struct {
	Root        string
	TypeFilter  TypeFilter
	Ignored     []string
	NeedModTime bool
	FollowLinks bool
}

type ScanResult struct {
	Entries []Entry
	Err     error
}

func scanEntries(ctx context.Context, opts ScanOptions) <-chan ScanResult {
	out := make(chan ScanResult, 16)
	go func() {
		defer close(out)
		root := opts.Root
		if root == "" {
			root = "."
		}
		rootInfo, err := os.Lstat(root)
		if err != nil {
			// A missing requested root is a user-facing input error, unlike a
			// descendant path that disappears while the scan is already running.
			if errors.Is(err, fs.ErrNotExist) {
				_ = sendScanResult(ctx, out, ScanResult{Err: err})
				return
			}
			// Permission-denied roots are treated like unreadable directories
			// encountered later in traversal: skip them instead of failing the
			// picker on systems with partially inaccessible trees.
			if skippableScanError(err) {
				return
			}
			_ = sendScanResult(ctx, out, ScanResult{Err: err})
			return
		}
		cleanRoot := filepath.Clean(root)
		rootPrefix := cleanRoot + string(filepath.Separator)
		ignored := ignoredDirSet(opts.Ignored)
		batch := make([]Entry, 0, scanBatchSize)
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			// Send immutable batches so the picker can render progressively
			// without a channel send for every filesystem entry.
			entries := make([]Entry, len(batch))
			copy(entries, batch)
			batch = batch[:0]
			return sendScanResult(ctx, out, ScanResult{Entries: entries})
		}

		addEntry := func(path string, dirent fs.DirEntry, entryType EntryType) error {
			if !typeAllowed(entryType, opts.TypeFilter) {
				return nil
			}
			rel, err := relativeScanPathUnderRoot(root, rootPrefix, path)
			if err != nil {
				if err := flush(); err != nil {
					return err
				}
				return sendScanResult(ctx, out, ScanResult{Err: err})
			}
			entry := Entry{
				Path: rel,
				Type: entryType,
			}
			if opts.NeedModTime {
				// Mtime is only paid for when sorting needs it; in follow-link
				// mode Stat keeps the displayed type and timestamp aligned.
				info, err := scanEntryInfo(path, dirent, opts.FollowLinks)
				if err != nil {
					if skippableScanError(err) {
						return nil
					}
					if err := flush(); err != nil {
						return err
					}
					return sendScanResult(ctx, out, ScanResult{Err: err})
				}
				entry.ModTimeNS = modTimeNS(info.ModTime())
			}
			batch = append(batch, entry)
			if len(batch) >= scanBatchSize {
				return flush()
			}
			return nil
		}

		if opts.FollowLinks {
			err = walkDirFollowLinks(ctx, root, ignored, addEntry)
		} else if !rootInfo.IsDir() {
			err = nil
		} else {
			err = walkDirShallowFirst(ctx, root, ignored, addEntry)
		}
		if err == nil {
			err = flush()
		} else if !errors.Is(err, context.Canceled) {
			if flushErr := flush(); flushErr != nil {
				err = flushErr
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = sendScanResult(ctx, out, ScanResult{Err: err})
		}
	}()
	return out
}

func skippableScanError(err error) bool {
	// Files can disappear or become unreadable while the tree is being scanned,
	// and real deployments often include directories the current user cannot
	// enter. Keep those cases non-fatal; callers handle the requested root
	// separately so a typo in the root path still reports an error.
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist)
}

func ignoredDir(ignored map[string]struct{}, name string) bool {
	_, ok := ignored[name]
	return ok
}

func walkDirShallowFirst(ctx context.Context, root string, ignored map[string]struct{}, addEntry func(string, fs.DirEntry, EntryType) error) error {
	rootDir, err := openDirNoFollow(root)
	if err != nil {
		if skippableNoFollowOpenError(err) {
			return nil
		}
		return err
	}
	var walk func(*os.File, string) error
	walk = func(dir *os.File, dirPath string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(-1)
		if err != nil {
			if skippableScanError(err) {
				return nil
			}
			return err
		}
		// File.ReadDir preserves filesystem order, while the previous os.ReadDir
		// traversal sorted names. Keep discovery order stable for the empty picker.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		type childDir struct {
			name string
			path string
		}
		childDirs := make([]childDir, 0, len(entries))
		for _, dirent := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(dirPath, dirent.Name())
			entryType := TypeFile
			if dirent.IsDir() {
				if ignoredDir(ignored, dirent.Name()) {
					continue
				}
				entryType = TypeDir
				childDirs = append(childDirs, childDir{name: dirent.Name(), path: path})
			}
			if err := addEntry(path, dirent, entryType); err != nil {
				return err
			}
		}
		// Emit a directory's immediate entries before descending. This keeps
		// nearby paths available to the interactive picker while a large earlier
		// sibling is still being scanned.
		for _, child := range childDirs {
			childFile, err := openDirAtNoFollow(dir, child.name, child.path)
			if err != nil {
				// A directory may disappear or be replaced after ReadDir. O_NOFOLLOW
				// makes that race a skipped snapshot entry instead of silently walking
				// through a new symlink target.
				if skippableNoFollowOpenError(err) {
					continue
				}
				return err
			}
			walkErr := walk(childFile, child.path)
			closeErr := childFile.Close()
			if walkErr != nil {
				return walkErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}
	walkErr := walk(rootDir, root)
	closeErr := rootDir.Close()
	if walkErr != nil {
		return walkErr
	}
	return closeErr
}

func openDirNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open directory %q: invalid file descriptor", path)
	}
	return file, nil
}

func openDirAtNoFollow(parent *os.File, name, path string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open directory %q: invalid file descriptor", path)
	}
	return file, nil
}

func skippableNoFollowOpenError(err error) bool {
	return skippableScanError(err) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)
}

func walkDirFollowLinks(ctx context.Context, root string, ignored map[string]struct{}, addEntry func(string, fs.DirEntry, EntryType) error) error {
	// filepath.WalkDir does not follow directory symlinks, so follow-link mode
	// keeps a realpath ancestor set per branch to avoid symlink cycles.
	ancestors := make(map[string]struct{})
	rootRealPath, err := filepath.EvalSymlinks(root)
	if err != nil && !skippableScanError(err) {
		return err
	}
	if rootRealPath != "" {
		ancestors[rootRealPath] = struct{}{}
	}
	var walk func(string, map[string]struct{}) error
	walk = func(dir string, ancestors map[string]struct{}) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if skippableScanError(err) {
				return nil
			}
			return err
		}
		type childDir struct {
			path      string
			ancestors map[string]struct{}
		}
		childDirs := make([]childDir, 0, len(entries))
		for _, dirent := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(dir, dirent.Name())
			entryType, traverse, err := followedEntryType(path, dirent)
			if err != nil {
				if skippableScanError(err) {
					continue
				}
				return err
			}
			if traverse && ignoredDir(ignored, dirent.Name()) {
				continue
			}
			if err := addEntry(path, dirent, entryType); err != nil {
				return err
			}
			if traverse {
				nextAncestors, cycle, err := descendSymlinkAware(ancestors, path)
				if err != nil {
					if skippableScanError(err) {
						continue
					}
					return err
				}
				if cycle {
					continue
				}
				childDirs = append(childDirs, childDir{path: path, ancestors: nextAncestors})
			}
		}
		// Keep follow-link traversal aligned with the default shallow-first
		// order while preserving per-branch realpath ancestry for cycle checks.
		for _, childDir := range childDirs {
			if err := walk(childDir.path, childDir.ancestors); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, ancestors)
}

func followedEntryType(path string, dirent fs.DirEntry) (EntryType, bool, error) {
	// Follow-link mode reports the target kind, so a symlink to a directory can
	// be displayed and traversed as a directory.
	info, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	if info.IsDir() {
		return TypeDir, true, nil
	}
	return TypeFile, false, nil
}

func scanEntryInfo(path string, dirent fs.DirEntry, followLinks bool) (fs.FileInfo, error) {
	if followLinks {
		return os.Stat(path)
	}
	return dirent.Info()
}

func descendSymlinkAware(ancestors map[string]struct{}, path string) (map[string]struct{}, bool, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, false, err
	}
	if _, ok := ancestors[realPath]; ok {
		return ancestors, true, nil
	}
	// Copy on descent so sibling branches do not incorrectly block each other
	// when they point at the same directory through different paths.
	next := make(map[string]struct{}, len(ancestors)+1)
	for ancestor := range ancestors {
		next[ancestor] = struct{}{}
	}
	next[realPath] = struct{}{}
	return next, false, nil
}

func collectEntries(ctx context.Context, opts ScanOptions) ([]Entry, error) {
	var entries []Entry
	for result := range scanEntries(ctx, opts) {
		if result.Err != nil {
			return entries, result.Err
		}
		entries = append(entries, result.Entries...)
	}
	return entries, nil
}

func sendScanResult(ctx context.Context, out chan<- ScanResult, result ScanResult) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- result:
		return nil
	}
}

func relativeScanPath(root, path string) (string, error) {
	rel, ok := relativeScanPathFast(root, path)
	if !ok {
		var err error
		rel, err = filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(rel), nil
}

func relativeScanPathUnderRoot(root, rootPrefix, path string) (string, error) {
	if strings.HasPrefix(path, rootPrefix) {
		return filepath.ToSlash(path[len(rootPrefix):]), nil
	}
	return relativeScanPath(root, path)
}

func relativeScanPathFast(root, path string) (string, bool) {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if cleanPath == cleanRoot {
		return "", true
	}
	prefix := cleanRoot + string(filepath.Separator)
	if strings.HasPrefix(cleanPath, prefix) {
		return cleanPath[len(prefix):], true
	}
	return "", false
}

func typeAllowed(entryType EntryType, filter TypeFilter) bool {
	switch filter {
	case FilterFiles:
		return entryType == TypeFile
	case FilterDirs:
		return entryType == TypeDir
	default:
		return true
	}
}

func sortEntries(entries []Entry, mode SortMode) {
	switch mode {
	case SortMTime:
		// Non-interactive mtime sort is oldest-first for stable pipeline use;
		// the interactive Ctrl-Space recent sort is the newest-first view.
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].ModTimeNS == entries[j].ModTimeNS {
				return entries[i].Path < entries[j].Path
			}
			return entries[i].ModTimeNS < entries[j].ModTimeNS
		})
	default:
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Path < entries[j].Path
		})
	}
}

func modTimeNS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
