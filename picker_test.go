package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPickerModelFiltersAndKeepsSelectionInRange(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "src/main.go"})
	model.addEntry(Entry{Path: "README.md"})
	model.move(10)
	model.appendRune('s')
	model.appendRune('m')
	model.applyQuery()

	if len(model.matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(model.matches))
	}
	if model.selected != 0 {
		t.Fatalf("selected = %d, want 0", model.selected)
	}
	if got := model.matches[0].Entry.Path; got != "src/main.go" {
		t.Fatalf("match = %q, want src/main.go", got)
	}
}

func TestPickerModelBackspaceRestoresMatches(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})
	model.appendRune('z')
	model.applyQuery()
	if len(model.matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(model.matches))
	}

	model.backspace()
	model.applyQuery()
	if len(model.matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(model.matches))
	}
}

func TestPickerModelQueryEditResetsSelection(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})
	model.addEntry(Entry{Path: "gamma"})
	model.move(2)

	model.appendRune('b')

	if model.selected != 0 {
		t.Fatalf("selected = %d, want 0", model.selected)
	}
	if !model.queryDirty {
		t.Fatal("expected query to be dirty after typing")
	}
	model.applyQuery()
	entry, ok := model.selectedEntry()
	if !ok {
		t.Fatal("expected selected entry")
	}
	if entry.Path != "beta" {
		t.Fatalf("selected path = %q, want beta", entry.Path)
	}
}

func TestPickerModelQueryEditDoesNotRefreshUntilApplied(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})

	model.appendRune('b')

	if got := string(model.query); got != "b" {
		t.Fatalf("query = %q, want b", got)
	}
	if got := model.appliedQuery; got != "" {
		t.Fatalf("applied query = %q, want empty", got)
	}
	if len(model.matches) != 2 {
		t.Fatalf("matches = %d, want stale unfiltered matches", len(model.matches))
	}

	model.applyQuery()
	if got := model.appliedQuery; got != "b" {
		t.Fatalf("applied query = %q, want b", got)
	}
	if model.queryDirty {
		t.Fatal("query still dirty after apply")
	}
	if len(model.matches) != 1 || model.matches[0].Entry.Path != "beta" {
		t.Fatalf("matches after apply = %#v, want beta only", matchPaths(model.matches))
	}
}

func TestPickerModelEmptyInteractiveQueryKeepsDiscoveryOrder(t *testing.T) {
	model := newPickerModel(SortPath)

	model.addEntries([]Entry{{Path: "zeta"}, {Path: "alpha"}})

	if got := matchPaths(model.matches); !equalStrings(got, []string{"zeta", "alpha"}) {
		t.Fatalf("matches = %#v, want discovery order", got)
	}
}

func TestPickerModelEmptyLatestQueryKeepsMTimeSort(t *testing.T) {
	model := newPickerModel(SortMTime)

	model.addEntries([]Entry{
		{Path: "new", ModTimeNS: modTimeNS(time.Unix(2, 0))},
		{Path: "old", ModTimeNS: modTimeNS(time.Unix(1, 0))},
	})

	if got := matchPaths(model.matches); !equalStrings(got, []string{"old", "new"}) {
		t.Fatalf("matches = %#v, want mtime order", got)
	}
}

func TestPickerModelQueryDebounceDelay(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = true
	model.entries = make([]Entry, queryDebounceImmediateThreshold)
	model.refresh()
	if got := model.queryDebounceDelayFor("a"); got != 0 {
		t.Fatalf("small scanning debounce = %v, want 0", got)
	}

	model.scanning = false
	model.entries = make([]Entry, queryDebounceImmediateThreshold)
	model.refresh()
	if got := model.queryDebounceDelayFor("a"); got != 0 {
		t.Fatalf("immediate debounce = %v, want 0", got)
	}

	model.entries = make([]Entry, queryDebounceLargeThreshold-1)
	model.refresh()
	if got := model.queryDebounceDelayFor("a"); got != queryDebounceSmall {
		t.Fatalf("small complete debounce = %v, want %v", got, queryDebounceSmall)
	}

	model.entries = make([]Entry, queryDebounceLargeThreshold)
	model.refresh()
	if got := model.queryDebounceDelayFor("a"); got != queryDebounceLarge {
		t.Fatalf("large complete debounce = %v, want %v", got, queryDebounceLarge)
	}
}

func TestPickerModelQueryDebounceUsesNarrowedCandidateCount(t *testing.T) {
	model := newPickerModel(SortPath)
	model.entries = make([]Entry, queryDebounceLargeThreshold+1)
	model.entriesVersion++
	model.fullMatches = make([]Match, queryDebounceImmediateThreshold)
	model.matches = model.fullMatches
	model.matchedEntriesVersion = model.entriesVersion
	model.appliedQuery = "a"
	model.lastEditAppend = true

	if got := model.queryDebounceDelayFor("ab"); got != 0 {
		t.Fatalf("narrow immediate debounce = %v, want 0", got)
	}

	model.fullMatches = make([]Match, queryDebounceImmediateThreshold+1)
	model.matches = model.fullMatches
	model.lastEditAppend = true
	if got := model.queryDebounceDelayFor("ab"); got != queryDebounceSmall {
		t.Fatalf("narrow small debounce = %v, want %v", got, queryDebounceSmall)
	}

	model.fullMatches = make([]Match, queryDebounceLargeThreshold)
	model.matches = model.fullMatches
	model.lastEditAppend = true
	if got := model.queryDebounceDelayFor("ab"); got != queryDebounceLarge {
		t.Fatalf("narrow large debounce = %v, want %v", got, queryDebounceLarge)
	}
}

func TestPickerModelQueryDebounceUsesNarrowedCandidateCountAfterSpaceToken(t *testing.T) {
	model := newPickerModel(SortPath)
	model.entries = make([]Entry, queryDebounceLargeThreshold+1)
	model.entriesVersion++
	model.fullMatches = make([]Match, queryDebounceImmediateThreshold)
	model.matches = model.fullMatches
	model.matchedEntriesVersion = model.entriesVersion
	model.appliedQuery = "alpha beta"
	model.lastEditAppend = true

	if got := model.queryDebounceDelayFor("alpha beta .dat"); got != 0 {
		t.Fatalf("space-token narrow debounce = %v, want 0", got)
	}
}

func TestPickerModelAppendRuneInsertsAtCursor(t *testing.T) {
	model := newPickerModel(SortPath)
	model.appendRune('f')
	model.appendRune('o')
	model.moveQueryCursor(-1)

	model.appendRune('x')

	if got, want := string(model.query), "fxo"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := model.queryCursor, 2; got != want {
		t.Fatalf("query cursor = %d, want %d", got, want)
	}
	if model.lastEditAppend {
		t.Fatal("middle insert marked as append")
	}
}

func TestPickerModelBackspaceDeletesBeforeCursor(t *testing.T) {
	model := newPickerModel(SortPath)
	model.query = []rune("foo")
	model.queryCursor = 2

	model.backspace()

	if got, want := string(model.query), "fo"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := model.queryCursor, 1; got != want {
		t.Fatalf("query cursor = %d, want %d", got, want)
	}
}

func TestPickerModelMoveQueryCursorClamps(t *testing.T) {
	model := newPickerModel(SortPath)
	model.query = []rune("foo")
	model.queryCursor = 1

	model.moveQueryCursor(-10)
	if got := model.queryCursor; got != 0 {
		t.Fatalf("left-clamped query cursor = %d, want 0", got)
	}

	model.moveQueryCursor(10)
	if got := model.queryCursor; got != len(model.query) {
		t.Fatalf("right-clamped query cursor = %d, want %d", got, len(model.query))
	}
}

func TestPickerModelMovesQueryCursorHomeAndEnd(t *testing.T) {
	model := newPickerModel(SortPath)
	model.query = []rune("foo")
	model.queryCursor = 1

	model.moveQueryCursorEnd()
	if got := model.queryCursor; got != len(model.query) {
		t.Fatalf("end query cursor = %d, want %d", got, len(model.query))
	}

	model.moveQueryCursorHome()
	if got := model.queryCursor; got != 0 {
		t.Fatalf("home query cursor = %d, want 0", got)
	}
}

func TestPickerModelClearQueryClearsWholeLine(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})
	model.query = []rune("alp")
	model.queryCursor = 2
	model.applyQuery()

	model.clearQuery()

	if got := string(model.query); got != "" {
		t.Fatalf("query = %q, want empty", got)
	}
	if model.queryCursor != 0 {
		t.Fatalf("query cursor = %d, want 0", model.queryCursor)
	}
	if !model.queryDirty {
		t.Fatal("queryDirty = false, want true")
	}
	if model.selected != 0 || model.offset != 0 {
		t.Fatalf("selected/offset = %d/%d, want 0/0", model.selected, model.offset)
	}
}

func TestPickerModelMiddleEditDisablesNarrowing(t *testing.T) {
	model := newPickerModel(SortPath)
	model.entries = make([]Entry, queryDebounceLargeThreshold+1)
	model.entriesVersion++
	model.fullMatches = make([]Match, queryDebounceImmediateThreshold)
	model.matches = model.fullMatches
	model.matchedEntriesVersion = model.entriesVersion
	model.appliedQuery = "ab"
	model.query = []rune("ab")
	model.queryCursor = 1

	model.appendRune('x')

	if got := model.queryDebounceDelayFor("axb"); got != queryDebounceLarge {
		t.Fatalf("middle-edit debounce = %v, want %v", got, queryDebounceLarge)
	}
}

func TestPickerModelAppendAtEndKeepsNarrowing(t *testing.T) {
	model := newPickerModel(SortPath)
	model.entries = make([]Entry, queryDebounceLargeThreshold+1)
	model.entriesVersion++
	model.fullMatches = make([]Match, queryDebounceImmediateThreshold)
	model.matches = model.fullMatches
	model.matchedEntriesVersion = model.entriesVersion
	model.appliedQuery = "a"
	model.query = []rune("a")
	model.queryCursor = len(model.query)

	model.appendRune('b')

	if got := model.queryDebounceDelayFor("ab"); got != 0 {
		t.Fatalf("append-at-end debounce = %v, want 0", got)
	}
}

