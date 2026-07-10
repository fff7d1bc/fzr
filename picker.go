package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const pickerRows = 10
const pickerStatusRows = 1
const pickerReservedLines = pickerRows + pickerStatusRows + 1
const scanRenderInterval = time.Second
const minScanRenderInterval = 250 * time.Millisecond

const (
	queryDebounceImmediateThreshold = 1_000
	queryDebounceSmall              = 100 * time.Millisecond
	queryDebounceLarge              = 250 * time.Millisecond
	queryDebounceLargeThreshold     = 10_000
	effectiveStrongWindowMatches    = 50
	effectiveMixedWindowMatches     = 200
	effectiveStrongProbeMatches     = 10
)

var errPickerCanceled = errors.New("selection canceled")

type terminationSignalError struct {
	signal os.Signal
}

func (e *terminationSignalError) Error() string {
	return fmt.Sprintf("terminated by %s", e.signal)
}

type pickerModel struct {
	query                 []rune
	queryCursor           int
	appliedQuery          string
	queryDirty            bool
	lastEditAppend        bool
	entries               []Entry
	entriesVersion        uint64
	fullMatches           []Match
	matches               []Match
	matchedEntriesVersion uint64
	selected              int
	offset                int
	scanning              bool
	filtering             bool
	sortingNewest         bool
	scanError             error
	fallbackSort          SortMode
	caseSensitive         bool
	root                  string
	mtimeCache            map[string]int64
	recentSortActive      bool
}

func newPickerModel(fallbackSort SortMode) *pickerModel {
	m := &pickerModel{scanning: true, fallbackSort: fallbackSort}
	m.refresh()
	return m
}

func (m *pickerModel) addEntry(entry Entry) {
	m.entries = append(m.entries, entry)
	m.entriesVersion++
	m.refresh()
}

func (m *pickerModel) addEntries(entries []Entry) {
	if len(entries) == 0 {
		return
	}
	m.entries = append(m.entries, entries...)
	m.entriesVersion++
	if m.recentSortActive {
		// Recent sort is a temporary user view; new scan results should not be
		// spliced into that ordering until a query edit resets it.
		return
	}
	if !m.queryDirty && m.appliedQuery != "" {
		// Keep the current filtered snapshot visible while scanning continues.
		// The event loop will schedule a fresh ranking pass for the newer
		// entriesVersion instead of reranking synchronously on every scan batch.
		return
	}
	if !m.queryDirty {
		if m.canAppendUnrankedMatches() {
			newMatches := matchesFromEntries(entries)
			m.fullMatches = append(m.fullMatches, newMatches...)
			m.matches = append(m.matches, newMatches...)
			m.matchedEntriesVersion = m.entriesVersion
			m.normalizeSelection()
			return
		}
		m.refresh()
	}
}

func (m *pickerModel) appendRune(r rune) {
	m.clampQueryCursor()
	m.resetRecentSort()
	m.lastEditAppend = m.queryCursor == len(m.query)
	m.query = append(m.query, 0)
	copy(m.query[m.queryCursor+1:], m.query[m.queryCursor:])
	m.query[m.queryCursor] = r
	m.queryCursor++
	m.selected = 0
	m.offset = 0
	m.queryDirty = string(m.query) != m.appliedQuery
}

func (m *pickerModel) backspace() {
	m.clampQueryCursor()
	if m.queryCursor == 0 {
		return
	}
	m.resetRecentSort()
	m.query = append(m.query[:m.queryCursor-1], m.query[m.queryCursor:]...)
	m.queryCursor--
	m.lastEditAppend = false
	m.selected = 0
	m.offset = 0
	m.queryDirty = string(m.query) != m.appliedQuery
}

func (m *pickerModel) moveQueryCursor(delta int) {
	m.queryCursor += delta
	m.clampQueryCursor()
}

func (m *pickerModel) moveQueryCursorHome() {
	m.queryCursor = 0
}

func (m *pickerModel) moveQueryCursorEnd() {
	m.queryCursor = len(m.query)
}

func (m *pickerModel) clearQuery() {
	m.clampQueryCursor()
	if len(m.query) == 0 {
		return
	}
	m.resetRecentSort()
	m.query = nil
	m.queryCursor = 0
	m.lastEditAppend = false
	m.selected = 0
	m.offset = 0
	m.queryDirty = m.appliedQuery != ""
}

func (m *pickerModel) resetRecentSort() {
	if !m.recentSortActive {
		return
	}
	m.matches = effectiveMatches(m.fullMatches, m.appliedQuery)
	m.recentSortActive = false
	m.normalizeSelection()
}

func (m *pickerModel) clampQueryCursor() {
	if m.queryCursor < 0 {
		m.queryCursor = 0
	}
	if m.queryCursor > len(m.query) {
		m.queryCursor = len(m.query)
	}
}

func (m *pickerModel) move(delta int) {
	if len(m.matches) == 0 {
		m.selected = 0
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.matches) {
		m.selected = len(m.matches) - 1
	}
	m.keepSelectionVisible()
}

func (m *pickerModel) selectedEntry() (Entry, bool) {
	if len(m.matches) == 0 || m.selected < 0 || m.selected >= len(m.matches) {
		return Entry{}, false
	}
	return m.matches[m.selected].Entry, true
}

