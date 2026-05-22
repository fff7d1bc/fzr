package main

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

// Natural ordering only breaks tiny fuzzy-score ties, such as an episode suffix
// "06v2" paying one extra trailing-gap unit compared with "07".
const naturalPathNearTieScoreDelta = 10

type Match struct {
	Entry           Entry
	Score           int
	disjoint        bool
	disjointQuality int
	substringCount  int
}

func rankEntries(entries []Entry, query string, fallbackSort SortMode) []Match {
	return rankEntriesWithOptions(entries, query, fallbackSort, false)
}

func rankEntriesWithOptions(entries []Entry, query string, fallbackSort SortMode, caseSensitive bool) []Match {
	matches, _ := rankEntriesWithOptionsContext(context.Background(), entries, query, fallbackSort, caseSensitive)
	return matches
}

func rankEntriesWithOptionsContext(ctx context.Context, entries []Entry, query string, fallbackSort SortMode, caseSensitive bool) ([]Match, bool) {
	if query == "" {
		return plainEntryMatchesContext(ctx, entries, fallbackSort)
	}

	queryPlan := makeQueryPlan(query)
	if len(queryPlan.specs) == 0 {
		return plainEntryMatchesContext(ctx, entries, fallbackSort)
	}
	matches := make([]rankedMatch, 0, initialRankedCapacity(len(entries), queryPlan))
	for i, entry := range entries {
		// Ranking can run asynchronously for large scans; poll cancellation
		// often enough to keep typing responsive without checking every entry.
		if i&255 == 0 && ctx.Err() != nil {
			return nil, false
		}
		matches = appendRankedEntry(matches, entry, queryPlan, caseSensitive)
	}
	if ctx.Err() != nil {
		return nil, false
	}
	return sortedMatches(matches), true
}

func plainEntryMatches(entries []Entry, fallbackSort SortMode) []Match {
	matches, _ := plainEntryMatchesContext(context.Background(), entries, fallbackSort)
	return matches
}

func plainEntryMatchesContext(ctx context.Context, entries []Entry, fallbackSort SortMode) ([]Match, bool) {
	matches := make([]Match, 0, len(entries))
	for i, entry := range entries {
		if i&255 == 0 && ctx.Err() != nil {
			return nil, false
		}
		matches = append(matches, Match{Entry: entry})
	}
	if ctx.Err() != nil {
		return nil, false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return entryLess(matches[i].Entry, matches[j].Entry, fallbackSort)
	})
	return matches, ctx.Err() == nil
}

func appendRankedEntry(matches []rankedMatch, entry Entry, plan queryPlan, caseSensitive bool) []rankedMatch {
	score, ok := scorePathForQueryPlan(entry.Path, plan, caseSensitive)
	if !ok {
		return matches
	}
	return append(matches, rankedMatch{
		Match: Match{
			Entry:           entry,
			Score:           score.totalScore,
			disjoint:        score.disjoint,
			disjointQuality: score.disjointQuality,
			substringCount:  score.substringCount,
		},
		disjointCount: score.disjointCount,
		disjointEnd:   score.disjointEnd,
		fuzzyCount:    score.fuzzyCount,
		matchSpan:     score.matchSpan,
		matchOffset:   score.matchOffset,
	})
}

func sortedMatches(matches []rankedMatch) []Match {
	sort.SliceStable(matches, func(i, j int) bool {
		// Keep ordinary space-separated tokens mostly order-insensitive. Disjoint
		// hints only matter for numeric or repeated-token queries.
		if matches[i].fuzzyCount != matches[j].fuzzyCount {
			return matches[i].fuzzyCount < matches[j].fuzzyCount
		}
		if matches[i].substringCount != matches[j].substringCount {
			return matches[i].substringCount > matches[j].substringCount
		}
		if matches[i].disjointQuality != matches[j].disjointQuality {
			return matches[i].disjointQuality > matches[j].disjointQuality
		}
		if matches[i].disjointCount != matches[j].disjointCount {
			return matches[i].disjointCount > matches[j].disjointCount
		}
		if matches[i].disjointEnd != matches[j].disjointEnd {
			return matches[i].disjointEnd > matches[j].disjointEnd
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].matchSpan != matches[j].matchSpan {
			return matches[i].matchSpan < matches[j].matchSpan
		}
		if matches[i].matchOffset != matches[j].matchOffset {
			return matches[i].matchOffset < matches[j].matchOffset
		}
		return matches[i].Entry.Path < matches[j].Entry.Path
	})
	sortNaturalPathNearTieBuckets(matches)
	out := make([]Match, len(matches))
	for i, match := range matches {
		out[i] = match.Match
	}
	return out
}

func sortNaturalPathNearTieBuckets(matches []rankedMatch) {
	for start := 0; start < len(matches); {
		end := start + 1
		best := matches[start]
		for end < len(matches) && naturalPathNearTieBucketMember(best, matches[end]) {
			end++
		}
		if naturalPathBucketHasNumericDifference(matches[start:end]) {
			sort.SliceStable(matches[start:end], func(i, j int) bool {
				return naturalPathLess(matches[start+i].Entry.Path, matches[start+j].Entry.Path)
			})
		}
		start = end
	}
}

func naturalPathNearTieBucketMember(best, match rankedMatch) bool {
	return best.fuzzyCount == match.fuzzyCount &&
		best.substringCount == match.substringCount &&
		best.disjointQuality == match.disjointQuality &&
		best.disjointCount == match.disjointCount &&
		best.disjointEnd == match.disjointEnd &&
		best.matchSpan == match.matchSpan &&
		best.matchOffset == match.matchOffset &&
		best.Score-match.Score <= naturalPathNearTieScoreDelta
}

func naturalPathBucketHasNumericDifference(matches []rankedMatch) bool {
	for i := 1; i < len(matches); i++ {
		if _, numeric := naturalPathCompareNumeric(matches[0].Entry.Path, matches[i].Entry.Path); numeric {
			return true
		}
	}
	return false
}

type rankedMatch struct {
	Match
	disjointCount int
	disjointEnd   int
	fuzzyCount    int
	matchSpan     int
	matchOffset   int
}

type querySpec struct {
	text    string
	ascii   bool
	runes   []rune
	numeric bool
}

type queryPlan struct {
	specs          []querySpec
	ascii          bool
	joinedLen      int
	preferDisjoint bool
}