func TestPickerModelExtendingQueryNarrowsExistingMatches(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})
	model.addEntry(Entry{Path: "bravo"})
	model.appendRune('b')
	model.applyQuery()
	if got := matchPaths(model.matches); !equalStrings(got, []string{"beta", "bravo"}) {
		t.Fatalf("matches = %#v, want beta/bravo", got)
	}

	model.appendRune('e')
	model.applyQuery()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"beta"}) {
		t.Fatalf("matches = %#v, want beta", got)
	}
	if model.matchedEntriesVersion != model.entriesVersion {
		t.Fatalf("matched version = %d, want entries version %d", model.matchedEntriesVersion, model.entriesVersion)
	}
}

func TestPickerModelDottedNumericVersionStaysReachableWhileTyping(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntries(steamDeckImageEntries())

	for _, r := range "385" {
		model.appendRune(r)
		model.applyQuery()
	}

	got := matchPaths(model.matches)
	want := []string{"steamdeck-20260520.100-3.8.5.img"}
	if !equalStrings(got, want) {
		t.Fatalf("matches after incremental 385 = %#v, want %#v", got, want)
	}
}

func TestPickerModelAppendingSpaceTokenNarrowsExistingMatches(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "fixtures/alpha/beta/item.dat"})
	model.addEntry(Entry{Path: "fixtures/alpha/beta/readme.txt"})
	model.addEntry(Entry{Path: "fixtures/other/item.dat"})
	model.query = []rune("alpha beta")
	model.applyQuery()
	if got := matchPaths(model.matches); !equalStrings(got, []string{
		"fixtures/alpha/beta/item.dat",
		"fixtures/alpha/beta/readme.txt",
	}) {
		t.Fatalf("matches = %#v, want alpha beta entries", got)
	}

	model.query = []rune("alpha beta .dat")
	model.applyQuery()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"fixtures/alpha/beta/item.dat"}) {
		t.Fatalf("matches = %#v, want narrowed dat entry", got)
	}
}

func TestPickerModelKeepsFullMatchesBehindEffectiveWindow(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntries(strongAndWeakWindowEntries(effectiveMixedWindowMatches + 30))
	model.query = []rune("alpha beta")
	model.queryCursor = len(model.query)
	model.applyQuery()

	if got := len(model.fullMatches); got != effectiveMixedWindowMatches+30 {
		t.Fatalf("full matches = %d, want all %d", got, effectiveMixedWindowMatches+30)
	}
	if got := len(model.matches); got != effectiveStrongWindowMatches {
		t.Fatalf("effective matches = %d, want strong window %d", got, effectiveStrongWindowMatches)
	}

	hidden := "fixtures/a-l-p-h-a/b-e-t-a/hidden-002.dat"
	model.query = []rune("alpha beta hidden-002")
	model.queryCursor = len(model.query)
	model.lastEditAppend = true
	model.applyQuery()

	if got := matchPaths(model.matches); !equalStrings(got, []string{hidden}) {
		t.Fatalf("matches after narrowing = %#v, want hidden candidate recovered", got)
	}
}

func TestPickerModelEffectiveWindowKeepsMixedResultSetLarger(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	var entries []Entry
	for i := 0; i < effectiveMixedWindowMatches+30; i++ {
		entries = append(entries, Entry{Path: "fixtures/a-l-p-h-a/b-e-t-a/weak-" + threeDigitString(i) + ".dat"})
	}
	model.addEntries(entries)
	model.query = []rune("alpha beta")
	model.queryCursor = len(model.query)
	model.applyQuery()

	if got := len(model.fullMatches); got != effectiveMixedWindowMatches+30 {
		t.Fatalf("full matches = %d, want all %d", got, effectiveMixedWindowMatches+30)
	}
	if got := len(model.matches); got != effectiveMixedWindowMatches {
		t.Fatalf("effective matches = %d, want mixed window %d", got, effectiveMixedWindowMatches)
	}
}

func TestPickerModelEffectiveWindowDoesNotLimitSingleTokenQuery(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	for i := 0; i < effectiveMixedWindowMatches+30; i++ {
		model.addEntry(Entry{Path: "fixtures/alpha-" + threeDigitString(i) + ".dat"})
	}
	model.query = []rune("alpha")
	model.queryCursor = len(model.query)
	model.applyQuery()

	if got := len(model.matches); got != effectiveMixedWindowMatches+30 {
		t.Fatalf("single-token matches = %d, want all %d", got, effectiveMixedWindowMatches+30)
	}
}

func TestPickerModelEffectiveWindowForEpisodeLikeSearch(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntries(episodeLikeWindowEntries())
	model.query = []rune("catadone alpha beta 1080p 10")
	model.queryCursor = len(model.query)
	model.applyQuery()

	got := matchPaths(model.matches)
	if len(got) != effectiveStrongWindowMatches {
		t.Fatalf("effective matches = %d, want strong window %d", len(got), effectiveStrongWindowMatches)
	}
	wantEpisode10 := "synthetic/catalog/done/_pack/Alpha Beta Signal S1/Alpha Beta Signal - 10 (BD 1080p).mkv"
	if !containsString(got, wantEpisode10) {
		t.Fatalf("effective matches missing episode 10; got %#v", got)
	}
	for _, path := range got {
		if strings.Contains(path, "Gamma Delta") {
			t.Fatalf("poor quality-string match leaked into effective window: %q", path)
		}
	}
}

func TestPickerModelSortCurrentMatchesNewest(t *testing.T) {
	root := t.TempDir()
	writeTestFileWithModTime(t, root, "old.jpg", time.Unix(1, 0))
	writeTestFileWithModTime(t, root, "new.jpg", time.Unix(3, 0))
	writeTestFileWithModTime(t, root, "mid.jpg", time.Unix(2, 0))
	writeTestDirWithModTime(t, root, "new-dir", time.Unix(4, 0))

	model := newPickerModel(SortPath)
	model.root = root
	model.matches = []Match{
		{Entry: Entry{Path: "old.jpg", Type: TypeFile}},
		{Entry: Entry{Path: "new-dir", Type: TypeDir}},
		{Entry: Entry{Path: "new.jpg", Type: TypeFile}},
		{Entry: Entry{Path: "mid.jpg", Type: TypeFile}},
	}
	model.selected = 2
	model.offset = 2

	model.sortCurrentMatchesNewest()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"new.jpg", "mid.jpg", "old.jpg", "new-dir"}) {
		t.Fatalf("recent matches = %#v, want files newest first and dirs last", got)
	}
	if !model.recentSortActive {
		t.Fatal("recent sort was not marked active")
	}
	if _, ok := model.mtimeCache["new-dir"]; ok {
		t.Fatal("directory was statted unexpectedly")
	}
	if model.selected != 0 || model.offset != 0 {
		t.Fatalf("selection/offset = %d/%d, want reset to 0/0", model.selected, model.offset)
	}
}

func TestPickerModelSortCurrentMatchesStatsAllFullMatches(t *testing.T) {
	root := t.TempDir()
	writeTestFileWithModTime(t, root, "visible.jpg", time.Unix(2, 0))
	writeTestFileWithModTime(t, root, "hidden.jpg", time.Unix(3, 0))

	model := newPickerModel(SortPath)
	model.root = root
	model.fullMatches = []Match{
		{Entry: Entry{Path: "visible.jpg"}},
		{Entry: Entry{Path: "hidden.jpg"}},
	}
	model.matches = model.fullMatches[:1]

	model.sortCurrentMatchesNewest()

	if _, ok := model.mtimeCache["visible.jpg"]; !ok {
		t.Fatal("visible match mtime was not cached")
	}
	if _, ok := model.mtimeCache["hidden.jpg"]; !ok {
		t.Fatal("hidden full match mtime was not cached")
	}
	if got := matchPaths(model.matches); !equalStrings(got, []string{"hidden.jpg", "visible.jpg"}) {
		t.Fatalf("recent matches = %#v, want full match set newest first", got)
	}
}

func TestPickerModelSortCurrentMatchesUsesFullMatchesBeyondEffectiveWindow(t *testing.T) {
	model := newPickerModel(SortPath)
	model.appliedQuery = "camera .jpg"
	model.fullMatches = make([]Match, 0, effectiveStrongWindowMatches+1)
	for i := 0; i < effectiveStrongWindowMatches; i++ {
		model.fullMatches = append(model.fullMatches, Match{
			Entry: Entry{
				Path:      "Camera/20210429_" + threeDigitString(i) + ".jpg",
				Type:      TypeFile,
				ModTimeNS: int64(i + 1),
			},
		})
	}
	newest := Match{
		Entry: Entry{
			Path:      "Camera/20260608_114105.jpg",
			Type:      TypeFile,
			ModTimeNS: int64(effectiveStrongWindowMatches + 100),
		},
	}
	model.fullMatches = append(model.fullMatches, newest)
	model.matches = model.fullMatches[:effectiveStrongWindowMatches]

	model.sortCurrentMatchesNewest()

	if got := model.matches[0].Entry.Path; got != newest.Entry.Path {
		t.Fatalf("first recent match = %q, want newest full match outside effective window", got)
	}
	if got := len(model.matches); got != len(model.fullMatches) {
		t.Fatalf("recent matches = %d, want all %d full matches", got, len(model.fullMatches))
	}
	if got := model.statusLine(); strings.Contains(got, "showing top") {
		t.Fatalf("status line = %q, want all full matches exposed after recent sort", got)
	}
	if got := model.fullMatches[len(model.fullMatches)-1].Entry.Path; got != newest.Entry.Path {
		t.Fatalf("full match ranking was reordered; last = %q, want %q", got, newest.Entry.Path)
	}
}