func (m *pickerModel) refresh() {
	m.fullMatches = m.rankCandidates(m.appliedQuery)
	m.matches = effectiveMatches(m.fullMatches, m.appliedQuery)
	m.matchedEntriesVersion = m.entriesVersion
	m.normalizeSelection()
}

func (m *pickerModel) applyQuery() {
	nextQuery := string(m.query)
	if m.canNarrowTo(nextQuery) {
		// Appending text can only remove matches, so rank the current result set
		// when no scan entries arrived since the last applied query.
		m.appliedQuery = nextQuery
		m.queryDirty = false
		m.fullMatches = rankMatchesWithOptions(m.fullMatches, m.appliedQuery, m.fallbackSort, m.caseSensitive)
		m.matches = effectiveMatches(m.fullMatches, m.appliedQuery)
		m.recentSortActive = false
		m.normalizeSelection()
		return
	}
	m.appliedQuery = string(m.query)
	m.queryDirty = false
	m.recentSortActive = false
	m.refresh()
}

func (m *pickerModel) canNarrowTo(nextQuery string) bool {
	return m.lastEditAppend &&
		nextQuery != m.appliedQuery &&
		strings.HasPrefix(nextQuery, m.appliedQuery) &&
		m.matchedEntriesVersion == m.entriesVersion
}

func (m *pickerModel) queryWorkPending() bool {
	return m.queryDirty || m.querySnapshotStale()
}

func (m *pickerModel) recentSortQueryWorkPending() bool {
	return m.queryDirty || m.querySnapshotStaleForRecentSort()
}

func (m *pickerModel) querySnapshotStale() bool {
	if m.recentSortActive {
		return false
	}
	return m.querySnapshotStaleForRecentSort()
}

func (m *pickerModel) querySnapshotStaleForRecentSort() bool {
	return string(m.query) == m.appliedQuery && m.matchedEntriesVersion != m.entriesVersion
}

func (m *pickerModel) rankCandidates(query string) []Match {
	if query == "" && m.fallbackSort != SortMTime {
		return matchesFromEntries(m.entries)
	}
	return rankEntriesWithOptions(m.entries, query, m.fallbackSort, m.caseSensitive)
}

func (m *pickerModel) canAppendUnrankedMatches() bool {
	// Empty path-sort results are already in scan order, so a fresh scan batch
	// can be appended without reranking the whole list.
	return m.appliedQuery == "" &&
		m.fallbackSort != SortMTime &&
		m.matchedEntriesVersion+1 == m.entriesVersion
}

func matchesFromEntries(entries []Entry) []Match {
	matches := make([]Match, len(entries))
	for i, entry := range entries {
		matches[i] = Match{Entry: entry}
	}
	return matches
}

func (m *pickerModel) sortCurrentMatchesNewest() {
	source := m.matches
	if len(m.fullMatches) > 0 {
		source = m.fullMatches
	}
	if len(source) == 0 {
		return
	}
	m.matches = make([]Match, len(source))
	copy(m.matches, source)
	if m.mtimeCache == nil {
		m.mtimeCache = make(map[string]int64, len(m.matches))
	}
	// The initial scan may omit mtimes for path-sorted interactive mode; fetch
	// file mtimes lazily only for the user's explicit request to sort by recency.
	// Recent sort uses every full match for the current query, not just the
	// normal top-ranked window, so a newer lower-scored path can rise to the top.
	for i := range m.matches {
		if m.matches[i].Entry.Type != TypeDir {
			m.matches[i].Entry.ModTimeNS = m.cachedModTimeNS(m.matches[i].Entry)
		}
	}
	sort.SliceStable(m.matches, func(i, j int) bool {
		if m.matches[i].Entry.Type == TypeDir || m.matches[j].Entry.Type == TypeDir {
			if m.matches[i].Entry.Type != m.matches[j].Entry.Type {
				return m.matches[i].Entry.Type != TypeDir
			}
			return m.matches[i].Entry.Path < m.matches[j].Entry.Path
		}
		if m.matches[i].Entry.ModTimeNS == m.matches[j].Entry.ModTimeNS {
			return m.matches[i].Entry.Path < m.matches[j].Entry.Path
		}
		return m.matches[i].Entry.ModTimeNS > m.matches[j].Entry.ModTimeNS
	})
	m.recentSortActive = true
	m.selected = 0
	m.offset = 0
}

func (m *pickerModel) cachedModTimeNS(entry Entry) int64 {
	if entry.ModTimeNS != 0 {
		m.mtimeCache[entry.Path] = entry.ModTimeNS
		return entry.ModTimeNS
	}
	if modTime, ok := m.mtimeCache[entry.Path]; ok {
		return modTime
	}
	info, err := os.Lstat(m.entryFilesystemPath(entry))
	if err != nil {
		m.mtimeCache[entry.Path] = 0
		return 0
	}
	modTime := modTimeNS(info.ModTime())
	m.mtimeCache[entry.Path] = modTime
	return modTime
}

func (m *pickerModel) entryFilesystemPath(entry Entry) string {
	root := m.root
	if root == "" {
		root = "."
	}
	return filepath.Join(root, filepath.FromSlash(entry.Path))
}