func makeQueryPlan(query string) queryPlan {
	// Whitespace separates independent tokens. Most tokens rank independently,
	// while repeated or numeric tokens also prefer an ordered disjoint match.
	specs := makeQuerySpecs(query)
	plan := queryPlan{
		specs:          specs,
		ascii:          true,
		joinedLen:      joinedQueryLen(specs),
		preferDisjoint: preferDisjointTokenRanking(specs),
	}
	for _, spec := range specs {
		if !spec.ascii {
			plan.ascii = false
			break
		}
	}
	return plan
}

func preferDisjointTokenRanking(specs []querySpec) bool {
	if len(specs) < 2 {
		return false
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.numeric {
			// Numeric tokens often identify ordered path fragments such as
			// versions or episode numbers, so prefer non-overlapping placement.
			return true
		}
		key := strings.ToLower(spec.text)
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}
	}
	return false
}

func makeQuerySpecs(query string) []querySpec {
	tokens := strings.Fields(query)
	specs := make([]querySpec, 0, len(tokens))
	for _, token := range tokens {
		specs = append(specs, makeQuerySpec(token))
	}
	return specs
}

func makeQuerySpec(token string) querySpec {
	return querySpec{
		text:    token,
		ascii:   isASCIIString(token),
		runes:   []rune(token),
		numeric: isNumericToken(token),
	}
}

func isNumericToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func rankMatches(matches []Match, query string, fallbackSort SortMode) []Match {
	return rankMatchesWithOptions(matches, query, fallbackSort, false)
}

func rankMatchesWithOptions(matches []Match, query string, fallbackSort SortMode, caseSensitive bool) []Match {
	out, _ := rankMatchesWithOptionsContext(context.Background(), matches, query, fallbackSort, caseSensitive)
	return out
}

func rankMatchesWithOptionsContext(ctx context.Context, matches []Match, query string, fallbackSort SortMode, caseSensitive bool) ([]Match, bool) {
	if query == "" {
		out := make([]Match, len(matches))
		copy(out, matches)
		if ctx.Err() != nil {
			return nil, false
		}
		sort.SliceStable(out, func(i, j int) bool {
			return entryLess(out[i].Entry, out[j].Entry, fallbackSort)
		})
		return out, ctx.Err() == nil
	}

	queryPlan := makeQueryPlan(query)
	if len(queryPlan.specs) == 0 {
		return rankMatchesWithOptionsContext(ctx, matches, "", fallbackSort, caseSensitive)
	}
	ranked := make([]rankedMatch, 0, initialRankedCapacity(len(matches), queryPlan))
	for i, match := range matches {
		// Re-ranking a narrowed list shares the same cancellation contract as a
		// full entry ranking job.
		if i&255 == 0 && ctx.Err() != nil {
			return nil, false
		}
		ranked = appendRankedEntry(ranked, match.Entry, queryPlan, caseSensitive)
	}
	if ctx.Err() != nil {
		return nil, false
	}
	return sortedMatches(ranked), true
}

type pathScore struct {
	disjoint        bool
	disjointCount   int
	disjointEnd     int
	disjointQuality int
	fuzzyCount      int
	matchOffset     int
	matchSpan       int
	substringCount  int
	totalScore      int
}

type tokenScore struct {
	tier   int
	score  int
	span   int
	offset int
}

const (
	disjointFuzzyMatchQuality   = -1
	disjointMaxSubstringQuality = 2
	fzyScoreMin                 = -1 << 30
	fzyScoreMax                 = 1 << 30
	fzyGapLeading               = -5
	fzyGapTrailing              = -5
	fzyGapInner                 = -10
	fzyMatchConsecutive         = 1000
	fzyMatchSlash               = 900
	fzyMatchWord                = 800
	fzyMatchCapital             = 700
	fzyMatchDot                 = 600
	fzyMaxLen                   = 1024
	fzyMaxCells                 = 128 * 1024
)

func initialRankedCapacity(candidates int, plan queryPlan) int {
	if plan.joinedLen <= 1 {
		return candidates
	}
	if candidates < 1024 {
		return candidates
	}
	return 1024
}

func scorePathForQueryPlan(path string, plan queryPlan, caseSensitive bool) (pathScore, bool) {
	if canScorePathASCII(path, plan) {
		// ASCII paths are the common case; avoid rune allocation and Unicode
		// folding unless either side actually needs it.
		return scoreASCIIPathForQueryPlan(path, plan, caseSensitive)
	}

	var result pathScore
	pathRunes := []rune(path)
	for _, query := range plan.specs {
		token, ok := scorePathRunesWithNumeric(pathRunes, query.runes, query.numeric, caseSensitive)
		if !ok {
			return pathScore{}, false
		}
		if token.tier > 0 {
			result.substringCount++
		} else {
			result.fuzzyCount++
		}
		result.totalScore += token.score
		result.matchSpan += token.span
		result.matchOffset += token.offset
	}
	if plan.preferDisjoint {
		_, disjointCount, disjointEnd, disjointQuality, disjoint := scoreDisjointRunes(pathRunes, plan, caseSensitive)
		result.disjoint = disjoint
		result.disjointCount = disjointCount
		result.disjointEnd = disjointEnd
		result.disjointQuality = disjointQuality
	}
	return result, true
}

func scoreASCIIPathForQueryPlan(path string, plan queryPlan, caseSensitive bool) (pathScore, bool) {
	var result pathScore
	for _, query := range plan.specs {
		token, ok := scorePathASCIIWithNumeric(path, query.text, query.numeric, caseSensitive)
		if !ok {
			return pathScore{}, false
		}
		if token.tier > 0 {
			result.substringCount++
		} else {
			result.fuzzyCount++
		}
		result.totalScore += token.score
		result.matchSpan += token.span
		result.matchOffset += token.offset
	}
	if plan.preferDisjoint {
		_, disjointCount, disjointEnd, disjointQuality, disjoint := scoreDisjointASCII(path, plan, caseSensitive)
		result.disjoint = disjoint
		result.disjointCount = disjointCount
		result.disjointEnd = disjointEnd
		result.disjointQuality = disjointQuality
	}
	return result, true
}

func canScorePathASCII(path string, plan queryPlan) bool {
	return plan.ascii && isASCIIString(path)
}

func joinedQueryLen(queries []querySpec) int {
	total := 0
	for _, query := range queries {
		total += len(query.runes)
	}
	return total
}

func scorePathRunes(pathRunes, queryRunes []rune, caseSensitive bool) (int, int, bool) {
	token, ok := scorePathRunesWithNumeric(pathRunes, queryRunes, false, caseSensitive)
	return token.tier, token.score, ok
}