func TestPickerModelSortCurrentMatchesUsesCachedMTime(t *testing.T) {
	root := t.TempDir()
	writeTestFileWithModTime(t, root, "a.jpg", time.Unix(1, 0))
	writeTestFileWithModTime(t, root, "b.jpg", time.Unix(2, 0))

	model := newPickerModel(SortPath)
	model.root = root
	model.matches = []Match{
		{Entry: Entry{Path: "a.jpg"}},
		{Entry: Entry{Path: "b.jpg"}},
	}
	model.sortCurrentMatchesNewest()
	writeTestFileWithModTime(t, root, "a.jpg", time.Unix(5, 0))
	writeTestFileWithModTime(t, root, "b.jpg", time.Unix(1, 0))

	model.sortCurrentMatchesNewest()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"b.jpg", "a.jpg"}) {
		t.Fatalf("recent matches = %#v, want cached order", got)
	}
}

func TestPickerModelTypingResetsRecentSort(t *testing.T) {
	model := newPickerModel(SortPath)
	model.fullMatches = []Match{
		{Entry: Entry{Path: "normal-first"}},
		{Entry: Entry{Path: "normal-second"}},
	}
	model.matches = []Match{
		{Entry: Entry{Path: "normal-second", ModTimeNS: 2}},
		{Entry: Entry{Path: "normal-first", ModTimeNS: 1}},
	}
	model.sortCurrentMatchesNewest()

	model.appendRune('x')

	if model.recentSortActive {
		t.Fatal("typing did not reset recent sort")
	}
	if got := matchPaths(model.matches); !equalStrings(got, []string{"normal-first", "normal-second"}) {
		t.Fatalf("matches = %#v, want normal order restored", got)
	}
}

func TestPickEntrySortRecentAppliesPendingQuery(t *testing.T) {
	root := t.TempDir()
	writeTestFileWithModTime(t, root, "alpha-old.jpg", time.Unix(1, 0))
	writeTestFileWithModTime(t, root, "alpha-new.jpg", time.Unix(2, 0))
	writeTestFileWithModTime(t, root, "beta-new.jpg", time.Unix(3, 0))

	model := newPickerModel(SortPath)
	model.root = root
	model.scanning = false
	model.addEntries([]Entry{
		{Path: "alpha-old.jpg", Type: TypeFile},
		{Path: "alpha-new.jpg", Type: TypeFile},
		{Path: "beta-new.jpg", Type: TypeFile},
	})
	model.query = []rune("alpha")
	model.queryCursor = len(model.query)
	model.queryDirty = true
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent, 2)
	keyCh <- keyEvent{kind: keySortRecent}
	keyCh <- keyEvent{kind: keyEnter}

	var stderr bytes.Buffer
	entry, err := pickEntry(context.Background(), model, scanCh, keyCh, &stderr, 80, pickerThemeForColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != "alpha-new.jpg" {
		t.Fatalf("selected path = %q, want newest alpha match", entry.Path)
	}
	if _, ok := model.mtimeCache["beta-new.jpg"]; ok {
		t.Fatal("unmatched beta path was statted unexpectedly")
	}
}

func TestPickEntrySortRecentRefreshesStaleQuerySnapshot(t *testing.T) {
	root := t.TempDir()
	writeTestFileWithModTime(t, root, "camera-old.jpg", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	writeTestFileWithModTime(t, root, "camera-new.jpg", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	model := newPickerModel(SortPath)
	model.root = root
	model.scanning = true
	model.addEntry(Entry{Path: "camera-old.jpg", Type: TypeFile})
	model.query = []rune("camera .jpg")
	model.queryCursor = len(model.query)
	model.applyQuery()

	scanCh := make(chan ScanResult, 1)
	keyCh := make(chan keyEvent)
	snapshots := make(chan pickerSnapshot, 20)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, snapshotRenderer(model, snapshots), 0, fixedQueryDebounce(time.Hour))
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 1 &&
			snapshot.matchedEntriesVersion == 1 &&
			equalStrings(snapshot.paths, []string{"camera-old.jpg"})
	})
	scanCh <- ScanResult{Entries: []Entry{{Path: "camera-new.jpg", Type: TypeFile}}}
	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 2 &&
			snapshot.matchedEntriesVersion == 1 &&
			equalStrings(snapshot.paths, []string{"camera-old.jpg"})
	})

	keyCh <- keyEvent{kind: keySortRecent}
	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 2 &&
			snapshot.matchedEntriesVersion == 2 &&
			equalStrings(snapshot.paths, []string{"camera-new.jpg", "camera-old.jpg"})
	})
	keyCh <- keyEvent{kind: keyEnter}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "camera-new.jpg" {
			t.Fatalf("selected path = %q, want newest refreshed camera match", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refreshed recent selection")
	}
}

func TestPickEntrySortRecentEnterSelectsVisibleSnapshotDuringStaleRefresh(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.query = []rune("camera .jpg")
	model.queryCursor = len(model.query)
	model.appliedQuery = string(model.query)
	model.entries = []Entry{
		{Path: "camera-old.jpg", Type: TypeFile, ModTimeNS: modTimeNS(time.Unix(1, 0))},
		{Path: "camera-new.jpg", Type: TypeFile, ModTimeNS: modTimeNS(time.Unix(2, 0))},
	}
	model.entriesVersion = 2
	model.fullMatches = []Match{{Entry: model.entries[0]}}
	model.matches = model.fullMatches
	model.matchedEntriesVersion = 1

	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	started := make(chan queryJob, 1)
	canceled := make(chan struct{}, 1)
	ranker := func(ctx context.Context, job queryJob) ([]Match, bool) {
		started <- job
		<-ctx.Done()
		canceled <- struct{}{}
		return nil, false
	}
	rendered := make(chan struct{}, 20)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour), ranker)
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keySortRecent}
	job := waitForQueryJob(t, started)
	if job.query != "camera .jpg" || job.entriesVersion != 2 {
		t.Fatalf("job = query %q version %d, want camera .jpg/version 2", job.query, job.entriesVersion)
	}
	keyCh <- keyEvent{kind: keyEnter}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "camera-old.jpg" {
			t.Fatalf("selected path = %q, want current visible camera match", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for current selection")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale refresh cancellation")
	}
}

func TestPickEntrySortRecentRefreshesAfterRecentSortActive(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.query = []rune("camera .jpg")
	model.queryCursor = len(model.query)
	model.appliedQuery = string(model.query)
	model.entries = []Entry{
		{Path: "camera-old.jpg", Type: TypeFile, ModTimeNS: modTimeNS(time.Unix(1, 0))},
		{Path: "camera-new.jpg", Type: TypeFile, ModTimeNS: modTimeNS(time.Unix(2, 0))},
	}
	model.entriesVersion = 2
	model.fullMatches = []Match{{Entry: model.entries[0]}}
	model.matches = model.fullMatches
	model.matchedEntriesVersion = 1
	model.recentSortActive = true

	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	snapshots := make(chan pickerSnapshot, 20)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, snapshotRenderer(model, snapshots), time.Hour, fixedQueryDebounce(time.Hour))
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.matchedEntriesVersion == 1 &&
			equalStrings(snapshot.paths, []string{"camera-old.jpg"})
	})
	keyCh <- keyEvent{kind: keySortRecent}
	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.matchedEntriesVersion == 2 &&
			equalStrings(snapshot.paths, []string{"camera-new.jpg", "camera-old.jpg"})
	})
	keyCh <- keyEvent{kind: keyEnter}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "camera-new.jpg" {
			t.Fatalf("selected path = %q, want newest refreshed camera match", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refreshed recent selection")
	}
	if model.matchedEntriesVersion != model.entriesVersion {
		t.Fatalf("matched version = %d, want entries version %d", model.matchedEntriesVersion, model.entriesVersion)
	}
}

func TestPickEntrySortRecentRendersSortingStatus(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntries([]Entry{
		{Path: "old.jpg", Type: TypeFile, ModTimeNS: 1},
		{Path: "new.jpg", Type: TypeFile, ModTimeNS: 2},
	})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	statuses := make(chan string, 10)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, pickerRenderer{
			full: func() {
				statuses <- model.statusText()
			},
		}, time.Hour, fixedQueryDebounce(time.Hour))
		done <- err
	}()

	waitForString(t, statuses, "ready")
	keyCh <- keyEvent{kind: keySortRecent}
	waitForString(t, statuses, "sorting newest")

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryClearQueryAppliesEmptyQuery(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntries([]Entry{
		{Path: "alpha", Type: TypeFile},
		{Path: "beta", Type: TypeFile},
	})
	model.query = []rune("alp")
	model.queryCursor = len(model.query)
	model.applyQuery()

	keyCh := make(chan keyEvent, 2)
	keyCh <- keyEvent{kind: keyClearQuery}
	keyCh <- keyEvent{kind: keyEnter}

	var stderr bytes.Buffer
	entry, err := pickEntry(context.Background(), model, nil, keyCh, &stderr, 80, pickerThemeForColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != "alpha" {
		t.Fatalf("selected path = %q, want first full-list entry alpha", entry.Path)
	}
	if string(model.query) != "" || model.appliedQuery != "" {
		t.Fatalf("query/appliedQuery = %q/%q, want empty", string(model.query), model.appliedQuery)
	}
	if got := matchPaths(model.matches); !equalStrings(got, []string{"alpha", "beta"}) {
		t.Fatalf("matches = %#v, want full list", got)
	}
}

func TestPickerModelBackspaceSearchesAllEntries(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})
	model.addEntry(Entry{Path: "bravo"})
	model.query = []rune("be")
	model.queryCursor = len(model.query)
	model.applyQuery()
	if got := matchPaths(model.matches); !equalStrings(got, []string{"beta"}) {
		t.Fatalf("matches = %#v, want beta", got)
	}

	model.backspace()
	model.applyQuery()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"beta", "bravo"}) {
		t.Fatalf("matches = %#v, want beta/bravo after backspace", got)
	}
}