func effectiveMatches(matches []Match, query string) []Match {
	plan := makeQueryPlan(query)
	if len(plan.specs) < 2 || len(matches) <= effectiveStrongWindowMatches {
		return matches
	}
	// Multi-token queries can produce many weak fuzzy matches. Limit the
	// rendered working set once the top results are strong enough to be useful.
	if hasStrongTopMatches(matches, len(plan.specs)) {
		return matches[:effectiveStrongWindowMatches]
	}
	if len(matches) <= effectiveMixedWindowMatches {
		return matches
	}
	return matches[:effectiveMixedWindowMatches]
}

func hasStrongTopMatches(matches []Match, tokenCount int) bool {
	if len(matches) < effectiveStrongProbeMatches {
		return false
	}
	for _, match := range matches[:effectiveStrongProbeMatches] {
		if weakEffectiveMatch(match, tokenCount) {
			return false
		}
	}
	return true
}

func weakEffectiveMatch(match Match, tokenCount int) bool {
	threshold := strongSubstringThreshold(tokenCount)
	return match.substringCount < threshold && (!match.disjoint || match.disjointQuality < 0)
}

func strongSubstringThreshold(tokenCount int) int {
	if tokenCount < 3 {
		return tokenCount
	}
	return 3
}

func (m *pickerModel) highlightQueries() []string {
	if string(m.query) != "" {
		return []string{string(m.query)}
	}
	return nil
}