func scorePathRunesWithNumeric(pathRunes, queryRunes []rune, numeric bool, caseSensitive bool) (tokenScore, bool) {
	if len(queryRunes) == 0 {
		return tokenScore{}, true
	}
	if start, _, ok := bestSubstringStartFrom(pathRunes, queryRunes, 0, caseSensitive); ok {
		score := scoreContiguousRun(pathRunes, start, len(queryRunes))
		return tokenScore{
			tier:   1,
			score:  score,
			span:   len(queryRunes),
			offset: componentOffsetRunes(pathRunes, start),
		}, true
	}
	if numeric {
		// Do not fuzzy-match digits; "12" should not match a path containing
		// unrelated "1" and "2" fragments. The dotted-version exception stays
		// bounded to one numeric run such as "3.8.5" so dates, hashes, and other
		// digit groups do not become accidental matches.
		if token, _, ok := scoreDottedNumericRunesFrom(pathRunes, queryRunes, 0); ok {
			return token, true
		}
		return tokenScore{}, false
	}
	score, span, offset, ok := scoreFzyRunes(pathRunes, queryRunes, caseSensitive)
	if !ok {
		return tokenScore{}, false
	}
	return tokenScore{score: score, span: span, offset: offset}, true
}

func scorePathASCII(path, query string, caseSensitive bool) (int, int, bool) {
	token, ok := scorePathASCIIWithNumeric(path, query, false, caseSensitive)
	return token.tier, token.score, ok
}

func scorePathASCIIWithNumeric(path, query string, numeric bool, caseSensitive bool) (tokenScore, bool) {
	if len(query) == 0 {
		return tokenScore{}, true
	}
	if start, _, ok := bestSubstringStartASCIIFrom(path, query, 0, caseSensitive); ok {
		score := scoreContiguousRunASCII(path, start, len(query))
		return tokenScore{
			tier:   1,
			score:  score,
			span:   len(query),
			offset: componentOffsetASCII(path, start),
		}, true
	}
	if numeric {
		// Keep numeric matching stricter than ordinary text for predictable
		// ordering of versioned and numbered paths. Dotted versions are the one
		// weak fallback because users often omit separators in queries.
		if token, _, ok := scoreDottedNumericASCIIFrom(path, query, 0); ok {
			return token, true
		}
		return tokenScore{}, false
	}
	score, span, offset, ok := scoreFzyASCII(path, query, caseSensitive)
	if !ok {
		return tokenScore{}, false
	}
	return tokenScore{score: score, span: span, offset: offset}, true
}

func scoreDisjointRunes(pathRunes []rune, plan queryPlan, caseSensitive bool) (int, int, int, int, bool) {
	if len(plan.specs) < 2 {
		return 0, 0, 0, 0, false
	}
	score := 0
	cursor := 0
	count := 0
	quality := 0
	for _, query := range plan.specs {
		if start, tokenQuality, ok := bestSubstringStartFrom(pathRunes, query.runes, cursor, caseSensitive); ok {
			score += scoreDisjointContiguousRun(pathRunes, start, len(query.runes), cursor)
			cursor = start + len(query.runes)
			quality += tokenQuality
			count++
			continue
		}
		if query.numeric {
			if token, positions, ok := scoreDottedNumericRunesFrom(pathRunes, query.runes, cursor); ok {
				score += token.score
				cursor = positions[len(positions)-1] + 1
				quality += disjointFuzzyMatchQuality
				count++
				continue
			}
			return score, count, cursor, quality, false
		}
		tokenScore, end, ok := scoreDisjointFuzzyRunesFrom(pathRunes, query.runes, cursor, caseSensitive)
		if !ok {
			return score, count, cursor, quality, false
		}
		score += tokenScore
		cursor = end
		quality += disjointFuzzyMatchQuality
		count++
	}
	return score, count, cursor, quality, true
}

func scoreDisjointASCII(path string, plan queryPlan, caseSensitive bool) (int, int, int, int, bool) {
	if len(plan.specs) < 2 {
		return 0, 0, 0, 0, false
	}
	score := 0
	cursor := 0
	count := 0
	quality := 0
	for _, query := range plan.specs {
		if start, tokenQuality, ok := bestSubstringStartASCIIFrom(path, query.text, cursor, caseSensitive); ok {
			score += scoreDisjointContiguousRunASCII(path, start, len(query.text), cursor)
			cursor = start + len(query.text)
			quality += tokenQuality
			count++
			continue
		}
		if query.numeric {
			if token, positions, ok := scoreDottedNumericASCIIFrom(path, query.text, cursor); ok {
				score += token.score
				cursor = positions[len(positions)-1] + 1
				quality += disjointFuzzyMatchQuality
				count++
				continue
			}
			return score, count, cursor, quality, false
		}
		tokenScore, end, ok := scoreDisjointFuzzyASCIIFrom(path, query.text, cursor, caseSensitive)
		if !ok {
			return score, count, cursor, quality, false
		}
		score += tokenScore
		cursor = end
		quality += disjointFuzzyMatchQuality
		count++
	}
	return score, count, cursor, quality, true
}

func bestSubstringStartFrom(pathRunes, queryRunes []rune, startAt int, caseSensitive bool) (int, int, bool) {
	if len(queryRunes) == 0 || len(queryRunes) > len(pathRunes) {
		return 0, 0, false
	}
	if startAt < 0 {
		startAt = 0
	}
	bestStart, bestQuality := 0, -1
	for start := startAt; start <= len(pathRunes)-len(queryRunes); start++ {
		matched := true
		for i, queryRune := range queryRunes {
			if !runesEqual(pathRunes[start+i], queryRune, caseSensitive) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		quality := substringBoundaryQuality(pathRunes, start, len(queryRunes))
		if quality == disjointMaxSubstringQuality {
			return start, quality, true
		}
		if quality > bestQuality {
			bestStart, bestQuality = start, quality
		}
	}
	return bestStart, bestQuality, bestQuality >= 0
}

func bestSubstringStartASCIIFrom(path, query string, startAt int, caseSensitive bool) (int, int, bool) {
	if len(query) == 0 || len(query) > len(path) {
		return 0, 0, false
	}
	if startAt < 0 {
		startAt = 0
	}
	bestStart, bestQuality := 0, -1
	for start := startAt; start <= len(path)-len(query); start++ {
		matched := true
		for i := 0; i < len(query); i++ {
			if !asciiEqual(path[start+i], query[i], caseSensitive) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		quality := substringBoundaryQualityASCII(path, start, len(query))
		if quality == disjointMaxSubstringQuality {
			return start, quality, true
		}
		if quality > bestQuality {
			bestStart, bestQuality = start, quality
		}
	}
	return bestStart, bestQuality, bestQuality >= 0
}

func substringBoundaryQuality(pathRunes []rune, start, length int) int {
	quality := 0
	if start == 0 || !isAlphaNumRune(pathRunes[start-1]) {
		quality++
	}
	end := start + length
	if end >= len(pathRunes) || !isAlphaNumRune(pathRunes[end]) {
		quality++
	}
	return quality
}

func substringBoundaryQualityASCII(path string, start, length int) int {
	quality := 0
	if start == 0 || !isAlphaNumASCII(path[start-1]) {
		quality++
	}
	end := start + length
	if end >= len(path) || !isAlphaNumASCII(path[end]) {
		quality++
	}
	return quality
}

func componentOffsetRunes(pathRunes []rune, start int) int {
	if start <= 0 {
		return 0
	}
	for i := start - 1; i >= 0; i-- {
		if pathRunes[i] == '/' || pathRunes[i] == '\\' {
			return start - i - 1
		}
	}
	return start
}

func componentOffsetASCII(path string, start int) int {
	if start <= 0 {
		return 0
	}
	for i := start - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return start - i - 1
		}
	}
	return start
}