func TestPickerModelNewEntriesWhileDirtyDisableNarrowing(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha"})
	model.addEntry(Entry{Path: "beta"})
	model.appendRune('b')
	model.applyQuery()
	if got := matchPaths(model.matches); !equalStrings(got, []string{"beta"}) {
		t.Fatalf("matches = %#v, want beta", got)
	}

	model.appendRune('e')
	model.addEntries([]Entry{{Path: "beacon"}})
	model.applyQuery()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"beta", "beacon"}) {
		t.Fatalf("matches = %#v, want beta/beacon", got)
	}
}

func TestPickerModelTokenQueryRanksFormerStagedFilterUseCase(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "fixtures/alpha/beta/signal/"})
	model.addEntry(Entry{Path: "fixtures/alpha/beta/signal/alpha-beta-signal-01.dat"})
	model.addEntry(Entry{Path: "fixtures/alpha/beta/q-u-i-e-t/s-i-g-n-a-l/example-series-01.dat"})

	model.query = []rune("alpha beta signal .dat")
	model.applyQuery()

	got := matchPaths(model.matches)
	wantPrefix := []string{
		"fixtures/alpha/beta/signal/alpha-beta-signal-01.dat",
		"fixtures/alpha/beta/q-u-i-e-t/s-i-g-n-a-l/example-series-01.dat",
	}
	if len(got) < len(wantPrefix) || !equalStrings(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("matches = %#v, want prefix %#v", got, wantPrefix)
	}
}

func TestPickerModelTokenQueryMatchesDynamicEntries(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "first.mkv"})
	model.query = []rune(".mkv")
	model.applyQuery()

	model.addEntries([]Entry{{Path: "second.mkv"}, {Path: "second.txt"}})

	model.applyQuery()
	if got := matchPaths(model.matches); !equalStrings(got, []string{"first.mkv", "second.mkv"}) {
		t.Fatalf("matches = %#v, want first/second mkv", got)
	}
}

func TestPickerModelCaseSensitiveFiltering(t *testing.T) {
	model := newPickerModel(SortPath)
	model.caseSensitive = true
	model.addEntry(Entry{Path: "src/Foo.go"})
	model.addEntry(Entry{Path: "src/foo.go"})

	model.query = []rune("foo")
	model.applyQuery()

	if got := matchPaths(model.matches); !equalStrings(got, []string{"src/foo.go"}) {
		t.Fatalf("matches = %#v, want lower-case foo only", got)
	}
}

func TestPickEntryReturnsSelectedEntryOnEnter(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "selected.txt", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent, 1)
	keyCh <- keyEvent{kind: keyEnter}

	var stderr bytes.Buffer
	entry, err := pickEntry(context.Background(), model, scanCh, keyCh, &stderr, 80, pickerThemeForColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != "selected.txt" {
		t.Fatalf("selected path = %q, want selected.txt", entry.Path)
	}
}

func TestPickEntryNoopsNoopKeyEvents(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "selected.txt", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent, 2)
	keyCh <- keyEvent{kind: keyNoop}
	keyCh <- keyEvent{kind: keyEnter}

	var stderr bytes.Buffer
	entry, err := pickEntry(context.Background(), model, scanCh, keyCh, &stderr, 80, pickerThemeForColor(false))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != "selected.txt" {
		t.Fatalf("selected path = %q, want selected.txt", entry.Path)
	}
}

func TestPickEntryUsesEditableQueryCursor(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "ab", Type: TypeFile})
	model.addEntry(Entry{Path: "ba", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour))
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyLeft}
	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'a'}
	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyEnter}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "ab" {
			t.Fatalf("selected path = %q, want ab", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
}

func TestPickEntryCursorMovementDoesNotApplyDirtyQuery(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.addEntry(Entry{Path: "beta", Type: TypeFile})
	model.query = []rune("b")
	model.queryCursor = len(model.query)
	model.queryDirty = true
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	promptRendered := make(chan struct{}, 10)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, pickerRenderer{
			full: func() {
				rendered <- struct{}{}
			},
			prompt: func() {
				promptRendered <- struct{}{}
			},
		}, time.Hour, fixedQueryDebounce(time.Hour))
		done <- err
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyLeft}
	waitForRenderCount(t, promptRendered, 1)

	if got := model.appliedQuery; got != "" {
		t.Fatalf("applied query = %q, want unchanged empty query", got)
	}
	if !model.queryDirty {
		t.Fatal("cursor movement unexpectedly cleared dirty query")
	}

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryCursorMovementDoesNotFlushPendingScanEntries(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult)
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	promptRendered := make(chan struct{}, 10)
	scanSent := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, pickerRenderer{
			full: func() {
				rendered <- struct{}{}
			},
			prompt: func() {
				promptRendered <- struct{}{}
			},
		}, time.Hour, nil)
		done <- err
	}()

	waitForRenderCount(t, rendered, 1)
	go func() {
		scanCh <- ScanResult{Entries: []Entry{{Path: "pending.txt", Type: TypeFile}}}
		close(scanSent)
	}()
	select {
	case <-scanSent:
	case <-time.After(time.Second):
		t.Fatal("timed out sending scan result")
	}

	keyCh <- keyEvent{kind: keyLeft}
	waitForRenderCount(t, promptRendered, 1)

	if got := len(model.entries); got != 0 {
		t.Fatalf("entries = %d, want pending scan entries to remain unflushed", got)
	}

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryNoopDoesNotFlushPendingScanEntries(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult)
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	scanSent := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, nil)
		done <- err
	}()

	waitForRenderCount(t, rendered, 1)
	go func() {
		scanCh <- ScanResult{Entries: []Entry{{Path: "pending.txt", Type: TypeFile}}}
		close(scanSent)
	}()
	select {
	case <-scanSent:
	case <-time.After(time.Second):
		t.Fatal("timed out sending scan result")
	}

	keyCh <- keyEvent{kind: keyNoop}
	if got := len(model.entries); got != 0 {
		t.Fatalf("entries = %d, want pending scan entries to remain unflushed", got)
	}

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryCancelReturnsCanceled(t *testing.T) {
	model := newPickerModel(SortPath)
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent, 1)
	keyCh <- keyEvent{kind: keyCancel}

	var stderr bytes.Buffer
	_, err := pickEntry(context.Background(), model, scanCh, keyCh, &stderr, 80, pickerThemeForColor(false))
	if err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryReturnsTerminationSignal(t *testing.T) {
	model := newPickerModel(SortPath)
	terminationCh := make(chan os.Signal, 1)
	terminationCh <- syscall.SIGTERM

	_, err := pickEntryWithRendererAndRankerAndSignals(
		context.Background(),
		model,
		nil,
		nil,
		terminationCh,
		pickerRenderer{},
		time.Hour,
		nil,
		defaultQueryRanker,
	)
	var signalErr *terminationSignalError
	if !errors.As(err, &signalErr) {
		t.Fatalf("err = %v, want terminationSignalError", err)
	}
	if signalErr.signal != syscall.SIGTERM {
		t.Fatalf("signal = %v, want SIGTERM", signalErr.signal)
	}
}

func TestPickEntryBatchesScanRendersUntilCompletion(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult, 100)
	for i := 0; i < 100; i++ {
		scanCh <- ScanResult{Entries: []Entry{{Path: string(rune('a' + i%26)), Type: TypeFile}}}
	}
	close(scanCh)
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour))
		done <- err
	}()

	waitForRenderCount(t, rendered, 2)
	if len(model.entries) != 100 {
		t.Fatalf("entries = %d, want 100", len(model.entries))
	}
	keyCh <- keyEvent{kind: keyEnter}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := len(rendered); got != 0 {
		t.Fatalf("unexpected extra scan renders queued: %d", got)
	}
}

func TestPickEntryFlushesPendingScanEntriesBeforeKey(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult, 1)
	scanCh <- ScanResult{Entries: []Entry{{Path: "pending.txt", Type: TypeFile}}}
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour))
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyEnter}
	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "pending.txt" {
			t.Fatalf("selected path = %q, want pending.txt", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
}

func TestPickEntryFlushesPendingScanBatchBeforeKey(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult, 1)
	scanCh <- ScanResult{Entries: []Entry{
		{Path: "alpha.txt", Type: TypeFile},
		{Path: "beta.txt", Type: TypeFile},
	}}
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour))
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyEnter}
	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "alpha.txt" {
			t.Fatalf("selected path = %q, want alpha.txt", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
	if len(model.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(model.entries))
	}
}