func (m *pickerModel) normalizeSelection() {
	if m.selected >= len(m.matches) {
		m.selected = len(m.matches) - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
	m.keepSelectionVisible()
}

func (m *pickerModel) queryDebounceDelayFor(nextQuery string) time.Duration {
	candidates := len(m.entries)
	if m.canNarrowTo(nextQuery) {
		candidates = len(m.fullMatches)
	}
	if candidates <= queryDebounceImmediateThreshold {
		return 0
	}
	if candidates >= queryDebounceLargeThreshold {
		return queryDebounceLarge
	}
	return queryDebounceSmall
}

func (m *pickerModel) keepSelectionVisible() {
	if len(m.matches) == 0 {
		m.selected = 0
		m.offset = 0
		return
	}
	if m.offset < 0 {
		m.offset = 0
	}
	maxOffset := len(m.matches) - 1
	if m.offset > maxOffset {
		m.offset = maxOffset
	}

	for {
		if m.selected < m.offset {
			m.offset = m.selected
			continue
		}
		lastVisible := m.offset + pickerRows - 1
		if m.selected > lastVisible {
			m.offset += m.selected - lastVisible
			continue
		}
		return
	}
}

func runInteractive(ctx context.Context, opts ScanOptions, sortMode SortMode, caseSensitive bool, matchStyle matchStyle, stdin *os.File, stdout, stderr io.Writer) error {
	inFD := int(stdin.Fd())
	if !term.IsTerminal(inFD) {
		return fmt.Errorf("interactive mode requires a terminal on stdin")
	}
	terminationCh := make(chan os.Signal, 1)
	signal.Notify(terminationCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(terminationCh)
	oldState, err := term.MakeRaw(inFD)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	model := newPickerModel(sortMode)
	model.caseSensitive = caseSensitive
	model.root = opts.Root
	scanCh := scanEntries(ctx, opts)
	keyCh := readKeys(stdin)
	width := terminalWidth(inFD)
	theme := pickerThemeForWriter(stderr, matchStyle)

	preparePicker(stderr)
	fmt.Fprint(stderr, "\x1b[?25l")
	// Always restore terminal state and cursor visibility, even when picking is
	// canceled or a scan error aborts the loop.
	defer term.Restore(inFD, oldState)
	defer fmt.Fprint(stderr, "\x1b[?25h\x1b[0m")

	selected, err := pickEntryWithSignals(ctx, model, scanCh, keyCh, terminationCh, stderr, width, theme)
	clearPicker(stderr)
	if err != nil {
		return err
	}
	// Restore raw mode before printing the selected path so command
	// substitution receives plain text and the shell prompt is usable again.
	if err := term.Restore(inFD, oldState); err != nil {
		return err
	}
	return writePath(stdout, selected.Path)
}

func pickEntry(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, stderr io.Writer, width int, theme pickerTheme) (Entry, error) {
	return pickEntryWithSignals(ctx, model, scanCh, keyCh, nil, stderr, width, theme)
}

func pickEntryWithSignals(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, terminationCh <-chan os.Signal, stderr io.Writer, width int, theme pickerTheme) (Entry, error) {
	renderer := pickerRenderer{
		full: func() {
			renderPicker(stderr, model, width, theme)
		},
		prompt: func() {
			renderPickerPrompt(stderr, model, width, theme)
		},
	}
	return pickEntryWithRendererAndRankerAndSignals(ctx, model, scanCh, keyCh, terminationCh, renderer, scanRenderInterval, nil, defaultQueryRanker)
}

type pickerRenderer struct {
	full   func()
	prompt func()
}

func (r pickerRenderer) renderFull() {
	if r.full != nil {
		r.full()
	}
}

func (r pickerRenderer) renderPrompt() {
	if r.prompt != nil {
		r.prompt()
		return
	}
	r.renderFull()
}

type queryJob struct {
	id             uint64
	query          string
	entriesVersion uint64
	narrow         bool
	entries        []Entry
	matches        []Match
	fallbackSort   SortMode
	caseSensitive  bool
}

type queryJobResult struct {
	id             uint64
	query          string
	entriesVersion uint64
	fullMatches    []Match
}

type queryRanker func(context.Context, queryJob) ([]Match, bool)

func defaultQueryRanker(ctx context.Context, job queryJob) ([]Match, bool) {
	if job.narrow {
		return rankMatchesWithOptionsContext(ctx, job.matches, job.query, job.fallbackSort, job.caseSensitive)
	}
	return rankEntriesWithOptionsContext(ctx, job.entries, job.query, job.fallbackSort, job.caseSensitive)
}

type pendingQueryAction int

const (
	pendingQueryNone pendingQueryAction = iota
	pendingQueryEnter
	pendingQuerySortRecent
	pendingQuerySortRecentEnter
)

func pickEntryWithRenderer(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, renderer pickerRenderer, scanInterval time.Duration, queryDebounce func(*pickerModel) time.Duration) (Entry, error) {
	return pickEntryWithRendererAndRanker(ctx, model, scanCh, keyCh, renderer, scanInterval, queryDebounce, defaultQueryRanker)
}

func pickEntryWithRendererAndRanker(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, renderer pickerRenderer, scanInterval time.Duration, queryDebounce func(*pickerModel) time.Duration, ranker queryRanker) (Entry, error) {
	return pickEntryWithRendererAndRankerAndSignals(ctx, model, scanCh, keyCh, nil, renderer, scanInterval, queryDebounce, ranker)
}

func pickEntryWithRendererAndRankerAndSignals(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, terminationCh <-chan os.Signal, renderer pickerRenderer, scanInterval time.Duration, queryDebounce func(*pickerModel) time.Duration, ranker queryRanker) (Entry, error) {
	scanInterval = effectiveScanRenderInterval(scanInterval)
	var pending []Entry
	var tick <-chan time.Time
	if scanInterval > 0 {
		ticker := time.NewTicker(scanInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	var queryTimer *time.Timer
	var queryTick <-chan time.Time
	stopQueryTimer := func() {
		if queryTimer == nil {
			return
		}
		if !queryTimer.Stop() {
			select {
			case <-queryTimer.C:
			default:
			}
		}
		queryTick = nil
	}
	defer stopQueryTimer()
	var queryJobID uint64
	var activeQueryID uint64
	var cancelQuery context.CancelFunc
	queryResultCh := make(chan queryJobResult, 1)
	pendingAction := pendingQueryNone
	cancelActiveQuery := func() {
		if cancelQuery == nil {
			return
		}
		cancelQuery()
		cancelQuery = nil
		activeQueryID = 0
		model.filtering = false
	}
	defer cancelActiveQuery()
	applyQueryResult := func(result queryJobResult) bool {
		// Query workers race with typing and scanning; a result may be from an
		// older entry snapshot, but it is still useful as long as it matches the
		// current query text and no newer query job replaced it.
		if result.id != activeQueryID || result.query != string(model.query) || result.entriesVersion > model.entriesVersion {
			return false
		}
		cancelQuery = nil
		activeQueryID = 0
		model.filtering = false
		model.appliedQuery = result.query
		model.queryDirty = false
		model.fullMatches = result.fullMatches
		model.matches = effectiveMatches(model.fullMatches, model.appliedQuery)
		model.matchedEntriesVersion = result.entriesVersion
		model.recentSortActive = false
		model.normalizeSelection()
		return true
	}
	sortNewestWithStatus := func() {
		model.sortingNewest = true
		renderer.renderFull()
		model.sortCurrentMatchesNewest()
		model.sortingNewest = false
	}
	runPendingAction := func() (Entry, bool, error) {
		action := pendingAction
		pendingAction = pendingQueryNone
		switch action {
		case pendingQueryEnter:
			entry, ok := model.selectedEntry()
			if !ok {
				return Entry{}, false, errPickerCanceled
			}
			return entry, true, nil
		case pendingQuerySortRecent:
			sortNewestWithStatus()
		case pendingQuerySortRecentEnter:
			sortNewestWithStatus()
			entry, ok := model.selectedEntry()
			if !ok {
				return Entry{}, false, errPickerCanceled
			}
			return entry, true, nil
		}
		return Entry{}, false, nil
	}
	startQueryJob := func(forceAsync bool, recentSort bool) bool {
		if recentSort {
			if !model.recentSortQueryWorkPending() {
				return false
			}
		} else if !model.queryWorkPending() {
			return false
		}
		stopQueryTimer()
		cancelActiveQuery()
		nextQuery := string(model.query)
		candidates := len(model.entries)
		narrow := model.canNarrowTo(nextQuery)
		if narrow {
			candidates = len(model.fullMatches)
		}
		if !forceAsync && candidates <= queryDebounceImmediateThreshold {
			model.applyQuery()
			return false
		}
		// Large rankings run asynchronously so key handling and scan rendering
		// stay responsive while scoring works through the candidate set.
		queryJobID++
		jobID := queryJobID
		jobCtx, cancel := context.WithCancel(ctx)
		cancelQuery = cancel
		activeQueryID = jobID
		model.filtering = true
		job := queryJob{
			id:             jobID,
			query:          nextQuery,
			entriesVersion: model.entriesVersion,
			narrow:         narrow,
			entries:        model.entries,
			matches:        model.fullMatches,
			fallbackSort:   model.fallbackSort,
			caseSensitive:  model.caseSensitive,
		}
		go func() {
			fullMatches, ok := ranker(jobCtx, job)
			if !ok {
				return
			}
			result := queryJobResult{
				id:             job.id,
				query:          job.query,
				entriesVersion: job.entriesVersion,
				fullMatches:    fullMatches,
			}
			select {
			case queryResultCh <- result:
			case <-jobCtx.Done():
			}
		}()
		return true
	}
	startQueryTimer := func(cancelExisting bool) bool {
		if cancelExisting {
			cancelActiveQuery()
		} else if activeQueryID != 0 {
			return false
		}
		if !model.queryWorkPending() {
			return false
		}
		nextQuery := string(model.query)
		delay := model.queryDebounceDelayFor(nextQuery)
		if queryDebounce != nil {
			delay = queryDebounce(model)
		}
		if delay <= 0 {
			model.applyQuery()
			renderer.renderFull()
			return true
		}
		if queryTimer == nil {
			queryTimer = time.NewTimer(delay)
		} else {
			if !queryTimer.Stop() {
				select {
				case <-queryTimer.C:
				default:
				}
			}
			queryTimer.Reset(delay)
		}
		queryTick = queryTimer.C
		return false
	}
	applyPendingQuery := func(forceAsync bool) bool {
		if !model.queryDirty {
			return false
		}
		if activeQueryID != 0 {
			return true
		}
		return startQueryJob(forceAsync, false)
	}
	applyPendingQueryForRecentSort := func(forceAsync bool) bool {
		// Ctrl-Space is a request to sort the query's full matched set by recency.
		// If scanning advanced since that set was ranked, refresh before sorting
		// so new matching files are not left behind a stale view.
		if !model.recentSortQueryWorkPending() {
			return false
		}
		if activeQueryID != 0 {
			return true
		}
		return startQueryJob(forceAsync, true)
	}
	flushPending := func() bool {
		if len(pending) == 0 {
			return false
		}
		hadActiveQuery := activeQueryID != 0
		// Scan results are batched between renders. When a query worker is
		// already ranking an older snapshot, let it finish so the user still gets
		// a filtered view before a follow-up pass covers newer entries.
		model.addEntries(pending)
		pending = nil
		return !hadActiveQuery && model.queryWorkPending()
	}
	renderer.renderFull()
	for {
		select {
		case result, ok := <-scanCh:
			restartQuery := false
			if !ok {
				restartQuery = flushPending()
				model.scanning = false
				scanCh = nil
			} else if result.Err != nil {
				restartQuery = flushPending()
				model.scanError = result.Err
				model.scanning = false
			} else {
				pending = append(pending, result.Entries...)
			}
			if !ok || model.scanError != nil || scanInterval <= 0 {
				restartQuery = flushPending() || restartQuery
				if restartQuery {
					renderer.renderFull()
					startQueryTimer(false)
				} else {
					renderer.renderFull()
				}
			}
			if model.scanError != nil {
				return Entry{}, model.scanError
			}
		case <-tick:
			if len(pending) > 0 {
				if flushPending() {
					renderer.renderFull()
					startQueryTimer(false)
				} else {
					renderer.renderFull()
				}
			}
		case <-queryTick:
			queryTick = nil
			flushPending()
			startQueryJob(false, false)
			renderer.renderFull()
		case result := <-queryResultCh:
			if applyQueryResult(result) {
				entry, done, err := runPendingAction()
				if err != nil || done {
					return entry, err
				}
				renderer.renderFull()
				startQueryTimer(false)
			}
		case key, ok := <-keyCh:
			if !ok {
				return Entry{}, errPickerCanceled
			}
			if key.kind == keyNoop {
				continue
			}
			if key.kind == keyRight || key.kind == keyLeft || key.kind == keyHome || key.kind == keyEnd {
				switch key.kind {
				case keyRight:
					model.moveQueryCursor(1)
				case keyLeft:
					model.moveQueryCursor(-1)
				case keyHome:
					model.moveQueryCursorHome()
				case keyEnd:
					model.moveQueryCursorEnd()
				}
				renderer.renderPrompt()
				continue
			}
			flushPending()
			rendered := false
			switch key.kind {
			case keyRune:
				pendingAction = pendingQueryNone
				model.appendRune(key.r)
				rendered = startQueryTimer(true)
			case keyBackspace:
				pendingAction = pendingQueryNone
				model.backspace()
				rendered = startQueryTimer(true)
			case keyClearQuery:
				pendingAction = pendingQueryNone
				model.clearQuery()
				rendered = startQueryTimer(true)
			case keyDown:
				if applyPendingQuery(false) {
					rendered = true
				}
				model.move(1)
			case keyUp:
				if applyPendingQuery(false) {
					rendered = true
				}
				model.move(-1)
			case keyEnter:
				if applyPendingQuery(true) {
					// Enter should select the result for the text currently on
					// screen, so remember it until async filtering catches up.
					if pendingAction == pendingQuerySortRecent {
						pendingAction = pendingQuerySortRecentEnter
					} else {
						pendingAction = pendingQueryEnter
					}
					renderer.renderFull()
					continue
				}
				entry, ok := model.selectedEntry()
				if !ok {
					return Entry{}, errPickerCanceled
				}
				return entry, nil
			case keySortRecent:
				if applyPendingQueryForRecentSort(true) {
					// Ctrl-Space means "filter first, then sort the resulting
					// matched set by recency." If Enter follows before this finishes
					// and the query text is already applied, Enter keeps its normal
					// current-selection behavior.
					pendingAction = pendingQuerySortRecent
					renderer.renderFull()
					continue
				}
				sortNewestWithStatus()
			case keyCancel:
				return Entry{}, errPickerCanceled
			}
			if !rendered {
				renderer.renderFull()
			}
		case <-ctx.Done():
			return Entry{}, ctx.Err()
		case received, ok := <-terminationCh:
			if !ok {
				terminationCh = nil
				continue
			}
			return Entry{}, &terminationSignalError{signal: received}
		}
	}
}

type keyKind int

const (
	keyRune keyKind = iota
	keyBackspace
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyEnter
	keyCancel
	keySortRecent
	keyClearQuery
	keyNoop
)

type keyEvent struct {
	kind keyKind
	r    rune
}

func readKeys(stdin *os.File) <-chan keyEvent {
	out := make(chan keyEvent)
	go func() {
		defer close(out)
		reader := bufio.NewReader(stdin)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			switch b {
			case 3, 27:
				if b == 27 {
					// Escape is both a standalone cancel key and the prefix for
					// arrow-key sequences. A short poll lets normal arrow sequences
					// arrive without making a plain Esc wait for another keypress.
					// bufio may already have read the rest of the escape sequence,
					// so check its buffer before polling the file descriptor.
					if reader.Buffered() > 0 || byteReady(stdin.Fd(), 25) {
						prefix, err := reader.ReadByte()
						if err != nil {
							return
						}
						if prefix == 'O' {
							code, err := reader.ReadByte()
							if err != nil {
								return
							}
							out <- arrowKeyEvent(code)
							continue
						}
						if prefix != '[' {
							out <- keyEvent{kind: keyNoop}
							continue
						}
						code, err := reader.ReadByte()
						if err != nil {
							return
						}
						sequence, ok := readCSISequence(reader, code)
						if !ok {
							return
						}
						out <- csiKeyEvent(sequence)
						continue
					}
				}
				out <- keyEvent{kind: keyCancel}
			case 10, 13:
				out <- keyEvent{kind: keyEnter}
			case 0:
				out <- keyEvent{kind: keySortRecent}
			case 21:
				out <- keyEvent{kind: keyClearQuery}
			case 14:
				out <- keyEvent{kind: keyDown}
			case 16:
				out <- keyEvent{kind: keyUp}
			case 8, 127:
				out <- keyEvent{kind: keyBackspace}
			default:
				if b >= 32 && b < 127 {
					out <- keyEvent{kind: keyRune, r: rune(b)}
				}
			}
		}
	}()
	return out
}

func arrowKeyEvent(code byte) keyEvent {
	switch code {
	case 'A':
		return keyEvent{kind: keyUp}
	case 'B':
		return keyEvent{kind: keyDown}
	case 'C':
		return keyEvent{kind: keyRight}
	case 'D':
		return keyEvent{kind: keyLeft}
	case 'H':
		return keyEvent{kind: keyHome}
	case 'F':
		return keyEvent{kind: keyEnd}
	default:
		return keyEvent{kind: keyNoop}
	}
}

func csiKeyEvent(sequence []byte) keyEvent {
	if len(sequence) == 1 {
		return arrowKeyEvent(sequence[0])
	}
	if len(sequence) == 2 && sequence[1] == '~' {
		switch sequence[0] {
		case '1', '7':
			return keyEvent{kind: keyHome}
		case '4', '8':
			return keyEvent{kind: keyEnd}
		}
	}
	return keyEvent{kind: keyNoop}
}

func readCSISequence(reader *bufio.Reader, first byte) ([]byte, bool) {
	sequence := []byte{first}
	b := first
	for {
		if b >= 0x40 && b <= 0x7e {
			return sequence, true
		}
		next, err := reader.ReadByte()
		if err != nil {
			return nil, false
		}
		sequence = append(sequence, next)
		b = next
	}
}

func byteReady(fd uintptr, timeoutMillis int) bool {
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(pollFDs, timeoutMillis)
	return err == nil && n > 0 && pollFDs[0].Revents&unix.POLLIN != 0
}

func terminalWidth(fd int) int {
	width, _, err := term.GetSize(fd)
	if err != nil || width < 20 {
		return 80
	}
	return width
}

func preparePicker(w io.Writer) {
	// Reserve a small fixed area below the shell prompt and save the cursor at
	// its top; rendering restores to that anchor instead of using alt-screen.
	for i := 0; i < pickerReservedLines; i++ {
		fmt.Fprint(w, "\r\n")
	}
	fmt.Fprintf(w, "\x1b[%dA\r\x1b7", pickerReservedLines)
}

func clearPicker(w io.Writer) {
	// Return to the saved anchor and clear only the lines fzr reserved, leaving
	// the rest of the terminal scrollback untouched.
	fmt.Fprint(w, "\x1b8")
	for i := 0; i < pickerReservedLines; i++ {
		fmt.Fprint(w, "\x1b[2K")
		if i < pickerReservedLines-1 {
			fmt.Fprint(w, "\x1b[1E")
		}
	}
	fmt.Fprint(w, "\x1b8")
}

func renderPicker(w io.Writer, m *pickerModel, width int, theme pickerTheme) {
	renderPickerPrompt(w, m, width, theme)
	renderPickerStatus(w, m, width, theme)

	highlightQueries := m.highlightQueries()
	start, end := visibleResultRange(len(m.matches), m.offset)
	for index := start; index < end; index++ {
		match := m.matches[index]
		line := styledResultLine(match.Entry, index == m.selected, highlightQueries, width, theme, m.caseSensitive)
		writeStyledLine(w, line)
	}
	for i := end - start; i < pickerRows; i++ {
		writePlainLine(w, "", width)
	}
}

func renderPickerPrompt(w io.Writer, m *pickerModel, width int, theme pickerTheme) {
	// Cursor movement in the query can redraw just the prompt line; scan and
	// ranking changes still use the full reserved area.
	fmt.Fprint(w, "\x1b8")
	prompt, cursor := m.promptLineAndCursor()
	writePromptLine(w, prompt, cursor, width, theme)
}

func renderPickerStatus(w io.Writer, m *pickerModel, width int, theme pickerTheme) {
	line := "· " + m.statusLine()
	if width > 0 {
		line = trimPathForDisplay(line, width)
	}
	if theme.statusStart != "" {
		line = theme.statusStart + line + theme.statusReset
	}
	writeStyledLine(w, line)
}

func (m *pickerModel) promptLine() string {
	line, _ := m.promptLineAndCursor()
	return line
}

func (m *pickerModel) promptLineAndCursor() (string, int) {
	m.clampQueryCursor()
	activeQuery := string(m.query)
	prefix := "> "
	if activeQuery == "" {
		return prefix, len([]rune(prefix))
	}
	return prefix + activeQuery, len([]rune(prefix)) + m.queryCursor
}

func (m *pickerModel) statusLine() string {
	total := compactCount(len(m.entries))
	matched := compactCount(len(m.fullMatches))
	if len(m.matches) < len(m.fullMatches) {
		return fmt.Sprintf("%s total, %s matched, showing top %s, %s", total, matched, compactCount(len(m.matches)), m.statusText())
	}
	return fmt.Sprintf("%s total, %s matched, %s", total, matched, m.statusText())
}

func (m *pickerModel) statusText() string {
	switch {
	case m.scanning && m.filtering:
		return "scanning, filtering"
	case m.sortingNewest:
		return "sorting newest"
	case m.scanning:
		return "scanning"
	case m.filtering:
		return "filtering"
	default:
		return "ready"
	}
}

func compactCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%dm", n/1_000_000)
	}
}

func effectiveScanRenderInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return interval
	}
	if interval < minScanRenderInterval {
		return minScanRenderInterval
	}
	return interval
}