func scoreDisjointContiguousRun(pathRunes []rune, start, length, cursor int) int {
	score := 0
	for idx := start; idx < start+length; idx++ {
		score += 10 + positionBonus(pathRunes, idx)
		if idx > start {
			score += 15
		}
	}
	score -= start - cursor
	return score
}

func scoreDisjointContiguousRunASCII(path string, start, length, cursor int) int {
	score := 0
	for idx := start; idx < start+length; idx++ {
		score += 10 + positionBonusASCII(path, idx)
		if idx > start {
			score += 15
		}
	}
	score -= start - cursor
	return score
}

func scoreDisjointFuzzyRunesFrom(pathRunes, queryRunes []rune, start int, caseSensitive bool) (int, int, bool) {
	positions, ok := fuzzyPositionsFrom(pathRunes, queryRunes, start, caseSensitive)
	if !ok {
		return 0, 0, false
	}
	score := scoreDisjointPositions(pathRunes, positions, start)
	return score, positions[len(positions)-1] + 1, true
}

func scoreDisjointFuzzyASCIIFrom(path, query string, start int, caseSensitive bool) (int, int, bool) {
	score, last := 0, -1
	for i := 0; i < len(query); i++ {
		nextStart := start
		if last >= 0 {
			nextStart = last + 1
		}
		idx := findNextASCII(path, query[i], nextStart, caseSensitive)
		if idx == -1 {
			return 0, 0, false
		}
		score += 10 + positionBonusASCII(path, idx)
		if last >= 0 {
			gap := idx - last - 1
			if gap == 0 {
				score += 15
			} else {
				score -= gap
			}
		} else {
			score -= idx - start
		}
		last = idx
	}
	return score, last + 1, true
}

// A dotted numeric fallback treats "3.8.5" as a single version-shaped run whose
// digits can be typed as "385". It matches typed prefixes of that run too, so
// interactive narrowing keeps the candidate alive while the user is still typing.
func scoreDottedNumericRunesFrom(pathRunes, queryRunes []rune, startAt int) (tokenScore, []int, bool) {
	if len(queryRunes) < 2 {
		return tokenScore{}, nil, false
	}
	if startAt < 0 {
		startAt = 0
	}
	var best tokenScore
	var bestPositions []int
	for start := startAt; start < len(pathRunes); start++ {
		if !isASCIIDigitRune(pathRunes[start]) || !dottedNumericStartBoundary(pathRunes, start) {
			continue
		}
		positions, matchEnd, runEnd, ok := dottedNumericRunesAt(pathRunes, queryRunes, start)
		if !ok || !dottedNumericEndBoundary(pathRunes, runEnd) {
			continue
		}
		token := tokenScore{
			score:  scoreDottedNumericPositions(pathRunes, positions, startAt, runEnd-matchEnd),
			span:   runEnd - start,
			offset: componentOffsetRunes(pathRunes, start),
		}
		if bestPositions == nil || betterDottedNumericToken(token, best) {
			best = token
			bestPositions = positions
		}
	}
	if bestPositions == nil {
		return tokenScore{}, nil, false
	}
	return best, bestPositions, true
}

func scoreDottedNumericASCIIFrom(path, query string, startAt int) (tokenScore, []int, bool) {
	if len(query) < 2 {
		return tokenScore{}, nil, false
	}
	if startAt < 0 {
		startAt = 0
	}
	var best tokenScore
	var bestPositions []int
	for start := startAt; start < len(path); start++ {
		if !isDigit(path[start]) || !dottedNumericStartBoundaryASCII(path, start) {
			continue
		}
		positions, matchEnd, runEnd, ok := dottedNumericASCIIAt(path, query, start)
		if !ok || !dottedNumericEndBoundaryASCII(path, runEnd) {
			continue
		}
		token := tokenScore{
			score:  scoreDottedNumericPositionsASCII(path, positions, startAt, runEnd-matchEnd),
			span:   runEnd - start,
			offset: componentOffsetASCII(path, start),
		}
		if bestPositions == nil || betterDottedNumericToken(token, best) {
			best = token
			bestPositions = positions
		}
	}
	if bestPositions == nil {
		return tokenScore{}, nil, false
	}
	return best, bestPositions, true
}

func betterDottedNumericToken(candidate, best tokenScore) bool {
	if candidate.score != best.score {
		return candidate.score > best.score
	}
	if candidate.span != best.span {
		return candidate.span < best.span
	}
	return candidate.offset < best.offset
}

func dottedNumericRunesAt(pathRunes, queryRunes []rune, start int) ([]int, int, int, bool) {
	positions := make([]int, 0, len(queryRunes))
	queryIdx := 0
	dotCount := 0
	matchEnd := 0
	runEnd := start
	for idx := start; idx < len(pathRunes); idx++ {
		r := pathRunes[idx]
		if isASCIIDigitRune(r) {
			if queryIdx < len(queryRunes) {
				if r != queryRunes[queryIdx] {
					return nil, idx, idx, false
				}
				positions = append(positions, idx)
				queryIdx++
				matchEnd = idx + 1
			}
			runEnd = idx + 1
			continue
		}
		if r != '.' || idx+1 >= len(pathRunes) || !isASCIIDigitRune(pathRunes[idx+1]) {
			break
		}
		dotCount++
		runEnd = idx + 1
	}
	return positions, matchEnd, runEnd, dotCount > 0 && queryIdx == len(queryRunes)
}