func TestPickEntryFlushesPendingScanEntriesOnTimer(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult, 1)
	scanCh <- ScanResult{Entries: []Entry{{Path: "timed.txt", Type: TypeFile}}}
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Millisecond, fixedQueryDebounce(time.Hour))
		done <- err
	}()

	waitForRenderCount(t, rendered, 2)
	if len(model.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(model.entries))
	}
	keyCh <- keyEvent{kind: keyEnter}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPickEntryScanRendersUseMinimumInterval(t *testing.T) {
	model := newPickerModel(SortPath)
	scanCh := make(chan ScanResult, 10)
	for i := 0; i < 3; i++ {
		scanCh <- ScanResult{Entries: []Entry{{Path: string(rune('a' + i)), Type: TypeFile}}}
	}
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Millisecond, fixedQueryDebounce(time.Hour))
		done <- err
	}()

	waitForRenderCount(t, rendered, 1)
	select {
	case <-rendered:
		t.Fatal("scan rendered before minimum interval")
	case <-time.After(50 * time.Millisecond):
	}

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryDebouncesQueryFiltering(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.addEntry(Entry{Path: "beta", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	type querySnapshot struct {
		applied string
		dirty   bool
		paths   []string
	}
	snapshots := make(chan querySnapshot, 10)
	renderer := pickerRenderer{
		full: func() {
			snapshots <- querySnapshot{
				applied: model.appliedQuery,
				dirty:   model.queryDirty,
				paths:   matchPaths(model.matches),
			}
		},
	}
	nextSnapshot := func() querySnapshot {
		t.Helper()
		select {
		case snapshot := <-snapshots:
			return snapshot
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for render snapshot")
			return querySnapshot{}
		}
	}
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, renderer, time.Hour, fixedQueryDebounce(20*time.Millisecond))
		done <- err
	}()

	nextSnapshot()
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	snapshot := nextSnapshot()
	if snapshot.applied != "" {
		t.Fatalf("applied query = %q, want stale empty query before debounce", snapshot.applied)
	}
	if !snapshot.dirty {
		t.Fatal("expected query to be dirty before debounce")
	}

	snapshot = nextSnapshot()
	if snapshot.applied != "b" {
		t.Fatalf("applied query = %q, want b after debounce", snapshot.applied)
	}
	if snapshot.dirty {
		t.Fatal("query still dirty after debounce")
	}
	if !equalStrings(snapshot.paths, []string{"beta"}) {
		t.Fatalf("matches = %#v, want beta only", snapshot.paths)
	}

	keyCh <- keyEvent{kind: keyEnter}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPickEntryKeepsPromptResponsiveDuringAsyncFiltering(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	for i := 0; i < queryDebounceImmediateThreshold+1; i++ {
		model.addEntry(Entry{Path: "candidate.txt", Type: TypeFile})
	}
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	queries := make(chan string, 20)
	started := make(chan string, 10)
	ranker := func(ctx context.Context, job queryJob) ([]Match, bool) {
		started <- job.query
		<-ctx.Done()
		return nil, false
	}
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, pickerRenderer{
			full: func() {
				queries <- string(model.query)
			},
			prompt: func() {
				queries <- string(model.query)
			},
		}, time.Hour, fixedQueryDebounce(time.Millisecond), ranker)
		done <- err
	}()

	waitForString(t, queries, "")
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	waitForString(t, queries, "b")
	waitForString(t, started, "b")

	keyCh <- keyEvent{kind: keyRune, r: 'e'}
	waitForString(t, queries, "be")

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryAcceptsSnapshotFilterWhileScanAdvances(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = true
	model.entries = largeCandidateEntries()
	model.entriesVersion = 1
	model.refresh()
	scanCh := make(chan ScanResult)
	keyCh := make(chan keyEvent)
	started := make(chan queryJob, 10)
	release := make(chan []Match)
	canceled := make(chan string, 10)
	snapshots := make(chan pickerSnapshot, 20)
	ranker := blockingTestRanker(started, release, canceled)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, snapshotRenderer(model, snapshots), 0, fixedQueryDebounce(time.Millisecond), ranker)
		done <- err
	}()

	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 1
	})
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	job := waitForQueryJob(t, started)
	if job.query != "b" || job.entriesVersion != 1 {
		t.Fatalf("job = query %q version %d, want b/version 1", job.query, job.entriesVersion)
	}

	scanCh <- ScanResult{Entries: []Entry{{Path: "beacon.txt", Type: TypeFile}}}
	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 2
	})
	select {
	case query := <-canceled:
		t.Fatalf("query %q was canceled by scan flush", query)
	default:
	}

	release <- []Match{{Entry: Entry{Path: "beta.txt", Type: TypeFile}}}
	snapshot := waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.applied == "b" &&
			snapshot.matchedEntriesVersion == 1 &&
			snapshot.entriesVersion == 2 &&
			equalStrings(snapshot.paths, []string{"beta.txt"})
	})
	if snapshot.dirty {
		t.Fatal("accepted current-query snapshot left query dirty")
	}

	job = waitForQueryJob(t, started)
	if job.query != "b" || job.entriesVersion != 2 {
		t.Fatalf("follow-up job = query %q version %d, want b/version 2", job.query, job.entriesVersion)
	}

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryEnterSelectsVisibleSnapshotWhileScanRefreshPending(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = true
	model.entries = largeCandidateEntries()
	model.entriesVersion = 1
	model.refresh()
	scanCh := make(chan ScanResult)
	keyCh := make(chan keyEvent)
	started := make(chan queryJob, 10)
	release := make(chan []Match)
	canceled := make(chan string, 10)
	snapshots := make(chan pickerSnapshot, 20)
	ranker := blockingTestRanker(started, release, canceled)
	debounceCalls := 0
	debounce := func(*pickerModel) time.Duration {
		debounceCalls++
		if debounceCalls == 1 {
			return time.Millisecond
		}
		return time.Hour
	}
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, snapshotRenderer(model, snapshots), 0, debounce, ranker)
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 1
	})
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	waitForQueryJob(t, started)
	scanCh <- ScanResult{Entries: []Entry{{Path: "beacon.txt", Type: TypeFile}}}
	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.entriesVersion == 2
	})
	release <- []Match{{Entry: Entry{Path: "beta.txt", Type: TypeFile}}}
	waitForPickerSnapshot(t, snapshots, func(snapshot pickerSnapshot) bool {
		return snapshot.applied == "b" &&
			snapshot.matchedEntriesVersion == 1 &&
			snapshot.entriesVersion == 2 &&
			equalStrings(snapshot.paths, []string{"beta.txt"})
	})

	keyCh <- keyEvent{kind: keyEnter}
	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "beta.txt" {
			t.Fatalf("selected path = %q, want beta.txt", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for snapshot selection")
	}
}

func TestPickEntryTypingCancelsActiveSnapshotFilter(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.entries = largeCandidateEntries()
	model.entriesVersion = 1
	model.refresh()
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	started := make(chan queryJob, 10)
	release := make(chan []Match)
	canceled := make(chan string, 10)
	ranker := blockingTestRanker(started, release, canceled)
	rendered := make(chan struct{}, 20)
	done := make(chan error, 1)

	go func() {
		_, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Millisecond), ranker)
		done <- err
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	job := waitForQueryJob(t, started)
	if job.query != "b" {
		t.Fatalf("job query = %q, want b", job.query)
	}
	keyCh <- keyEvent{kind: keyRune, r: 'e'}
	waitForString(t, canceled, "b")
	job = waitForQueryJob(t, started)
	if job.query != "be" {
		t.Fatalf("second job query = %q, want be", job.query)
	}

	keyCh <- keyEvent{kind: keyCancel}
	if err := <-done; err != errPickerCanceled {
		t.Fatalf("err = %v, want %v", err, errPickerCanceled)
	}
}

