package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestScorePathMatchesCaseInsensitively(t *testing.T) {
	_, ok := scorePathForQueryPlan("Src/FooBar.go", makeQueryPlan("sfb"), false)
	if !ok {
		t.Fatal("expected query to match path case-insensitively")
	}
}

func TestScorePathWithCaseRejectsCaseMismatch(t *testing.T) {
	if _, ok := scorePathForQueryPlan("Src/FooBar.go", makeQueryPlan("sfb"), true); ok {
		t.Fatal("expected case-sensitive query to reject mismatched case")
	}
	if _, ok := scorePathForQueryPlan("Src/FooBar.go", makeQueryPlan("SFB"), true); !ok {
		t.Fatal("expected case-sensitive query to match exact case")
	}
}

func TestRankEntriesWithOptionsContextStopsWhenCanceled(t *testing.T) {
	entries := make([]Entry, 2048)
	for i := range entries {
		entries[i] = Entry{Path: "alpha/beta/gamma.txt", Type: TypeFile}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	matches, ok := rankEntriesWithOptionsContext(ctx, entries, "alpha", SortPath, false)
	if ok {
		t.Fatal("rankEntriesWithOptionsContext ok = true, want false")
	}
	if matches != nil {
		t.Fatalf("matches = %#v, want nil after cancellation", matches)
	}
}

func TestRankMatchesWithOptionsContextStopsWhenCanceled(t *testing.T) {
	matches := make([]Match, 2048)
	for i := range matches {
		matches[i] = Match{Entry: Entry{Path: "alpha/beta/gamma.txt", Type: TypeFile}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	filtered, ok := rankMatchesWithOptionsContext(ctx, matches, "alpha", SortPath, false)
	if ok {
		t.Fatal("rankMatchesWithOptionsContext ok = true, want false")
	}
	if filtered != nil {
		t.Fatalf("matches = %#v, want nil after cancellation", filtered)
	}
}

func TestRankEntriesPrefersBoundariesAndConsecutiveMatches(t *testing.T) {
	entries := []Entry{
		{Path: "foo/ab.go"},
		{Path: "foo/a-b.go"},
		{Path: "foo/alpha/beta.go"},
	}

	matches := rankEntries(entries, "ab", SortPath)
	got := matchPaths(matches)
	want := []string{"foo/ab.go", "foo/alpha/beta.go", "foo/a-b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesGluedQueryPrefersFzyLikeExtensionAlignment(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/CAN_mounts/CAD/EBB36 controller mount strain relief.f3d"},
		{Path: "fixtures/CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/"},
		{Path: "fixtures/CAN_mounts/STLs/BTT_EBB36_1.0_mounts_with_strain_relief/"},
		{Path: "fixtures/CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/EBB36 controller mount.stl"},
		{Path: "fixtures/CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/EBB36 compact mount.stl"},
	}

	matches := rankEntries(entries, "canmountebb36.stl", SortPath)
	got := matchPaths(matches)
	wantPrefix := []string{
		"fixtures/CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/EBB36 compact mount.stl",
		"fixtures/CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/EBB36 controller mount.stl",
	}
	if len(got) < len(wantPrefix) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("ranked paths = %#v, want .stl files first %#v", got, wantPrefix)
	}
}

func TestRankEntriesGluedQueryUsesFzyStyleScoring(t *testing.T) {
	tests := []struct {
		query string
		paths []string
		want  []string
	}{
		{
			query: "amor",
			paths: []string{
				"app/models/zrder",
				"app/models/order",
			},
			want: []string{
				"app/models/order",
				"app/models/zrder",
			},
		},
		{
			query: "gemfil",
			paths: []string{
				"Gemfile.lock",
				"Gemfile",
			},
			want: []string{
				"Gemfile",
				"Gemfile.lock",
			},
		},
		{
			query: "test",
			paths: []string{
				"testing",
				"tests",
			},
			want: []string{
				"tests",
				"testing",
			},
		},
	}

	for _, tt := range tests {
		entries := make([]Entry, 0, len(tt.paths))
		for _, path := range tt.paths {
			entries = append(entries, Entry{Path: path})
		}
		got := matchPaths(rankEntries(entries, tt.query, SortPath))
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("query %q ranked paths = %#v, want %#v", tt.query, got, tt.want)
		}
	}
}