func dottedNumericASCIIAt(path, query string, start int) ([]int, int, int, bool) {
	positions := make([]int, 0, len(query))
	queryIdx := 0
	dotCount := 0
	matchEnd := 0
	runEnd := start
	for idx := start; idx < len(path); idx++ {
		b := path[idx]
		if isDigit(b) {
			if queryIdx < len(query) {
				if b != query[queryIdx] {
					return nil, idx, idx, false
				}
				positions = append(positions, idx)
				queryIdx++
				matchEnd = idx + 1
			}
			runEnd = idx + 1
			continue
		}
		if b != '.' || idx+1 >= len(path) || !isDigit(path[idx+1]) {
			break
		}
		dotCount++
		runEnd = idx + 1
	}
	return positions, matchEnd, runEnd, dotCount > 0 && queryIdx == len(query)
}

func dottedNumericStartBoundary(pathRunes []rune, start int) bool {
	if start == 0 {
		return true
	}
	if isAlphaNumRune(pathRunes[start-1]) {
		return false
	}
	return pathRunes[start-1] != '.' || start == 1 || !isASCIIDigitRune(pathRunes[start-2])
}

func dottedNumericStartBoundaryASCII(path string, start int) bool {
	if start == 0 {
		return true
	}
	if isAlphaNumASCII(path[start-1]) {
		return false
	}
	return path[start-1] != '.' || start == 1 || !isDigit(path[start-2])
}

func dottedNumericEndBoundary(pathRunes []rune, end int) bool {
	if end >= len(pathRunes) {
		return true
	}
	if isAlphaNumRune(pathRunes[end]) {
		return false
	}
	return pathRunes[end] != '.' || end+1 >= len(pathRunes) || !isASCIIDigitRune(pathRunes[end+1])
}

func dottedNumericEndBoundaryASCII(path string, end int) bool {
	if end >= len(path) {
		return true
	}
	if isAlphaNumASCII(path[end]) {
		return false
	}
	return path[end] != '.' || end+1 >= len(path) || !isDigit(path[end+1])
}

func scoreDottedNumericPositions(pathRunes []rune, positions []int, cursor int, unmatchedTail int) int {
	score := scoreDisjointPositions(pathRunes, positions, cursor)
	for i := 1; i < len(positions); i++ {
		score -= positions[i] - positions[i-1] - 1
	}
	score -= unmatchedTail
	return score
}

func scoreDottedNumericPositionsASCII(path string, positions []int, cursor int, unmatchedTail int) int {
	score := scoreDisjointPositionsASCII(path, positions, cursor)
	for i := 1; i < len(positions); i++ {
		score -= positions[i] - positions[i-1] - 1
	}
	score -= unmatchedTail
	return score
}

func matchPositions(path, query string) ([]int, bool) {
	return matchPositionsWithCase(path, query, false)
}

func matchPositionsWithCase(path, query string, caseSensitive bool) ([]int, bool) {
	if query == "" {
		return nil, true
	}
	return tokenPositionsForSpec([]rune(path), makeQuerySpec(query), caseSensitive)
}

func matchPositionsForQueries(path string, queries []string) ([]int, bool) {
	return matchPositionsForQueriesWithCase(path, queries, false)
}

func matchPositionsForQueriesWithCase(path string, queries []string, caseSensitive bool) ([]int, bool) {
	plan := makeQueryPlan(strings.Join(queries, " "))
	if len(plan.specs) == 0 {
		return nil, true
	}
	if _, ok := scorePathForQueryPlan(path, plan, caseSensitive); !ok {
		return nil, false
	}
	if plan.preferDisjoint {
		// Highlight the same ordered token placement used for ranking when it
		// fully explains the match; otherwise fall back to accepted token spans.
		if positions, ok := completeDisjointPositionsForQueryPlan(path, plan, caseSensitive); ok {
			return positions, true
		}
	}
	return tokenPositionsForAcceptedQueryPlan(path, plan, caseSensitive)
}

func tokenPositionsForAcceptedQueryPlan(path string, plan queryPlan, caseSensitive bool) ([]int, bool) {
	if !plan.preferDisjoint {
		return independentTokenPositionsForAcceptedQueryPlan(path, plan, caseSensitive)
	}
	return disjointTokenPositionsForAcceptedQueryPlan(path, plan, caseSensitive)
}