func TestPickEntryEnterWaitsForAsyncFilteringResult(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.addEntry(Entry{Path: "beta", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	started := make(chan queryJob, 1)
	release := make(chan []Match, 1)
	ranker := func(ctx context.Context, job queryJob) ([]Match, bool) {
		started <- job
		select {
		case matches := <-release:
			return matches, true
		case <-ctx.Done():
			return nil, false
		}
	}
	rendered := make(chan struct{}, 10)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour), ranker)
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyEnter}
	job := waitForQueryJob(t, started)
	if job.query != "b" {
		t.Fatalf("query job = %q, want b", job.query)
	}
	release <- []Match{{Entry: Entry{Path: "beta", Type: TypeFile}}}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "beta" {
			t.Fatalf("selected path = %q, want beta", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
}

func TestPickEntrySortRecentThenEnterWaitsForAsyncFilteringResult(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntry(Entry{Path: "alpha-old.jpg", Type: TypeFile, ModTimeNS: 1})
	model.addEntry(Entry{Path: "alpha-new.jpg", Type: TypeFile, ModTimeNS: 2})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	started := make(chan queryJob, 1)
	release := make(chan []Match, 1)
	ranker := func(ctx context.Context, job queryJob) ([]Match, bool) {
		started <- job
		select {
		case matches := <-release:
			return matches, true
		case <-ctx.Done():
			return nil, false
		}
	}
	rendered := make(chan struct{}, 20)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRendererAndRanker(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour), ranker)
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'a'}
	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keySortRecent}
	job := waitForQueryJob(t, started)
	if job.query != "a" {
		t.Fatalf("query job = %q, want a", job.query)
	}
	keyCh <- keyEvent{kind: keyEnter}
	release <- []Match{
		{Entry: Entry{Path: "alpha-old.jpg", Type: TypeFile, ModTimeNS: 1}},
		{Entry: Entry{Path: "alpha-new.jpg", Type: TypeFile, ModTimeNS: 2}},
	}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "alpha-new.jpg" {
			t.Fatalf("selected path = %q, want newest alpha match", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
}

func TestPickEntryImmediateQueryFilteringForSmallCandidateSet(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.addEntry(Entry{Path: "beta", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, nil)
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	waitForRenderCount(t, rendered, 1)
	if model.appliedQuery != "b" {
		t.Fatalf("applied query = %q, want immediate b", model.appliedQuery)
	}
	if model.queryDirty {
		t.Fatal("query still dirty after immediate filter")
	}
	if len(model.matches) != 1 || model.matches[0].Entry.Path != "beta" {
		t.Fatalf("matches = %#v, want beta only", matchPaths(model.matches))
	}

	keyCh <- keyEvent{kind: keyEnter}
	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "beta" {
			t.Fatalf("selected path = %q, want beta", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
}

func TestPickEntrySpaceTokensSearchIndependently(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntry(Entry{Path: "alpha.mkv", Type: TypeFile})
	model.addEntry(Entry{Path: "beta.mkv", Type: TypeFile})
	model.addEntry(Entry{Path: "beta.txt", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent, 10)
	rendered := make(chan struct{}, 20)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, nil)
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	for _, r := range ".mkv be" {
		keyCh <- keyEvent{kind: keyRune, r: r}
	}
	keyCh <- keyEvent{kind: keyEnter}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "beta.mkv" {
			t.Fatalf("selected path = %q, want beta.mkv", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
	if got := string(model.query); got != ".mkv be" {
		t.Fatalf("query = %q, want .mkv be", got)
	}
}

func TestPickEntryEnterAppliesPendingQueryBeforeSelection(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.addEntry(Entry{Path: "beta", Type: TypeFile})
	var scanCh chan ScanResult
	keyCh := make(chan keyEvent)
	rendered := make(chan struct{}, 10)
	done := make(chan Entry, 1)
	errs := make(chan error, 1)

	go func() {
		entry, err := pickEntryWithRenderer(context.Background(), model, scanCh, keyCh, testRenderer(rendered), time.Hour, fixedQueryDebounce(time.Hour))
		if err != nil {
			errs <- err
			return
		}
		done <- entry
	}()

	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyRune, r: 'b'}
	waitForRenderCount(t, rendered, 1)
	keyCh <- keyEvent{kind: keyEnter}

	select {
	case err := <-errs:
		t.Fatal(err)
	case entry := <-done:
		if entry.Path != "beta" {
			t.Fatalf("selected path = %q, want beta", entry.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for selection")
	}
}

func TestReadKeysMapsLeftRightAndUnknownCSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  keyKind
	}{
		{name: "right", input: "\x1b[C", want: keyRight},
		{name: "left", input: "\x1b[D", want: keyLeft},
		{name: "home", input: "\x1b[H", want: keyHome},
		{name: "end", input: "\x1b[F", want: keyEnd},
		{name: "home tilde 1", input: "\x1b[1~", want: keyHome},
		{name: "end tilde 4", input: "\x1b[4~", want: keyEnd},
		{name: "home tilde 7", input: "\x1b[7~", want: keyHome},
		{name: "end tilde 8", input: "\x1b[8~", want: keyEnd},
		{name: "unknown", input: "\x1b[Z", want: keyNoop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := writeTempInputFile(t, tt.input)
			defer file.Close()

			keyCh := readKeys(file)
			key, ok := <-keyCh
			if !ok {
				t.Fatal("key channel closed before event")
			}
			if key.kind != tt.want {
				t.Fatalf("key kind = %v, want %v", key.kind, tt.want)
			}
		})
	}
}

func TestReadKeysMapsSS3Arrows(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  keyKind
	}{
		{name: "up", input: "\x1bOA", want: keyUp},
		{name: "down", input: "\x1bOB", want: keyDown},
		{name: "right", input: "\x1bOC", want: keyRight},
		{name: "left", input: "\x1bOD", want: keyLeft},
		{name: "home", input: "\x1bOH", want: keyHome},
		{name: "end", input: "\x1bOF", want: keyEnd},
		{name: "unknown", input: "\x1bOZ", want: keyNoop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := writeTempInputFile(t, tt.input)
			defer file.Close()

			keyCh := readKeys(file)
			key, ok := <-keyCh
			if !ok {
				t.Fatal("key channel closed before event")
			}
			if key.kind != tt.want {
				t.Fatalf("key kind = %v, want %v", key.kind, tt.want)
			}
		})
	}
}

func TestReadKeysDecodesUTF8QueryRunes(t *testing.T) {
	file := writeTempInputFile(t, "żółć界")
	defer file.Close()

	var got []rune
	for key := range readKeys(file) {
		if key.kind != keyRune {
			t.Fatalf("key kind = %v, want rune", key.kind)
		}
		got = append(got, key.r)
	}
	if want := "żółć界"; string(got) != want {
		t.Fatalf("runes = %q, want %q", string(got), want)
	}
}

func TestReadKeysDropsInvalidUTF8AndContinues(t *testing.T) {
	file := writeTempInputFile(t, string([]byte{0xff, 0xc3, 'x'}))
	defer file.Close()

	key, ok := <-readKeys(file)
	if !ok {
		t.Fatal("key channel closed before valid byte")
	}
	if key.kind != keyRune || key.r != 'x' {
		t.Fatalf("key = (%v, %q), want rune x", key.kind, key.r)
	}
}

func TestReadKeysKeepsUTF8AdjacentToEscapeSequence(t *testing.T) {
	file := writeTempInputFile(t, "界\x1b[Dé")
	defer file.Close()

	keyCh := readKeys(file)
	want := []keyEvent{
		{kind: keyRune, r: '界'},
		{kind: keyLeft},
		{kind: keyRune, r: 'é'},
	}
	for i, expected := range want {
		key, ok := <-keyCh
		if !ok {
			t.Fatalf("key channel closed before event %d", i)
		}
		if key != expected {
			t.Fatalf("key %d = %#v, want %#v", i, key, expected)
		}
	}
}

func TestReadKeysDrainsUnsupportedCSISequences(t *testing.T) {
	file := writeTempInputFile(t, "\x1b[3~x")
	defer file.Close()

	keyCh := readKeys(file)
	key, ok := <-keyCh
	if !ok {
		t.Fatal("key channel closed before event")
	}
	if key.kind != keyNoop {
		t.Fatalf("key kind = %v, want %v", key.kind, keyNoop)
	}

	key, ok = <-keyCh
	if !ok {
		t.Fatal("key channel closed before second event")
	}
	if key.kind != keyRune || key.r != 'x' {
		t.Fatalf("second key = (%v, %q), want rune x", key.kind, key.r)
	}
}

func TestReadKeysNoopsModifiedArrowCSISequences(t *testing.T) {
	file := writeTempInputFile(t, "\x1b[1;5Cx")
	defer file.Close()

	keyCh := readKeys(file)
	key, ok := <-keyCh
	if !ok {
		t.Fatal("key channel closed before event")
	}
	if key.kind != keyNoop {
		t.Fatalf("key kind = %v, want %v", key.kind, keyNoop)
	}

	key, ok = <-keyCh
	if !ok {
		t.Fatal("key channel closed before second event")
	}
	if key.kind != keyRune || key.r != 'x' {
		t.Fatalf("second key = (%v, %q), want rune x", key.kind, key.r)
	}
}

func TestReadKeysNoopsAltModifiedSingleByteWithoutWaitingForAnotherByte(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	keyCh := readKeys(reader)
	if _, err := writer.Write([]byte{27, 'b'}); err != nil {
		t.Fatal(err)
	}

	select {
	case key, ok := <-keyCh:
		if !ok {
			t.Fatal("key channel closed before event")
		}
		if key.kind != keyNoop {
			t.Fatalf("key kind = %v, want %v", key.kind, keyNoop)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Alt-modified byte to be ignored")
	}
}

func TestReadKeysMapsCtrlSpaceToSortRecent(t *testing.T) {
	file := writeTempInputFile(t, string([]byte{0}))
	defer file.Close()

	keyCh := readKeys(file)
	key, ok := <-keyCh
	if !ok {
		t.Fatal("key channel closed before event")
	}
	if key.kind != keySortRecent {
		t.Fatalf("key kind = %v, want %v", key.kind, keySortRecent)
	}
}

func TestReadKeysMapsCtrlUToClearQuery(t *testing.T) {
	file := writeTempInputFile(t, string([]byte{21}))
	defer file.Close()

	keyCh := readKeys(file)
	key, ok := <-keyCh
	if !ok {
		t.Fatal("key channel closed before event")
	}
	if key.kind != keyClearQuery {
		t.Fatalf("key kind = %v, want %v", key.kind, keyClearQuery)
	}
}

func TestReadKeysDropsUnknownSingleControlBytes(t *testing.T) {
	file := writeTempInputFile(t, string([]byte{1}))
	defer file.Close()

	keyCh := readKeys(file)
	if key, ok := <-keyCh; ok {
		t.Fatalf("unexpected key event for unknown control byte: %v", key.kind)
	}
}

func TestPickerModelScrollsSelectionIntoViewport(t *testing.T) {
	model := newPickerModel(SortPath)
	for i := 0; i < 12; i++ {
		model.addEntry(Entry{Path: string(rune('a' + i))})
	}

	for i := 0; i < 10; i++ {
		model.move(1)
	}

	if model.selected != 10 {
		t.Fatalf("selected = %d, want 10", model.selected)
	}
	if model.offset == 0 {
		t.Fatal("expected viewport to scroll after selected entry passes visible entries")
	}
	first, end := visibleResultRange(len(model.matches), model.offset)
	last := end - 1
	if model.selected < first || model.selected > last {
		t.Fatalf("selected %d outside visible entry range %d..%d", model.selected, first, last)
	}
}

func TestVisibleResultRangeUsesPlainTenRowViewport(t *testing.T) {
	start, end := visibleResultRange(20, 4)
	if start != 4 || end != 14 {
		t.Fatalf("range = %d..%d, want 4..14", start, end)
	}
}

func TestPickerModelOffsetClampsAtBottom(t *testing.T) {
	model := newPickerModel(SortPath)
	for i := 0; i < 12; i++ {
		model.addEntry(Entry{Path: string(rune('a' + i))})
	}

	for i := 0; i < 20; i++ {
		model.move(1)
	}

	_, end := visibleResultRange(len(model.matches), model.offset)
	if end-1 != len(model.matches)-1 {
		t.Fatalf("last visible index = %d, want %d", end-1, len(model.matches)-1)
	}
}

func TestRenderPickerHighlightsOnlyEntryText(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})

	var out bytes.Buffer
	renderPicker(&out, model, 20, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[7malpha\x1b[0m\x1b[1E") {
		t.Fatalf("selected entry text not found in render output: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[7malpha               \x1b[0m") {
		t.Fatalf("selection extends beyond row text: %q", rendered)
	}
}

func TestStyledDisplayPathHighlightsMatchedCharactersGreenBoldUnderlined(t *testing.T) {
	entry := Entry{Path: "src/FooBar.go", Type: TypeFile}

	got := styledDisplayPath(entry, []string{"sfb"}, 80, pickerThemeForColor(true), false)
	want := "\x1b[32m\x1b[1m\x1b[4ms\x1b[39m\x1b[22m\x1b[24m\x1b[2mrc/\x1b[22m\x1b[32m\x1b[1m\x1b[4mF\x1b[39m\x1b[22m\x1b[24moo\x1b[32m\x1b[1m\x1b[4mB\x1b[39m\x1b[22m\x1b[24mar.go"
	if got != want {
		t.Fatalf("styled path = %q, want %q", got, want)
	}
}

func TestStyledResultLineUsesPlainFzyLikeRows(t *testing.T) {
	file := Entry{Path: "src/FooBar.go", Type: TypeFile}
	dir := Entry{Path: "src/FooBar", Type: TypeDir}

	gotFile := styledResultLine(file, true, nil, 80, pickerThemeForColor(false), false)
	if gotFile != "\x1b[7msrc/FooBar.go\x1b[0m" {
		t.Fatalf("file line = %q, want selected plain path", gotFile)
	}

	gotColorFile := styledResultLine(file, true, nil, 80, pickerThemeForColor(true), false)
	if gotColorFile != "\x1b[7msrc/FooBar.go\x1b[0m" {
		t.Fatalf("color file line = %q, want reverse-video selected path", gotColorFile)
	}

	gotDir := styledResultLine(dir, false, nil, 80, pickerThemeForColor(false), false)
	if gotDir != "src/FooBar/" {
		t.Fatalf("dir line = %q, want unselected dir path", gotDir)
	}
}

func TestStyledDisplayPathDimsParentPathOnly(t *testing.T) {
	entry := Entry{Path: "fixtures/file.mkv", Type: TypeFile}

	got := styledDisplayPath(entry, nil, 80, pickerThemeForColor(true), false)
	want := "\x1b[2mfixtures/\x1b[22mfile.mkv"
	if got != want {
		t.Fatalf("styled path = %q, want dimmed parent path %q", got, want)
	}
}

func TestStyledDisplayPathHighlightsQueryTokenMatches(t *testing.T) {
	entry := Entry{Path: "src/FooBar.mkv", Type: TypeFile}

	got := styledDisplayPath(entry, []string{".mkv", "fb"}, 80, pickerThemeForColor(true), false)
	for _, want := range []string{
		"\x1b[32m\x1b[1m\x1b[4mF\x1b[39m\x1b[22m\x1b[24m",
		"\x1b[32m\x1b[1m\x1b[4mB\x1b[39m\x1b[22m\x1b[24m",
		"\x1b[32m\x1b[1m\x1b[4m.mkv\x1b[39m\x1b[22m\x1b[24m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("styled path %q missing highlight %q", got, want)
		}
	}
}

func TestStyledDisplayPathHighlightsContiguousSubstringMatch(t *testing.T) {
	entry := Entry{Path: "Waaa/i/t/c/h/Witch.mkv", Type: TypeFile}

	got := styledDisplayPath(entry, []string{"Witch"}, 80, pickerThemeForColor(true), false)
	if !strings.Contains(got, "\x1b[2mWaaa/i/t/c/h/\x1b[22m\x1b[32m\x1b[1m\x1b[4mWitch\x1b[39m\x1b[22m\x1b[24m.mkv") {
		t.Fatalf("styled path did not highlight contiguous substring: %q", got)
	}
}

func TestStyledDisplayPathDoesNotHighlightEmptyQuery(t *testing.T) {
	entry := Entry{Path: "alpha", Type: TypeFile}

	got := styledDisplayPath(entry, nil, 80, pickerThemeForColor(true), false)
	if strings.Contains(got, "\x1b[4m") {
		t.Fatalf("empty query produced underline highlight: %q", got)
	}
}

func TestStyledDisplayPathNoColorThemeDoesNotHighlight(t *testing.T) {
	entry := Entry{Path: "alpha", Type: TypeFile}

	got := styledDisplayPath(entry, []string{"a"}, 80, pickerThemeForColor(false), false)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain theme produced ANSI escapes: %q", got)
	}
}

func TestRenderPickerCombinesSelectionAndUnderlinedMatches(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.appendRune('a')

	var out bytes.Buffer
	renderPicker(&out, model, 20, pickerThemeForColor(true))

	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[7m\x1b[32m\x1b[1m\x1b[4ma\x1b[39m\x1b[22m\x1b[24mlpha\x1b[0m\x1b[1E") {
		t.Fatalf("selected green underline highlight not found in render output: %q", rendered)
	}
}

func TestPickerModelPromptLineShowsActiveQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "active only",
			query: "foo",
			want:  "> foo",
		},
		{
			name: "empty",
			want: "> ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newPickerModel(SortPath)
			model.scanning = false
			model.query = []rune(tt.query)
			model.queryCursor = len(model.query)

			if got := model.promptLine(); got != tt.want {
				t.Fatalf("prompt line = %q, want %q", got, tt.want)
			}
		})
	}
}

func strongAndWeakWindowEntries(count int) []Entry {
	entries := make([]Entry, 0, count)
	for i := 0; i < effectiveStrongWindowMatches+10; i++ {
		entries = append(entries, Entry{Path: "fixtures/alpha/beta/strong-" + threeDigitString(i) + ".dat"})
	}
	for i := 0; len(entries) < count; i++ {
		entries = append(entries, Entry{Path: "fixtures/a-l-p-h-a/b-e-t-a/hidden-" + threeDigitString(i) + ".dat"})
	}
	return entries
}

func episodeLikeWindowEntries() []Entry {
	entries := []Entry{
		{Path: "synthetic/catalog/done/_pack/Alpha Beta Signal S1/Alpha Beta Signal - 10 (BD 1080p).mkv"},
	}
	for i := 1; i <= effectiveStrongWindowMatches+10; i++ {
		episode := i
		if episode >= 10 {
			episode++
		}
		entries = append(entries, Entry{
			Path: "synthetic/catalog/done/_pack/Alpha Beta Signal S1/Alpha Beta Signal - " + twoDigitString(episode) + " (BD 1080p).mkv",
		})
	}
	for i := 0; i < effectiveMixedWindowMatches; i++ {
		entries = append(entries, Entry{
			Path: "synthetic/catalog/done/_pack/Gamma Delta/Gamma Delta Extras/GammaDelta-SP" + twoDigitString(i+1) + "-10bit-BD1080p.mkv",
		})
	}
	return entries
}

func twoDigitString(n int) string {
	return string([]byte{
		byte('0' + n/10%10),
		byte('0' + n%10),
	})
}

func threeDigitString(n int) string {
	return string([]byte{
		byte('0' + n/100%10),
		byte('0' + n/10%10),
		byte('0' + n%10),
	})
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestPickerModelPromptLineShowsErrorIndicator(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanError = context.Canceled
	model.query = []rune("foo")
	model.queryCursor = len(model.query)

	if got, want := model.promptLine(), "> foo"; got != want {
		t.Fatalf("error prompt line = %q, want %q", got, want)
	}
}

func TestRenderPickerTrimsPromptLineFromLeft(t *testing.T) {
	model := newPickerModel(SortPath)
	model.query = []rune("very-long-current")
	model.queryCursor = len(model.query)

	var out bytes.Buffer
	renderPicker(&out, model, 18, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "...y-long-current") {
		t.Fatalf("trimmed prompt did not keep current query visible: %q", rendered)
	}
}

func TestRenderPickerShowsPromptCursorMarker(t *testing.T) {
	model := newPickerModel(SortPath)
	model.query = []rune("foo")
	model.queryCursor = len(model.query)

	var out bytes.Buffer
	renderPicker(&out, model, 20, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "> foo\x1b[7m \x1b[0m\x1b[1E") {
		t.Fatalf("prompt cursor marker missing from render output: %q", rendered)
	}
}

func TestRenderPickerShowsPromptCursorMarkerInMiddle(t *testing.T) {
	model := newPickerModel(SortPath)
	model.query = []rune("foo")
	model.queryCursor = 1

	var out bytes.Buffer
	renderPicker(&out, model, 20, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "> f\x1b[7mo\x1b[0mo\x1b[1E") {
		t.Fatalf("prompt cursor marker missing from middle of render output: %q", rendered)
	}
}

func TestWritePromptLineReservesSpaceForCursorMarker(t *testing.T) {
	var out bytes.Buffer

	writePromptLine(&out, "> very-long-current", len([]rune("> very-long-current")), 10, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "...urrent\x1b[7m \x1b[0m") {
		t.Fatalf("prompt line did not reserve cursor space: %q", rendered)
	}
}

func TestWritePromptLineKeepsMiddleCursorVisible(t *testing.T) {
	var out bytes.Buffer

	writePromptLine(&out, "> very-long-current", len([]rune("> very")), 10, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[7m-\x1b[0m") {
		t.Fatalf("prompt cursor marker missing: %q", rendered)
	}
	if strings.Contains(rendered, "current") {
		t.Fatalf("prompt trim did not follow middle cursor: %q", rendered)
	}
}

func TestWritePromptLineKeepsStartVisibleForStartCursor(t *testing.T) {
	var out bytes.Buffer

	writePromptLine(&out, "> very-long-current", 0, 10, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[7m>\x1b[0m very-lon") {
		t.Fatalf("prompt trim did not keep start cursor visible: %q", rendered)
	}
}

func TestRenderPickerShowsStatusLine(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})
	model.addEntry(Entry{Path: "beta", Type: TypeDir})

	var out bytes.Buffer
	renderPicker(&out, model, 80, pickerThemeForColor(false))

	rendered := out.String()
	if !strings.Contains(rendered, "· 2 total, 2 matched, scanning") {
		t.Fatalf("status line missing from render output: %q", rendered)
	}
	if !strings.Contains(rendered, "> ") {
		t.Fatalf("prompt marker missing from render output: %q", rendered)
	}
	model.scanning = false
	out.Reset()
	renderPicker(&out, model, 80, pickerThemeForColor(false))
	if !strings.Contains(out.String(), "· 2 total, 2 matched, ready") {
		t.Fatalf("ready status line missing from render output: %q", out.String())
	}
	if !strings.Contains(out.String(), "> ") {
		t.Fatalf("idle prompt missing prompt marker: %q", out.String())
	}
}

func TestRenderPickerStylesStatusLineWithColor(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})

	var out bytes.Buffer
	renderPicker(&out, model, 80, pickerThemeForColor(true))

	rendered := out.String()
	if strings.Contains(rendered, "\x1b[48;5;") {
		t.Fatalf("status line unexpectedly used background color: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[1m") {
		t.Fatalf("status line unexpectedly used bold: %q", rendered)
	}
	if !strings.Contains(rendered, "\x1b[38;5;242m· 1 total, 1 matched, ready\x1b[0m") {
		t.Fatalf("styled status line missing from render output: %q", rendered)
	}
}

func TestPickerModelStatusLineShowsVisibleAndFullMatches(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.entries = make([]Entry, 617468)
	model.fullMatches = make([]Match, 1234)
	model.matches = model.fullMatches[:50]

	if got, want := model.statusLine(), "617k total, 1k matched, showing top 50, ready"; got != want {
		t.Fatalf("status line = %q, want %q", got, want)
	}
}

func TestPickerModelStatusLineOmitsShownWhenAllMatchesVisible(t *testing.T) {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.entries = make([]Entry, 1200)
	model.fullMatches = make([]Match, 300)
	model.matches = model.fullMatches

	if got, want := model.statusLine(), "1k total, 300 matched, ready"; got != want {
		t.Fatalf("status line = %q, want %q", got, want)
	}
}

func TestCompactCountUsesWholeSuffixes(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{n: 0, want: "0"},
		{n: 999, want: "999"},
		{n: 1000, want: "1k"},
		{n: 654321, want: "654k"},
		{n: 1000000, want: "1m"},
		{n: 23500000, want: "23m"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := compactCount(tt.n); got != tt.want {
				t.Fatalf("compact count = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPickerModelStatusTextShowsCombinedWork(t *testing.T) {
	model := newPickerModel(SortPath)
	model.filtering = true

	if got, want := model.statusText(), "scanning, filtering"; got != want {
		t.Fatalf("status text = %q, want %q", got, want)
	}

	model.scanning = false
	if got, want := model.statusText(), "filtering"; got != want {
		t.Fatalf("status text = %q, want %q", got, want)
	}

	model.filtering = false
	model.sortingNewest = true
	if got, want := model.statusText(), "sorting newest"; got != want {
		t.Fatalf("status text = %q, want %q", got, want)
	}
}

func TestStyledDisplayPathHighlightsOnlyVisibleTrimmedCharacters(t *testing.T) {
	entry := Entry{Path: "0123456789abcdef", Type: TypeFile}

	got := styledDisplayPath(entry, []string{"af"}, 8, pickerThemeForColor(true), false)
	if strings.Contains(got, "\x1b[4m.") {
		t.Fatalf("trimmed ellipsis was highlighted: %q", got)
	}
	if !strings.Contains(got, "\x1b[32m\x1b[1m\x1b[4mf\x1b[39m\x1b[22m\x1b[24m") {
		t.Fatalf("visible matched suffix was not highlighted: %q", got)
	}
}

func TestTrimPathForDisplayDoesNotSplitUTF8(t *testing.T) {
	got := trimPathForDisplay("fixtures/unicode/世界/ファイル.txt", 14)
	if !strings.Contains(got, "ファイル.txt") {
		t.Fatalf("trimmed unicode path = %q, want intact filename", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("trimmed unicode path contains replacement rune: %q", got)
	}
}

func TestStyledDisplayPathHonorsCaseSensitiveMode(t *testing.T) {
	entry := Entry{Path: "src/FooBar.go", Type: TypeFile}

	got := styledDisplayPath(entry, []string{"sfb"}, 80, pickerThemeForColor(true), true)
	if strings.Contains(got, "\x1b[4m") {
		t.Fatalf("case-sensitive mismatch produced underline highlight: %q", got)
	}
}

func TestStyledDisplayPathEscapesUnsafeFilenameCharacters(t *testing.T) {
	entry := Entry{Path: "dir/line\nbreak\r\t\x1b\\\u202ename" + string([]byte{0xff}), Type: TypeFile}

	got := styledDisplayPath(entry, nil, 200, pickerThemeForColor(false), false)
	want := "dir/line\\nbreak\\r\\t\\x1b\\\\\\u202ename\\xff"
	if got != want {
		t.Fatalf("safe display path = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\r\n\x1b") {
		t.Fatalf("safe display path contains terminal control bytes: %q", got)
	}
}

func TestStyledDisplayPathDoesNotSplitEscapedTokenWhenTrimmed(t *testing.T) {
	entry := Entry{Path: "prefix/unsafe\nname", Type: TypeFile}

	got := styledDisplayPath(entry, nil, 8, pickerThemeForColor(false), false)
	if strings.Contains(got, "\\n") && !strings.Contains(got, "\\nname") {
		t.Fatalf("trimmed display split escaped token: %q", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("trimmed display contains newline bytes: %q", got)
	}
}

func TestRenderPickerDoesNotEmitNewlines(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "alpha", Type: TypeFile})

	var out bytes.Buffer
	renderPicker(&out, model, 20, pickerThemeForColor(false))

	if strings.ContainsAny(out.String(), "\r\n") {
		t.Fatalf("render emitted newline bytes: %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[1B") {
		t.Fatalf("render used column-preserving cursor down: %q", out.String())
	}
}

func TestRenderPickerDoesNotEmitFilenameControlBytes(t *testing.T) {
	model := newPickerModel(SortPath)
	model.addEntry(Entry{Path: "unsafe\n\r\x1b[2Jname", Type: TypeFile})

	var out bytes.Buffer
	renderPicker(&out, model, 40, pickerThemeForColor(false))

	rendered := out.String()
	if strings.ContainsAny(rendered, "\r\n") {
		t.Fatalf("render emitted filename newline bytes: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[2Jname") {
		t.Fatalf("render emitted filename escape sequence: %q", rendered)
	}
	if !strings.Contains(rendered, "unsafe\\n\\r\\x1b[2Jname") {
		t.Fatalf("render missing escaped filename: %q", rendered)
	}
}

func TestPrepareAndClearPickerReservePromptStatusAndRows(t *testing.T) {
	var prepared bytes.Buffer
	preparePicker(&prepared)
	if got, want := strings.Count(prepared.String(), "\r\n"), pickerRows+pickerStatusRows+1; got != want {
		t.Fatalf("prepared reserved lines = %d, want %d", got, want)
	}

	var cleared bytes.Buffer
	clearPicker(&cleared)
	if got, want := strings.Count(cleared.String(), "\x1b[2K"), pickerRows+pickerStatusRows+1; got != want {
		t.Fatalf("cleared lines = %d, want %d", got, want)
	}
}

func TestClearPickerDoesNotEmitNewlines(t *testing.T) {
	var out bytes.Buffer
	clearPicker(&out)

	if strings.ContainsAny(out.String(), "\r\n") {
		t.Fatalf("clear emitted newline bytes: %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[1B") {
		t.Fatalf("clear used column-preserving cursor down: %q", out.String())
	}
}

func writeTempInputFile(t *testing.T, content string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "keys-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	return file
}

func writeTestFileWithModTime(t *testing.T, root, rel string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func writeTestDirWithModTime(t *testing.T, root, rel string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func waitForRenderCount(t *testing.T, rendered <-chan struct{}, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-rendered:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for render %d of %d", i+1, want)
		}
	}
}

func waitForString(t *testing.T, values <-chan string, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-values:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func waitForQueryJob(t *testing.T, jobs <-chan queryJob) queryJob {
	t.Helper()
	select {
	case job := <-jobs:
		return job
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query job")
		return queryJob{}
	}
}

type pickerSnapshot struct {
	applied               string
	dirty                 bool
	paths                 []string
	entriesVersion        uint64
	matchedEntriesVersion uint64
}

func waitForPickerSnapshot(t *testing.T, snapshots <-chan pickerSnapshot, want func(pickerSnapshot) bool) pickerSnapshot {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case snapshot := <-snapshots:
			if want(snapshot) {
				return snapshot
			}
		case <-deadline:
			t.Fatal("timed out waiting for picker snapshot")
			return pickerSnapshot{}
		}
	}
}

func snapshotRenderer(model *pickerModel, snapshots chan<- pickerSnapshot) pickerRenderer {
	return pickerRenderer{
		full: func() {
			snapshots <- pickerSnapshot{
				applied:               model.appliedQuery,
				dirty:                 model.queryDirty,
				paths:                 matchPaths(model.matches),
				entriesVersion:        model.entriesVersion,
				matchedEntriesVersion: model.matchedEntriesVersion,
			}
		},
	}
}

func blockingTestRanker(started chan<- queryJob, release <-chan []Match, canceled chan<- string) queryRanker {
	return func(ctx context.Context, job queryJob) ([]Match, bool) {
		started <- job
		select {
		case matches := <-release:
			return matches, true
		case <-ctx.Done():
			canceled <- job.query
			return nil, false
		}
	}
}

func largeCandidateEntries() []Entry {
	entries := make([]Entry, queryDebounceImmediateThreshold+1)
	for i := range entries {
		entries[i] = Entry{Path: "candidate.txt", Type: TypeFile}
	}
	entries[0] = Entry{Path: "beta.txt", Type: TypeFile}
	return entries
}

func fixedQueryDebounce(delay time.Duration) func(*pickerModel) time.Duration {
	return func(*pickerModel) time.Duration {
		return delay
	}
}

func testRenderer(rendered chan<- struct{}) pickerRenderer {
	return pickerRenderer{
		full: func() {
			rendered <- struct{}{}
		},
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