type displayToken struct {
	text           string
	sourcePosition int
}

func safeDisplayTokens(entry Entry) []displayToken {
	tokens := make([]displayToken, 0, len(entry.Path)+1)
	for bytePosition, sourcePosition := 0, 0; bytePosition < len(entry.Path); sourcePosition++ {
		r, size := utf8.DecodeRuneInString(entry.Path[bytePosition:])
		text := ""
		if r == utf8.RuneError && size == 1 {
			text = fmt.Sprintf("\\x%02x", entry.Path[bytePosition])
		} else {
			text = safeDisplayRune(r)
		}
		tokens = append(tokens, displayToken{text: text, sourcePosition: sourcePosition})
		bytePosition += size
	}
	if entry.Type == TypeDir {
		tokens = append(tokens, displayToken{text: "/", sourcePosition: -1})
	}
	return tokens
}

func safeDisplayRune(r rune) string {
	switch r {
	case '\\':
		return "\\\\"
	case '\n':
		return "\\n"
	case '\r':
		return "\\r"
	case '\t':
		return "\\t"
	}
	if unicode.IsPrint(r) {
		return string(r)
	}
	if r <= 0xff {
		return fmt.Sprintf("\\x%02x", r)
	}
	if r <= 0xffff {
		return fmt.Sprintf("\\u%04x", r)
	}
	return fmt.Sprintf("\\U%08x", r)
}