func TestRankEntriesWindowedFzyScoresLongPaths(t *testing.T) {
	prefix := strings.Repeat("synthetic/archive/segment/", 60)
	entries := []Entry{
		{Path: prefix + "CAN_mounts/CAD/EBB36 controller mount strain relief.f3d"},
		{Path: prefix + "CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/"},
		{Path: prefix + "CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/EBB36 compact mount.stl"},
	}

	matches := rankEntries(entries, "canmountebb36.stl", SortPath)
	got := matchPaths(matches)
	wantPrefix := []string{
		prefix + "CAN_mounts/STLs/BTT_EBB36_1.0_mounts_plain/EBB36 compact mount.stl",
	}
	if len(got) < len(wantPrefix) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("ranked paths = %#v, want .stl file first %#v", got, wantPrefix)
	}
}

func TestScoreFzyASCIIFallsBackForOversizedWindow(t *testing.T) {
	path := strings.Repeat("a", 1500) + "/needle/" + strings.Repeat("b", 1500) + "/target.dat"
	query := strings.Repeat("a", 200) + "target"

	score, span, _, ok := scoreFzyASCII(path, query, false)
	if !ok {
		t.Fatal("expected oversized fuzzy query to match")
	}
	if score == fzyScoreMin {
		t.Fatalf("score = %d, want fallback score instead of minimum sentinel", score)
	}
	if span <= len(query) {
		t.Fatalf("span = %d, want a fuzzy span larger than query length", span)
	}
}