func independentTokenPositionsForAcceptedQueryPlan(path string, plan queryPlan, caseSensitive bool) ([]int, bool) {
	pathRunes := []rune(path)
	positionSet := make(map[int]struct{}, plan.joinedLen)
	for _, query := range plan.specs {
		tokenPositions, ok := tokenPositionsForSpec(pathRunes, query, caseSensitive)
		if !ok {
			return nil, false
		}
		for _, position := range tokenPositions {
			positionSet[position] = struct{}{}
		}
	}
	positions := make([]int, 0, len(positionSet))
	for position := range positionSet {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	return positions, true
}

func disjointTokenPositionsForAcceptedQueryPlan(path string, plan queryPlan, caseSensitive bool) ([]int, bool) {
	pathRunes := []rune(path)
	positionSet := make(map[int]struct{}, plan.joinedLen)
	cursor := 0
	for _, query := range plan.specs {
		tokenPositions, end, ok := tokenPositionsFrom(pathRunes, query, cursor, caseSensitive)
		if ok {
			cursor = end
		} else {
			tokenPositions, ok = tokenPositionsForSpec(pathRunes, query, caseSensitive)
			if !ok {
				return nil, false
			}
		}
		for _, position := range tokenPositions {
			positionSet[position] = struct{}{}
		}
	}
	positions := make([]int, 0, len(positionSet))
	for position := range positionSet {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	return positions, true
}

func completeDisjointPositionsForQueryPlan(path string, plan queryPlan, caseSensitive bool) ([]int, bool) {
	if len(plan.specs) < 2 {
		return nil, false
	}
	pathRunes := []rune(path)
	positions := make([]int, 0, plan.joinedLen)
	cursor := 0
	for _, query := range plan.specs {
		tokenPositions, end, ok := tokenPositionsFrom(pathRunes, query, cursor, caseSensitive)
		if !ok {
			return nil, false
		}
		positions = append(positions, tokenPositions...)
		cursor = end
	}
	sort.Ints(positions)
	return positions, true
}

func tokenPositionsForSpec(pathRunes []rune, query querySpec, caseSensitive bool) ([]int, bool) {
	positions, _, ok := tokenPositionsFrom(pathRunes, query, 0, caseSensitive)
	return positions, ok
}

func tokenPositionsFrom(pathRunes []rune, query querySpec, start int, caseSensitive bool) ([]int, int, bool) {
	if len(query.runes) == 0 {
		return nil, start, true
	}
	if matchStart, _, ok := bestSubstringStartFrom(pathRunes, query.runes, start, caseSensitive); ok {
		return contiguousPositions(matchStart, len(query.runes)), matchStart + len(query.runes), true
	}
	if query.numeric {
		if _, positions, ok := scoreDottedNumericRunesFrom(pathRunes, query.runes, start); ok {
			return positions, positions[len(positions)-1] + 1, true
		}
		return nil, start, false
	}
	positions, ok := fzyPositionsFrom(pathRunes, query.runes, start, caseSensitive)
	if !ok {
		return nil, start, false
	}
	return positions, positions[len(positions)-1] + 1, true
}

func fuzzyPositionsFrom(pathRunes, queryRunes []rune, start int, caseSensitive bool) ([]int, bool) {
	last := -1
	positions := make([]int, 0, len(queryRunes))
	for _, qr := range queryRunes {
		nextStart := start
		if last >= 0 {
			nextStart = last + 1
		}
		idx := findNext(pathRunes, qr, nextStart, caseSensitive)
		if idx == -1 {
			return nil, false
		}
		positions = append(positions, idx)
		last = idx
	}
	return positions, true
}

func scoreFzyRunes(pathRunes, queryRunes []rune, caseSensitive bool) (int, int, int, bool) {
	score, _, span, offset, ok := fzyScoreAndPositionsRunesFrom(pathRunes, queryRunes, 0, caseSensitive, false)
	return score, span, offset, ok
}

// Fzy-style dynamic scoring chooses the best alignment for a glued query.
// The DP is bounded to the part of the path that can affect the match.
func scoreFzyASCII(path, query string, caseSensitive bool) (int, int, int, bool) {
	if query == "" || len(query) > len(path) {
		return 0, 0, 0, false
	}
	start, end, ok := fuzzyWindowASCII(path, query, 0, caseSensitive)
	if !ok {
		return 0, 0, 0, false
	}
	if len(query) == len(path) {
		return fzyScoreMax, len(path), componentOffsetASCII(path, 0), true
	}
	windowLen := end - start
	if windowLen > fzyMaxLen || len(query) > fzyMaxLen || windowLen*len(query) > fzyMaxCells {
		// Bound DP memory and CPU for very long paths; greedy scoring preserves
		// interactivity even if it gives up the exact best alignment.
		return scoreGreedyFzyASCII(path, query, 0, caseSensitive)
	}

	var bonusBuf, d0, d1, m0, m1 [fzyMaxLen]int
	bonus := bonusBuf[:windowLen]
	lastD := d0[:windowLen]
	currD := d1[:windowLen]
	lastM := m0[:windowLen]
	currM := m1[:windowLen]
	for i := 0; i < windowLen; i++ {
		pathIdx := start + i
		prev := byte('/')
		if pathIdx > 0 {
			prev = path[pathIdx-1]
		}
		bonus[i] = fzyBonusASCII(prev, path[pathIdx])
		lastD[i] = fzyScoreMin
		lastM[i] = fzyScoreMin
	}

	for i := 0; i < len(query); i++ {
		prevScore := fzyScoreMin
		prevD, prevM := fzyScoreMin, fzyScoreMin
		gapScore := fzyGapInner
		if i == len(query)-1 {
			gapScore = fzyGapTrailing
		}
		for j := 0; j < windowLen; j++ {
			pathIdx := start + j
			if asciiEqual(path[pathIdx], query[i], caseSensitive) {
				score := fzyScoreMin
				if i == 0 {
					score = pathIdx*fzyGapLeading + bonus[j]
				} else if j > 0 {
					score = maxInt(addFzyScore(prevM, bonus[j]), addFzyScore(prevD, fzyMatchConsecutive))
				}
				prevD = lastD[j]
				prevM = lastM[j]
				currD[j] = score
				prevScore = maxInt(score, addFzyScore(prevScore, gapScore))
				currM[j] = prevScore
			} else {
				prevD = lastD[j]
				prevM = lastM[j]
				currD[j] = fzyScoreMin
				prevScore = addFzyScore(prevScore, gapScore)
				currM[j] = prevScore
			}
		}
		lastD, currD = currD, lastD
		lastM, currM = currM, lastM
	}
	score := addFzyScore(lastM[windowLen-1], (len(path)-end)*fzyGapTrailing)
	return score, windowLen, componentOffsetASCII(path, start), true
}

func fzyPositionsFrom(pathRunes, queryRunes []rune, start int, caseSensitive bool) ([]int, bool) {
	if start < 0 {
		start = 0
	}
	if start > len(pathRunes) {
		return nil, false
	}
	_, positions, _, _, ok := fzyScoreAndPositionsRunesFrom(pathRunes, queryRunes, start, caseSensitive, true)
	return positions, ok
}

func fzyScoreAndPositionsRunesFrom(pathRunes, queryRunes []rune, startAt int, caseSensitive bool, wantPositions bool) (int, []int, int, int, bool) {
	if startAt < 0 {
		startAt = 0
	}
	if len(queryRunes) == 0 || startAt > len(pathRunes) || len(queryRunes) > len(pathRunes)-startAt {
		return 0, nil, 0, 0, false
	}
	start, end, ok := fuzzyWindowRunes(pathRunes, queryRunes, startAt, caseSensitive)
	if !ok {
		return 0, nil, 0, 0, false
	}
	if len(queryRunes) == len(pathRunes)-startAt {
		var positions []int
		if wantPositions {
			positions = make([]int, len(queryRunes))
			for i := range positions {
				positions[i] = startAt + i
			}
		}
		return fzyScoreMax, positions, len(queryRunes), componentOffsetRunes(pathRunes, startAt), true
	}

	m := end - start
	n := len(queryRunes)
	if m > fzyMaxLen || n > fzyMaxLen || m*n > fzyMaxCells {
		// Unicode DP allocates by window size, so use the same hard limit as the
		// ASCII path before falling back to greedy alignment.
		return scoreGreedyFzyRunes(pathRunes, queryRunes, startAt, caseSensitive, wantPositions)
	}

	bonus := make([]int, m)
	for i := range bonus {
		pathIdx := start + i
		prev := rune('/')
		if pathIdx > 0 {
			prev = pathRunes[pathIdx-1]
		}
		bonus[i] = fzyBonusRune(prev, pathRunes[pathIdx])
	}

	var D, M [][]int
	if wantPositions {
		D = make([][]int, n)
		M = make([][]int, n)
	}
	lastD := make([]int, m)
	lastM := make([]int, m)
	for i := range lastD {
		lastD[i] = fzyScoreMin
		lastM[i] = fzyScoreMin
	}
	for i := 0; i < n; i++ {
		currD := make([]int, m)
		currM := make([]int, m)
		prevScore := fzyScoreMin
		prevD, prevM := fzyScoreMin, fzyScoreMin
		gapScore := fzyGapInner
		if i == n-1 {
			gapScore = fzyGapTrailing
		}
		for j := 0; j < m; j++ {
			pathIdx := start + j
			if runesEqual(pathRunes[pathIdx], queryRunes[i], caseSensitive) {
				score := fzyScoreMin
				if i == 0 {
					score = pathIdx*fzyGapLeading + bonus[j]
				} else if j > 0 {
					score = maxInt(addFzyScore(prevM, bonus[j]), addFzyScore(prevD, fzyMatchConsecutive))
				}
				prevD = lastD[j]
				prevM = lastM[j]
				currD[j] = score
				prevScore = maxInt(score, addFzyScore(prevScore, gapScore))
				currM[j] = prevScore
			} else {
				prevD = lastD[j]
				prevM = lastM[j]
				currD[j] = fzyScoreMin
				prevScore = addFzyScore(prevScore, gapScore)
				currM[j] = prevScore
			}
		}
		if wantPositions {
			D[i] = currD
			M[i] = currM
		}
		lastD = currD
		lastM = currM
	}
	finalScore := addFzyScore(lastM[m-1], (len(pathRunes)-end)*fzyGapTrailing)
	if !wantPositions {
		return finalScore, nil, m, componentOffsetRunes(pathRunes, start), true
	}

	positions := make([]int, n)
	matchRequired := false
	// Walk the DP matrices backward to recover one optimal alignment for
	// highlighting; matchRequired keeps consecutive-match transitions intact.
	for i, j := n-1, m-1; i >= 0; i-- {
		for ; j >= 0; j-- {
			if D[i][j] != fzyScoreMin && (matchRequired || D[i][j] == M[i][j]) {
				matchRequired = i > 0 && j > 0 && M[i][j] == addFzyScore(D[i-1][j-1], fzyMatchConsecutive)
				positions[i] = start + j
				j--
				break
			}
		}
	}
	return finalScore, positions, m, componentOffsetRunes(pathRunes, start), true
}

func fuzzyWindowASCII(path, query string, startAt int, caseSensitive bool) (int, int, bool) {
	if startAt < 0 {
		startAt = 0
	}
	if len(query) == 0 || startAt > len(path) || len(query) > len(path)-startAt {
		return 0, 0, false
	}
	next := 0
	first, last := -1, -1
	for i := startAt; i < len(path) && next < len(query); i++ {
		if asciiEqual(path[i], query[next], caseSensitive) {
			if next == 0 {
				first = i
			}
			last = i
			next++
		}
	}
	if next != len(query) {
		return 0, 0, false
	}
	end := last + 1
	// Extend through later occurrences of the final query byte so DP can choose
	// a better trailing alignment without scoring the entire path.
	for i := last + 1; i < len(path); i++ {
		if asciiEqual(path[i], query[len(query)-1], caseSensitive) {
			end = i + 1
		}
	}
	return first, end, true
}

func fuzzyWindowRunes(pathRunes, queryRunes []rune, startAt int, caseSensitive bool) (int, int, bool) {
	if startAt < 0 {
		startAt = 0
	}
	if len(queryRunes) == 0 || startAt > len(pathRunes) || len(queryRunes) > len(pathRunes)-startAt {
		return 0, 0, false
	}
	next := 0
	first, last := -1, -1
	for i := startAt; i < len(pathRunes) && next < len(queryRunes); i++ {
		if runesEqual(pathRunes[i], queryRunes[next], caseSensitive) {
			if next == 0 {
				first = i
			}
			last = i
			next++
		}
	}
	if next != len(queryRunes) {
		return 0, 0, false
	}
	end := last + 1
	// Keep the scoring window tight but include later final-token positions that
	// may produce a stronger fzy alignment.
	for i := last + 1; i < len(pathRunes); i++ {
		if runesEqual(pathRunes[i], queryRunes[len(queryRunes)-1], caseSensitive) {
			end = i + 1
		}
	}
	return first, end, true
}

func scoreGreedyFzyASCII(path, query string, startAt int, caseSensitive bool) (int, int, int, bool) {
	score, first, last := 0, -1, -1
	for i := 0; i < len(query); i++ {
		nextStart := startAt
		if last >= 0 {
			nextStart = last + 1
		}
		idx := findNextASCII(path, query[i], nextStart, caseSensitive)
		if idx == -1 {
			return 0, 0, 0, false
		}
		prev := byte('/')
		if idx > 0 {
			prev = path[idx-1]
		}
		if first == -1 {
			first = idx
			score = idx*fzyGapLeading + fzyBonusASCII(prev, path[idx])
		} else if idx == last+1 {
			score = addFzyScore(score, fzyMatchConsecutive)
		} else {
			score = addFzyScore(score, (idx-last-1)*fzyGapInner+fzyBonusASCII(prev, path[idx]))
		}
		last = idx
	}
	score = addFzyScore(score, (len(path)-last-1)*fzyGapTrailing)
	return score, last - first + 1, componentOffsetASCII(path, first), true
}

func scoreGreedyFzyRunes(pathRunes, queryRunes []rune, startAt int, caseSensitive bool, wantPositions bool) (int, []int, int, int, bool) {
	score, first, last := 0, -1, -1
	var positions []int
	if wantPositions {
		positions = make([]int, 0, len(queryRunes))
	}
	for _, queryRune := range queryRunes {
		nextStart := startAt
		if last >= 0 {
			nextStart = last + 1
		}
		idx := findNext(pathRunes, queryRune, nextStart, caseSensitive)
		if idx == -1 {
			return 0, nil, 0, 0, false
		}
		prev := rune('/')
		if idx > 0 {
			prev = pathRunes[idx-1]
		}
		if first == -1 {
			first = idx
			score = idx*fzyGapLeading + fzyBonusRune(prev, pathRunes[idx])
		} else if idx == last+1 {
			score = addFzyScore(score, fzyMatchConsecutive)
		} else {
			score = addFzyScore(score, (idx-last-1)*fzyGapInner+fzyBonusRune(prev, pathRunes[idx]))
		}
		last = idx
		if wantPositions {
			positions = append(positions, idx)
		}
	}
	score = addFzyScore(score, (len(pathRunes)-last-1)*fzyGapTrailing)
	return score, positions, last - first + 1, componentOffsetRunes(pathRunes, first), true
}

func fzyBonusASCII(prev, cur byte) int {
	switch prev {
	case '/', '\\':
		return fzyMatchSlash
	case '-', '_', ' ':
		return fzyMatchWord
	case '.':
		return fzyMatchDot
	}
	if prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z' {
		return fzyMatchCapital
	}
	return 0
}

func fzyBonusRune(prev, cur rune) int {
	switch prev {
	case '/', '\\':
		return fzyMatchSlash
	case '-', '_', ' ':
		return fzyMatchWord
	case '.':
		return fzyMatchDot
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return fzyMatchCapital
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func addFzyScore(score, delta int) int {
	if score == fzyScoreMin {
		return fzyScoreMin
	}
	return score + delta
}

func contiguousPositions(start, length int) []int {
	positions := make([]int, length)
	for i := range positions {
		positions[i] = start + i
	}
	return positions
}

func scoreDisjointPositions(pathRunes []rune, positions []int, cursor int) int {
	score, last := 0, -1
	for _, idx := range positions {
		score += 10 + positionBonus(pathRunes, idx)
		if last >= 0 {
			gap := idx - last - 1
			if gap == 0 {
				score += 15
			} else {
				score -= gap
			}
		} else {
			score -= idx - cursor
		}
		last = idx
	}
	return score
}

func scoreDisjointPositionsASCII(path string, positions []int, cursor int) int {
	score, last := 0, -1
	for _, idx := range positions {
		score += 10 + positionBonusASCII(path, idx)
		if last >= 0 {
			gap := idx - last - 1
			if gap == 0 {
				score += 15
			} else {
				score -= gap
			}
		} else {
			score -= idx - cursor
		}
		last = idx
	}
	return score
}

func scoreContiguousRun(pathRunes []rune, start, length int) int {
	score, last := 0, -1
	for idx := start; idx < start+length; idx++ {
		score += 10 + positionBonus(pathRunes, idx)
		if last >= 0 {
			score += 15
		} else {
			score -= idx
		}
		last = idx
	}
	score -= len(pathRunes) - last - 1
	return score
}

func scoreContiguousRunASCII(path string, start, length int) int {
	score, last := 0, -1
	for idx := start; idx < start+length; idx++ {
		score += 10 + positionBonusASCII(path, idx)
		if last >= 0 {
			score += 15
		} else {
			score -= idx
		}
		last = idx
	}
	score -= len(path) - last - 1
	return score
}

func findNext(path []rune, query rune, start int, caseSensitive bool) int {
	if caseSensitive {
		for i := start; i < len(path); i++ {
			if path[i] == query {
				return i
			}
		}
		return -1
	}
	queryLower := unicode.ToLower(query)
	for i := start; i < len(path); i++ {
		if unicode.ToLower(path[i]) == queryLower {
			return i
		}
	}
	return -1
}

func findNextASCII(path string, query byte, start int, caseSensitive bool) int {
	if caseSensitive {
		for i := start; i < len(path); i++ {
			if path[i] == query {
				return i
			}
		}
		return -1
	}
	queryLower := asciiLower(query)
	for i := start; i < len(path); i++ {
		if asciiLower(path[i]) == queryLower {
			return i
		}
	}
	return -1
}

func runesEqual(pathRune, queryRune rune, caseSensitive bool) bool {
	if caseSensitive {
		return pathRune == queryRune
	}
	return unicode.ToLower(pathRune) == unicode.ToLower(queryRune)
}

func asciiEqual(pathByte, queryByte byte, caseSensitive bool) bool {
	if caseSensitive {
		return pathByte == queryByte
	}
	return asciiLower(pathByte) == asciiLower(queryByte)
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func naturalPathLess(a, b string) bool {
	return naturalPathCompare(a, b) < 0
}

func naturalPathCompare(a, b string) int {
	cmp, _ := naturalPathCompareNumeric(a, b)
	return cmp
}

func naturalPathCompareNumeric(a, b string) (int, bool) {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			nextI, nextJ := digitRunEnd(a, i), digitRunEnd(b, j)
			if cmp := compareDigitRuns(a[i:nextI], b[j:nextJ]); cmp != 0 {
				return cmp, true
			}
			i, j = nextI, nextJ
			continue
		}
		if a[i] != b[j] {
			if a[i] < b[j] {
				return -1, false
			}
			return 1, false
		}
		i++
		j++
	}
	switch {
	case i < len(a):
		return 1, false
	case j < len(b):
		return -1, false
	default:
		return 0, false
	}
}

func digitRunEnd(s string, start int) int {
	for start < len(s) && isDigit(s[start]) {
		start++
	}
	return start
}

func compareDigitRuns(a, b string) int {
	trimmedA := strings.TrimLeft(a, "0")
	trimmedB := strings.TrimLeft(b, "0")
	if trimmedA == "" {
		trimmedA = "0"
	}
	if trimmedB == "" {
		trimmedB = "0"
	}
	if len(trimmedA) != len(trimmedB) {
		if len(trimmedA) < len(trimmedB) {
			return -1
		}
		return 1
	}
	if trimmedA != trimmedB {
		if trimmedA < trimmedB {
			return -1
		}
		return 1
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return 0
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isASCIIDigitRune(r rune) bool {
	return r >= '0' && r <= '9'
}

func isAlphaNumRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isAlphaNumASCII(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func positionBonus(path []rune, idx int) int {
	if idx == 0 {
		return 20
	}
	prev := path[idx-1]
	cur := path[idx]
	switch prev {
	case '/', '\\':
		return 18
	case '-', '_', ' ', '.':
		return 12
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return 10
	}
	return 0
}

func positionBonusASCII(path string, idx int) int {
	if idx == 0 {
		return 20
	}
	prev := path[idx-1]
	cur := path[idx]
	switch prev {
	case '/', '\\':
		return 18
	case '-', '_', ' ', '.':
		return 12
	}
	if prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z' {
		return 10
	}
	return 0
}

func isASCIIString(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return false
		}
	}
	return true
}

func entryLess(a, b Entry, mode SortMode) bool {
	if mode == SortMTime && a.ModTimeNS != b.ModTimeNS {
		return a.ModTimeNS < b.ModTimeNS
	}
	return strings.Compare(a.Path, b.Path) < 0
}