func trimDisplayTokens(tokens []displayToken, width int) []displayToken {
	if width <= 0 {
		return tokens
	}
	total := 0
	for _, token := range tokens {
		total += len([]rune(token.text))
	}
	if total <= width {
		return tokens
	}
	if width <= 3 {
		used := 0
		end := 0
		for end < len(tokens) {
			tokenWidth := len([]rune(tokens[end].text))
			if used+tokenWidth > width {
				break
			}
			used += tokenWidth
			end++
		}
		return tokens[:end]
	}

	available := width - 3
	used := 0
	start := len(tokens)
	for start > 0 {
		tokenWidth := len([]rune(tokens[start-1].text))
		if used+tokenWidth > available {
			break
		}
		used += tokenWidth
		start--
	}
	for start < len(tokens) && tokens[start].text == "/" {
		start++
	}
	trimmed := make([]displayToken, 0, len(tokens)-start+1)
	trimmed = append(trimmed, displayToken{text: "...", sourcePosition: -1})
	trimmed = append(trimmed, tokens[start:]...)
	return trimmed
}

func styledResultLine(entry Entry, selected bool, queries []string, width int, theme pickerTheme, caseSensitive bool) string {
	pathTheme := theme
	if selected {
		pathTheme.dimStart = ""
		pathTheme.dimReset = ""
	}
	line := styledDisplayPath(entry, queries, width, pathTheme, caseSensitive)
	if selected && theme.selectStart != "" {
		return theme.selectStart + line + theme.selectReset
	}
	return line
}

