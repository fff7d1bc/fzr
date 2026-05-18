package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const pickerRows = 10
const pickerReservedLines = pickerRows + 1
const scanRenderInterval = time.Second

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

func (m *pickerModel) rankCandidates(query string) []Match {
	if query == "" && m.fallbackSort != SortMTime {
		return matchesFromEntries(m.entries)
	}
	return rankEntriesWithOptions(m.entries, query, m.fallbackSort, m.caseSensitive)
}

func (m *pickerModel) canAppendUnrankedMatches() bool {
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
	if len(m.matches) == 0 {
		return
	}
	if m.mtimeCache == nil {
		m.mtimeCache = make(map[string]int64, len(m.matches))
	}
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

func runInteractive(ctx context.Context, opts ScanOptions, sortMode SortMode, caseSensitive bool, stdin *os.File, stdout, stderr io.Writer) error {
	inFD := int(stdin.Fd())
	if !term.IsTerminal(inFD) {
		return fmt.Errorf("interactive mode requires a terminal on stdin")
	}
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
	theme := pickerThemeForStderr(os.Stderr)

	preparePicker(stderr)
	fmt.Fprint(stderr, "\x1b[?25l")
	defer term.Restore(inFD, oldState)
	defer fmt.Fprint(stderr, "\x1b[?25h\x1b[0m")

	selected, err := pickEntry(ctx, model, scanCh, keyCh, stderr, width, theme)
	clearPicker(stderr)
	if err != nil {
		return err
	}
	if err := term.Restore(inFD, oldState); err != nil {
		return err
	}
	fmt.Fprintln(stdout, selected.Path)
	return nil
}

func pickEntry(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, stderr io.Writer, width int, theme pickerTheme) (Entry, error) {
	renderer := pickerRenderer{
		full: func() {
			renderPicker(stderr, model, width, theme)
		},
		prompt: func() {
			renderPickerPrompt(stderr, model, width, theme)
		},
	}
	return pickEntryWithRenderer(ctx, model, scanCh, keyCh, renderer, scanRenderInterval, nil)
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

func pickEntryWithRenderer(ctx context.Context, model *pickerModel, scanCh <-chan ScanResult, keyCh <-chan keyEvent, renderer pickerRenderer, scanInterval time.Duration, queryDebounce func(*pickerModel) time.Duration) (Entry, error) {
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
	startQueryTimer := func() bool {
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
	applyPendingQuery := func() {
		if !model.queryDirty {
			return
		}
		stopQueryTimer()
		model.applyQuery()
	}
	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		model.addEntries(pending)
		pending = nil
	}
	renderer.renderFull()
	for {
		select {
		case result, ok := <-scanCh:
			if !ok {
				flushPending()
				model.scanning = false
				scanCh = nil
			} else if result.Err != nil {
				flushPending()
				model.scanError = result.Err
				model.scanning = false
			} else {
				pending = append(pending, result.Entries...)
			}
			if !ok || model.scanError != nil || scanInterval <= 0 {
				flushPending()
				renderer.renderFull()
			}
			if model.scanError != nil {
				return Entry{}, model.scanError
			}
		case <-tick:
			if len(pending) > 0 {
				flushPending()
				renderer.renderFull()
			}
		case <-queryTick:
			queryTick = nil
			flushPending()
			model.applyQuery()
			renderer.renderFull()
		case key, ok := <-keyCh:
			if !ok {
				return Entry{}, errPickerCanceled
			}
			if key.kind == keyNoop {
				continue
			}
			if key.kind == keyRight || key.kind == keyLeft {
				if key.kind == keyRight {
					model.moveQueryCursor(1)
				} else {
					model.moveQueryCursor(-1)
				}
				renderer.renderPrompt()
				continue
			}
			flushPending()
			rendered := false
			switch key.kind {
			case keyRune:
				model.appendRune(key.r)
				rendered = startQueryTimer()
			case keyBackspace:
				model.backspace()
				rendered = startQueryTimer()
			case keyDown:
				applyPendingQuery()
				model.move(1)
			case keyUp:
				applyPendingQuery()
				model.move(-1)
			case keyEnter:
				applyPendingQuery()
				entry, ok := model.selectedEntry()
				if !ok {
					return Entry{}, errPickerCanceled
				}
				return entry, nil
			case keySortRecent:
				applyPendingQuery()
				model.sortCurrentMatchesNewest()
			case keyCancel:
				return Entry{}, errPickerCanceled
			}
			if !rendered {
				renderer.renderFull()
			}
		case <-ctx.Done():
			return Entry{}, ctx.Err()
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
	keyEnter
	keyCancel
	keySortRecent
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
						out <- arrowKeyEvent(sequence[0])
						continue
					}
				}
				out <- keyEvent{kind: keyCancel}
			case 10, 13:
				out <- keyEvent{kind: keyEnter}
			case 0:
				out <- keyEvent{kind: keySortRecent}
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
	default:
		return keyEvent{kind: keyNoop}
	}
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
	for i := 0; i < pickerReservedLines; i++ {
		fmt.Fprint(w, "\r\n")
	}
	fmt.Fprintf(w, "\x1b[%dA\r\x1b7", pickerReservedLines)
}

func clearPicker(w io.Writer) {
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
	fmt.Fprint(w, "\x1b8")
	prompt, cursor := m.promptLineAndCursor()
	writePromptLine(w, prompt, cursor, width, theme)
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

func displayPath(entry Entry) string {
	if entry.Type == TypeDir {
		return entry.Path + "/"
	}
	return entry.Path
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
	display := displayPath(entry)
	trimmed := trimPathForDisplay(display, width)
	positions, ok := matchPositionsForQueriesWithCase(entry.Path, queries, caseSensitive)

	visibleStart := 0
	trimmedPrefix := 0
	if strings.HasPrefix(trimmed, "...") && len([]rune(display)) > len([]rune(trimmed)) {
		visibleStart = len([]rune(display)) - len([]rune(trimmed)) + 3
		trimmedPrefix = 3
	}
	positionSet := make(map[int]struct{}, len(positions))
	for _, pos := range positions {
		trimmedPos := pos - visibleStart + trimmedPrefix
		if trimmedPos >= trimmedPrefix {
			positionSet[trimmedPos] = struct{}{}
		}
	}

	basenameStart := displayBasenameStart(display, entry.Type)
	var b strings.Builder
	inMatch := false
	inDim := false
	for i, r := range []rune(trimmed) {
		fullPosition := visibleStart + i - trimmedPrefix
		shouldDim := theme.dimStart != "" && i >= trimmedPrefix && fullPosition >= 0 && fullPosition < basenameStart
		_, shouldStyle := positionSet[i]
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
		b.WriteRune(r)
	}
	if inMatch {
		b.WriteString(theme.matchReset)
	}
	if inDim {
		b.WriteString(theme.dimReset)
	}
	return b.String()
}

func displayBasenameStart(display string, entryType EntryType) int {
	runes := []rune(display)
	end := len(runes)
	if entryType == TypeDir && end > 0 && runes[end-1] == '/' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
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