func TestRankEntriesEmptyQueryUsesFallbackSort(t *testing.T) {
	entries := []Entry{
		{Path: "b"},
		{Path: "a"},
	}

	matches := rankEntries(entries, "", SortPath)
	got := matchPaths(matches)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenQueryRequiresEachTokenIndependently(t *testing.T) {
	entries := []Entry{
		{Path: "movies/exampletitle.mkv"},
		{Path: "movies/exampletitle.txt"},
		{Path: "movies/other.mkv"},
	}

	matches := rankEntries(entries, ".mkv example", SortPath)
	got := matchPaths(matches)
	want := []string{"movies/exampletitle.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenQueryRanksByCombinedQuery(t *testing.T) {
	entries := []Entry{
		{Path: "movies/long-prefix-foo.mkv"},
		{Path: "movies/foo.mkv"},
	}

	matches := rankEntries(entries, ".mkv foo", SortPath)
	got := matchPaths(matches)
	want := []string{"movies/foo.mkv", "movies/long-prefix-foo.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSplitsActiveQueryIntoRequiredTokens(t *testing.T) {
	entries := []Entry{
		{Path: "movies/foo-only.mkv"},
		{Path: "movies/bar-only.mkv"},
		{Path: "movies/foo/bar.mkv"},
	}

	matches := rankEntries(entries, "foo bar", SortPath)
	got := matchPaths(matches)
	want := []string{"movies/foo/bar.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenQueryBehavesLikeIndependentStages(t *testing.T) {
	entries := []Entry{
		{Path: "movies/foo/long-prefix-bar.mkv"},
		{Path: "movies/foo/bar.mkv"},
		{Path: "movies/bar-only.mkv"},
		{Path: "movies/foo-only.mkv"},
	}

	got := matchPaths(rankEntries(entries, "foo bar", SortPath))
	want := []string{"movies/foo/bar.mkv", "movies/foo/long-prefix-bar.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("token ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenQueryMatchesAcrossSeparators(t *testing.T) {
	entries := []Entry{
		{Path: "samples/AlphaBeta.dat"},
		{Path: "samples/Alpha.dat"},
	}

	matches := rankEntries(entries, "Alpha Beta", SortPath)
	got := matchPaths(matches)
	want := []string{"samples/AlphaBeta.dat"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenQueryFollowsCaseSensitiveMode(t *testing.T) {
	entries := []Entry{
		{Path: "shows/Foo/bar.mkv"},
		{Path: "shows/Foo/Bar.mkv"},
	}

	matches := rankEntriesWithOptions(entries, "Foo Bar", SortPath, true)
	got := matchPaths(matches)
	want := []string{"shows/Foo/Bar.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case-sensitive ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenOrderDoesNotDriveRanking(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/tool/compact/signal_mount.mesh"},
		{Path: "fixtures/tool/large/signal_board_mounts/spacer.mesh"},
		{Path: "fixtures/tool/mount/signal.txt"},
	}

	for _, query := range []string{"signal mount .mesh", ".mesh signal mount"} {
		matches := rankEntries(entries, query, SortPath)
		got := matchPaths(matches)
		want := []string{
			"fixtures/tool/compact/signal_mount.mesh",
			"fixtures/tool/large/signal_board_mounts/spacer.mesh",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("query %q ranked paths = %#v, want %#v", query, got, want)
		}
	}
}

func TestRankEntriesPrefersStrongTokenMatchesOverScatteredFuzzyMatches(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/a/l/p/h/a/b/e/t/a/s/i/g/n/a/l.txt"},
		{Path: "fixtures/alpha/beta/signal/"},
		{Path: "fixtures/alpha/beta/signal/alpha-beta-signal-01.dat"},
	}

	got := matchPaths(rankEntries(entries, "alpha beta signal", SortPath))
	want := []string{
		"fixtures/alpha/beta/signal/",
		"fixtures/alpha/beta/signal/alpha-beta-signal-01.dat",
		"fixtures/a/l/p/h/a/b/e/t/a/s/i/g/n/a/l.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesTokenQueryKeepsRankingInfluenceAcrossTokens(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/alpha/beta/q-u-i-e-t/s-i-g-n-a-l/example-series-01.dat"},
		{Path: "fixtures/alpha/beta/signal/alpha-beta-signal-01.dat"},
	}

	matches := rankEntries(entries, "alpha beta signal .dat", SortPath)
	got := matchPaths(matches)
	want := []string{
		"fixtures/alpha/beta/signal/alpha-beta-signal-01.dat",
		"fixtures/alpha/beta/q-u-i-e-t/s-i-g-n-a-l/example-series-01.dat",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesPrefersLaterUnusedMatchForTrailingToken(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/series/quality-1080p/item-01/quality-1080p.dat"},
		{Path: "fixtures/series/quality-1080p/item-10/quality-1080p.dat"},
	}

	matches := rankEntries(entries, "quality 1080p 10", SortPath)
	got := matchPaths(matches)
	want := []string{
		"fixtures/series/quality-1080p/item-10/quality-1080p.dat",
		"fixtures/series/quality-1080p/item-01/quality-1080p.dat",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesPrefersSeparateRepeatedTokenOccurrences(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/series/quality-1080p/item-01.dat"},
		{Path: "fixtures/series/quality-1080p/item-1080p.dat"},
	}

	matches := rankEntries(entries, "quality 1080p 1080p", SortPath)
	got := matchPaths(matches)
	want := []string{
		"fixtures/series/quality-1080p/item-1080p.dat",
		"fixtures/series/quality-1080p/item-01.dat",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesKeepsFallbackWhenRepeatedTokenCannotBeDisjoint(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/series/quality-1080p/item-01.dat"},
	}

	matches := rankEntries(entries, "quality 1080p 1080p", SortPath)
	got := matchPaths(matches)
	want := []string{"fixtures/series/quality-1080p/item-01.dat"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want fallback match %#v", got, want)
	}
}

func TestRankEntriesPrefersFileWhenLaterTokenExtendsDisjointChain(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/series/alpha-title/quality-1080p/"},
		{Path: "fixtures/series/alpha-title/quality-1080p/alpha-title-10-quality-1080p.dat"},
	}

	matches := rankEntries(entries, "alpha quality 1080p 10 alpha", SortPath)
	got := matchPaths(matches)
	want := []string{
		"fixtures/series/alpha-title/quality-1080p/alpha-title-10-quality-1080p.dat",
		"fixtures/series/alpha-title/quality-1080p/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want file before parent directory %#v", got, want)
	}
}

func TestRankEntriesPrefersBoundedNumericTokenOverEmbeddedLaterToken(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/series/quality-1080p/item-09/alpha-title-quality-1080p.dat"},
		{Path: "fixtures/series/quality-1080p/item-10/alpha-title-quality-1080p.dat"},
		{Path: "fixtures/series/quality-1080p/item-11/alpha-title-quality-1080p.dat"},
	}

	matches := rankEntries(entries, "quality 1080p 10 alpha", SortPath)
	got := matchPaths(matches)
	want := []string{
		"fixtures/series/quality-1080p/item-10/alpha-title-quality-1080p.dat",
		"fixtures/series/quality-1080p/item-09/alpha-title-quality-1080p.dat",
		"fixtures/series/quality-1080p/item-11/alpha-title-quality-1080p.dat",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want bounded numeric token first %#v", got, want)
	}
}

func TestRankEntriesUsesBestSubstringOccurrenceForOutOfOrderTrailingToken(t *testing.T) {
	entries := []Entry{
		{Path: "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 01 (BD 1080p).mkv"},
		{Path: "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 08 (BD 1080p).mkv"},
		{Path: "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 09 (BD 1080p).mkv"},
	}

	matches := rankEntries(entries, "catalog done alpha beta mkv 08", SortPath)
	got := matchPaths(matches)
	want := []string{
		"synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 08 (BD 1080p).mkv",
		"synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 01 (BD 1080p).mkv",
		"synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 09 (BD 1080p).mkv",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want episode 08 first %#v", got, want)
	}
}

func TestRankEntriesPrefersBoundedNumericSuffixInGluedQuery(t *testing.T) {
	entries := []Entry{
		{Path: "synthetic/catalog/done/_pack/Alpha Beta S1 (BD 1080p)/Alpha Beta - 02 (BD 1080p).mkv"},
		{Path: "synthetic/catalog/done/_pack/Alpha Beta S1 (BD 1080p)/Alpha Beta - 10 (BD 1080p).mkv"},
	}

	matches := rankEntries(entries, "catadonealphabetas11080p10", SortPath)
	got := matchPaths(matches)
	want := []string{
		"synthetic/catalog/done/_pack/Alpha Beta S1 (BD 1080p)/Alpha Beta - 10 (BD 1080p).mkv",
		"synthetic/catalog/done/_pack/Alpha Beta S1 (BD 1080p)/Alpha Beta - 02 (BD 1080p).mkv",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want episode 10 first %#v", got, want)
	}
}

func TestRankEntriesPrefersEpisodeNumberForGluedNumericSuffixWithoutQualityToken(t *testing.T) {
	entries := []Entry{
		{Path: "fixtures/media/archive/Orbit Signal S1 (BD 1080p)/Orbit Signal - 02 (BD 1080p).mkv"},
		{Path: "fixtures/media/archive/Orbit Signal S1 (BD 1080p)/Orbit Signal - 10 (BD 1080p).mkv"},
	}

	matches := rankEntries(entries, "fixmedarchorbsig1010", SortPath)
	got := matchPaths(matches)
	want := []string{
		"fixtures/media/archive/Orbit Signal S1 (BD 1080p)/Orbit Signal - 10 (BD 1080p).mkv",
		"fixtures/media/archive/Orbit Signal S1 (BD 1080p)/Orbit Signal - 02 (BD 1080p).mkv",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want episode 10 first %#v", got, want)
	}
}

func TestRankEntriesRejectsScatteredNumericToken(t *testing.T) {
	entries := []Entry{
		{Path: "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 10 (BD 1080p) [4763C209].mkv"},
	}

	matches := rankEntries(entries, "catalog done alpha beta mkv 09 86", SortPath)
	if len(matches) != 0 {
		t.Fatalf("matches = %#v, want none for scattered numeric token", matchPaths(matches))
	}
}

func TestRankEntriesWithOptionsHonorsCaseSensitiveMode(t *testing.T) {
	entries := []Entry{
		{Path: "src/Foo.go"},
		{Path: "src/foo.go"},
	}

	matches := rankEntriesWithOptions(entries, "foo", SortPath, true)
	got := matchPaths(matches)
	want := []string{"src/foo.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesPrioritizesSubstringMatchesOverScatteredFuzzyMatches(t *testing.T) {
	entries := []Entry{
		{Path: "archive/Waaa/i/t/c/h.mkv"},
		{Path: "shows/TheWitcher.mkv"},
	}

	matches := rankEntries(entries, "Witch", SortPath)
	got := matchPaths(matches)
	want := []string{"shows/TheWitcher.mkv", "archive/Waaa/i/t/c/h.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSubstringPriorityFollowsCaseMode(t *testing.T) {
	entries := []Entry{
		{Path: "shows/witch.mkv"},
		{Path: "shows/Witch.mkv"},
		{Path: "shows/Waaa/i/t/c/h.mkv"},
	}

	matches := rankEntriesWithOptions(entries, "Witch", SortPath, false)
	got := matchPaths(matches)
	want := []string{"shows/Witch.mkv", "shows/witch.mkv", "shows/Waaa/i/t/c/h.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case-insensitive ranked paths = %#v, want %#v", got, want)
	}

	matches = rankEntriesWithOptions(entries, "Witch", SortPath, true)
	got = matchPaths(matches)
	want = []string{"shows/Witch.mkv", "shows/Waaa/i/t/c/h.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case-sensitive ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSubstringPriorityUsesWholePath(t *testing.T) {
	entries := []Entry{
		{Path: "Witch/aaa.mkv"},
		{Path: "shows/Waaa/i/t/c/h.mkv"},
	}

	matches := rankEntries(entries, "Witch", SortPath)
	got := matchPaths(matches)
	want := []string{"Witch/aaa.mkv", "shows/Waaa/i/t/c/h.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSubstringPriorityUsesCurrentScoreAcrossComponents(t *testing.T) {
	entries := []Entry{
		{Path: "Witch/aaa/bbb.mkv"},
		{Path: "shows/Witch/bbb.mkv"},
		{Path: "shows/foo/Witch.mkv"},
	}

	matches := rankEntries(entries, "Witch", SortPath)
	got := matchPaths(matches)
	want := []string{"Witch/aaa/bbb.mkv", "shows/Witch/bbb.mkv", "shows/foo/Witch.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSubstringPriorityKeepsCurrentScoreWithinSameComponent(t *testing.T) {
	entries := []Entry{
		{Path: "shows/TheWitcher.mkv"},
		{Path: "shows/Witch.mkv"},
	}

	matches := rankEntries(entries, "Witch", SortPath)
	got := matchPaths(matches)
	want := []string{"shows/Witch.mkv", "shows/TheWitcher.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSubstringStillBeatsFuzzyRun(t *testing.T) {
	entries := []Entry{
		{Path: "samples/Alpha Beta.dat"},
		{Path: "samples/AlphaBeta.dat"},
	}

	matches := rankEntries(entries, "AlphaBeta", SortPath)
	got := matchPaths(matches)
	want := []string{"samples/AlphaBeta.dat", "samples/Alpha Beta.dat"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked paths = %#v, want %#v", got, want)
	}
}

func TestRankEntriesSubstringPriorityFollowsCaseSensitiveMode(t *testing.T) {
	entries := []Entry{
		{Path: "Witch/aaa.mkv"},
		{Path: "shows/witch.mkv"},
	}

	matches := rankEntriesWithOptions(entries, "Witch", SortPath, true)
	got := matchPaths(matches)
	want := []string{"Witch/aaa.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case-sensitive ranked paths = %#v, want %#v", got, want)
	}
}

func TestScorePathRejectsNonSubsequence(t *testing.T) {
	if _, ok := scorePathForQueryPlan("abc", makeQueryPlan("acx"), false); ok {
		t.Fatal("expected non-subsequence query to be rejected")
	}
}

func TestMatchPositionsCaseInsensitive(t *testing.T) {
	positions, ok := matchPositions("Src/FooBar.go", "sfb")
	if !ok {
		t.Fatal("expected query to match path")
	}
	want := []int{0, 4, 7}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want %#v", positions, want)
	}
}

func TestMatchPositionsPrefersContiguousSubstring(t *testing.T) {
	positions, ok := matchPositions("Waaa/i/t/c/h/Witch.mkv", "Witch")
	if !ok {
		t.Fatal("expected query to match path")
	}
	want := []int{13, 14, 15, 16, 17}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want contiguous substring %#v", positions, want)
	}
}

func TestMatchPositionsWithCaseHonorsCaseSensitiveMode(t *testing.T) {
	positions, ok := matchPositionsWithCase("Src/FooBar.go", "SFB", true)
	if !ok {
		t.Fatal("expected exact-case query to match path")
	}
	want := []int{0, 4, 7}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want %#v", positions, want)
	}
	if _, ok := matchPositionsWithCase("Src/FooBar.go", "sfb", true); ok {
		t.Fatal("expected lower-case query to fail in case-sensitive mode")
	}
}

func TestMatchPositionsEmptyQueryReturnsNoPositions(t *testing.T) {
	positions, ok := matchPositions("alpha", "")
	if !ok {
		t.Fatal("expected empty query to match")
	}
	if len(positions) != 0 {
		t.Fatalf("positions = %#v, want none", positions)
	}
}

func TestMatchPositionsForQueriesCombinesMatchedCharacters(t *testing.T) {
	positions, ok := matchPositionsForQueries("foo/bar.mkv", []string{"fb", ".mkv"})
	if !ok {
		t.Fatal("expected queries to match path")
	}
	want := []int{0, 4, 7, 8, 9, 10}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want %#v", positions, want)
	}
}

func TestMatchPositionsForQueriesSplitsTokens(t *testing.T) {
	positions, ok := matchPositionsForQueries("foo/x/bar.mkv", []string{"foo bar"})
	if !ok {
		t.Fatal("expected token query to match path")
	}
	want := []int{0, 1, 2, 6, 7, 8}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want %#v", positions, want)
	}
}

func TestMatchPositionsForQueriesHighlightsUnorderedTokensIndependently(t *testing.T) {
	path := "fixtures/signal/mount/signal.txt"
	positions, ok := matchPositionsForQueries(path, []string{"mount signal"})
	if !ok {
		t.Fatal("expected token query to match path")
	}

	firstSignal := strings.Index(path, "signal")
	lastSignal := strings.LastIndex(path, "signal")
	mount := strings.Index(path, "mount")
	if firstSignal == -1 || lastSignal == -1 || mount == -1 || firstSignal == lastSignal {
		t.Fatal("test path missing expected token layout")
	}
	if !containsPositions(positions, contiguousPositions(firstSignal, len("signal"))) {
		t.Fatalf("positions = %#v, want first signal highlighted", positions)
	}
	if containsPositions(positions, contiguousPositions(lastSignal, len("signal"))) {
		t.Fatalf("positions = %#v, want unordered query to avoid forced later signal", positions)
	}
	if !containsPositions(positions, contiguousPositions(mount, len("mount"))) {
		t.Fatalf("positions = %#v, want mount highlighted", positions)
	}
}

func TestMatchPositionsForQueriesPrefersSeparateRepeatedOccurrences(t *testing.T) {
	positions, ok := matchPositionsForQueries("fixtures/quality-1080p/item-1080p.dat", []string{"1080p 1080p"})
	if !ok {
		t.Fatal("expected repeated tokens to match path")
	}
	want := []int{17, 18, 19, 20, 21, 28, 29, 30, 31, 32}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want separate occurrences %#v", positions, want)
	}
}

func TestMatchPositionsForQueriesPrefersEpisodeLikeTrailingToken(t *testing.T) {
	positions, ok := matchPositionsForQueries("fixtures/quality-1080p/item-10/quality-1080p.dat", []string{"1080p 10"})
	if !ok {
		t.Fatal("expected query to match path")
	}
	want := []int{17, 18, 19, 20, 21, 28, 29}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want episode-like trailing token %#v", positions, want)
	}
}

func TestMatchPositionsForQueriesKeepsEpisodeTokenBeforeRepeatedLaterToken(t *testing.T) {
	positions, ok := matchPositionsForQueries("fixtures/alpha-title/quality-1080p/alpha-title-10-quality-1080p.dat", []string{"1080p 10 alpha"})
	if !ok {
		t.Fatal("expected query to match path")
	}
	want := []int{9, 10, 11, 12, 13, 29, 30, 31, 32, 33, 47, 48}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want all token matches with episode token %#v", positions, want)
	}
}

func TestMatchPositionsForQueriesHighlightsBestOutOfOrderTrailingToken(t *testing.T) {
	path := "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 08 (BD 1080p).mkv"
	positions, ok := matchPositionsForQueries(path, []string{"catalog done alpha beta mkv 08"})
	if !ok {
		t.Fatal("expected query to match path")
	}
	want := []int{
		10, 11, 12, 13, 14, 15, 16,
		18, 19, 20, 21,
		29, 30, 31, 32, 33,
		35, 36, 37, 38,
		56, 57,
		70, 71, 72,
	}
	if !reflect.DeepEqual(positions, want) {
		t.Fatalf("positions = %#v, want episode 08 highlighted %#v", positions, want)
	}
}

func TestMatchPositionsRejectsScatteredNumericToken(t *testing.T) {
	path := "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 10 (BD 1080p) [4763C209].mkv"
	if positions, ok := matchPositionsForQueries(path, []string{"catalog done alpha beta mkv 09 86"}); ok {
		t.Fatalf("positions = %#v, want no match for scattered numeric token", positions)
	}
}

func TestMatchPositionsPrefersBoundedNumericSuffixInGluedQuery(t *testing.T) {
	path := "synthetic/catalog/done/_pack/Alpha Beta S1 (BD 1080p)/Alpha Beta - 10 (BD 1080p).mkv"
	positions, ok := matchPositions(path, "catadonealphabetas11080p10")
	if !ok {
		t.Fatal("expected glued query to match path")
	}
	if !containsPositions(positions, []int{67, 68}) {
		t.Fatalf("positions = %#v, want episode 10 highlighted", positions)
	}
}

func TestMatchPositionsPreferEpisodeNumberForGluedNumericSuffixWithoutQualityToken(t *testing.T) {
	path := "fixtures/media/archive/Orbit Signal S1 (BD 1080p)/Orbit Signal - 10 (BD 1080p).mkv"
	positions, ok := matchPositions(path, "fixmedarchorbsig1010")
	if !ok {
		t.Fatal("expected glued query to match path")
	}
	episodeStart := strings.Index(path, " - 10 ")
	if episodeStart == -1 {
		t.Fatal("test path missing episode number")
	}
	want := []int{episodeStart + 3, episodeStart + 4}
	if !containsPositions(positions, want) {
		t.Fatalf("positions = %#v, want episode 10 highlighted", positions)
	}
}

func TestMatchPositionsForQueriesHighlightsAllRequiredTokens(t *testing.T) {
	path := "synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 08 (BD 1080p).mkv"
	query := "catalog done alpha beta .mkv 08"

	positions, ok := matchPositionsForQueries(path, []string{query})
	if !ok {
		t.Fatal("expected query to match path")
	}
	for _, token := range strings.Fields(query) {
		if !positionsCoverToken(path, positions, token) {
			t.Fatalf("positions %#v do not cover token %q in %q", positions, token, path)
		}
	}
}

func TestRankAndHighlightAcceptanceStayConsistent(t *testing.T) {
	paths := []string{
		"synthetic/catalog/done/_pack/Alpha Beta S1/",
		"synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 08 (BD 1080p).mkv",
		"synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 09 (BD 1080p).mkv",
		"synthetic/catalog/done/_pack/Alpha Beta S1/Alpha Beta - 10 (BD 1080p) [4763C209].mkv",
		"synthetic/catalog/done/_pack/Gamma Delta S1/Gamma Delta - 01 (BD 1080p).mkv",
		"fixtures/quality-1080p/item-1080p.dat",
		"fixtures/alpha/beta/signal/alpha-beta-signal-01.dat",
		"fixtures/a-l-p-h-a/b-e-t-a/weak-001.dat",
	}
	queries := []string{
		"catalog done alpha beta mkv 08",
		"catalog done alpha beta .mkv 09 86",
		"quality 1080p 1080p",
		"alpha beta signal .dat",
		"AlphaBeta",
		"Witch",
	}

	for _, query := range queries {
		plan := makeQueryPlan(query)
		for _, path := range paths {
			_, ranked := scorePathForQueryPlan(path, plan, false)
			positions, highlighted := matchPositionsForQueries(path, []string{query})
			if ranked != highlighted {
				t.Fatalf("query %q path %q ranked=%v highlighted=%v positions=%#v", query, path, ranked, highlighted, positions)
			}
			if !ranked {
				continue
			}
			for _, token := range strings.Fields(query) {
				if !positionsCoverToken(path, positions, token) {
					t.Fatalf("query %q path %q positions %#v do not cover token %q", query, path, positions, token)
				}
			}
		}
	}
}

func positionsCoverToken(path string, positions []int, token string) bool {
	pathRunes := []rune(path)
	positionSet := make(map[int]struct{}, len(positions))
	for _, position := range positions {
		positionSet[position] = struct{}{}
	}
	tokenRunes := []rune(token)
	next := 0
	for i, pathRune := range pathRunes {
		if _, ok := positionSet[i]; !ok {
			continue
		}
		if runesEqual(pathRune, tokenRunes[next], false) {
			next++
			if next == len(tokenRunes) {
				return true
			}
		}
	}
	return len(tokenRunes) == 0
}

func containsPositions(positions, want []int) bool {
	positionSet := make(map[int]struct{}, len(positions))
	for _, position := range positions {
		positionSet[position] = struct{}{}
	}
	for _, position := range want {
		if _, ok := positionSet[position]; !ok {
			return false
		}
	}
	return true
}

func matchPaths(matches []Match) []string {
	paths := make([]string, len(matches))
	for i, match := range matches {
		paths[i] = match.Entry.Path
	}
	return paths
}