func styledDisplayPath(entry Entry, queries []string, width int, theme pickerTheme, caseSensitive bool) string {
	tokens := trimDisplayTokens(safeDisplayTokens(entry), width)
	positions, ok := matchPositionsForQueriesWithCase(entry.Path, queries, caseSensitive)
	positionSet := make(map[int]struct{}, len(positions))
	for _, pos := range positions {
		positionSet[pos] = struct{}{}
	}

	basenameStart := displayBasenameStart(entry.Path)
	var b strings.Builder
	inMatch := false
	inDim := false
	for _, token := range tokens {
		shouldDim := theme.dimStart != "" && token.sourcePosition >= 0 && token.sourcePosition < basenameStart
		_, shouldStyle := positionSet[token.sourcePosition]
		shouldStyle = shouldStyle && ok && theme.matchStart != ""
		// Match style temporarily overrides dim; after a match ends, dim is
		// restored when the remaining characters are still path context.
		if shouldStyle && !inMatch {
			if inDim {
				b.WriteString(theme.dimReset)
				inDim = false
			}
			b.WriteString(theme.matchStart)
			inMatch = true
		} else if !shouldStyle && inMatch {
			b.WriteString(theme.matchReset)
			inMatch = false
		}
		if !inMatch {
			if shouldDim && !inDim {
				b.WriteString(theme.dimStart)
				inDim = true
			} else if !shouldDim && inDim {
				b.WriteString(theme.dimReset)
				inDim = false
			}
		}
		b.WriteString(token.text)
	}
	if inMatch {
		b.WriteString(theme.matchReset)
	}
	if inDim {
		b.WriteString(theme.dimReset)
	}
	return b.String()
}

func displayBasenameStart(display string) int {
	runes := []rune(display)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '/' {
			return i + 1
		}
	}
	return 0
}

func writePlainLine(w io.Writer, text string, width int) {
	fmt.Fprintf(w, "\x1b[2K%s\x1b[1E", trimPathForDisplay(text, width))
}

func writePromptLine(w io.Writer, text string, cursorPosition int, width int, theme pickerTheme) {
	if width > 0 {
		trimWidth := width
		if cursorPosition >= len([]rune(text)) {
			// Keep one cell available for the highlighted cursor when it sits at
			// end-of-line; otherwise terminals may wrap the prompt.
			trimWidth = width - 1
		}
		text, cursorPosition = trimRunesAroundCursor(text, cursorPosition, trimWidth)
	}
	runes := []rune(text)
	if cursorPosition < 0 {
		cursorPosition = 0
	}
	if cursorPosition > len(runes) {
		cursorPosition = len(runes)
	}
	cursorRune := ' '
	afterStart := cursorPosition
	if cursorPosition < len(runes) {
		cursorRune = runes[cursorPosition]
		afterStart = cursorPosition + 1
	}
	cursor := theme.selectStart + string(cursorRune) + theme.selectReset
	fmt.Fprintf(w, "\x1b[2K%s%s%s\x1b[1E", string(runes[:cursorPosition]), cursor, string(runes[afterStart:]))
}

func writeStyledLine(w io.Writer, text string) {
	fmt.Fprintf(w, "\x1b[2K%s\x1b[1E", text)
}

func trimPathForDisplay(path string, width int) string {
	runes := []rune(path)
	if width <= 0 || len(runes) <= width {
		return path
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return "..." + strings.TrimLeft(string(runes[len(runes)-width+3:]), "/")
}

func trimRunesAroundCursor(text string, cursorPosition int, width int) (string, int) {
	if width <= 0 {
		return "", 0
	}
	runes := []rune(text)
	if cursorPosition < 0 {
		cursorPosition = 0
	}
	if cursorPosition > len(runes) {
		cursorPosition = len(runes)
	}
	if len(runes) <= width {
		return text, cursorPosition
	}
	if width <= 3 {
		start := cursorPosition - width
		if start < 0 {
			start = 0
		}
		if start > len(runes)-width {
			start = len(runes) - width
		}
		return string(runes[start : start+width]), cursorPosition - start
	}

	contentWidth := width - 3
	// For long prompts, bias the window toward the cursor and mark hidden left
	// context with an ellipsis.
	start := cursorPosition - contentWidth
	if start < 0 {
		start = 0
	}
	if start == 0 {
		return string(runes[:width]), cursorPosition
	}
	if start > len(runes)-contentWidth {
		start = len(runes) - contentWidth
	}
	visibleTail := string(runes[start : start+contentWidth])
	visible := "..." + visibleTail
	cursor := cursorPosition - start + 3
	if cursor > len([]rune(visible)) {
		cursor = len([]rune(visible))
	}
	return visible, cursor
}

func visibleResultRange(matchCount, offset int) (int, int) {
	if matchCount <= 0 {
		return 0, 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= matchCount {
		offset = matchCount - 1
	}
	end := offset + pickerRows
	if end > matchCount {
		end = matchCount
	}
	return offset, end
}
