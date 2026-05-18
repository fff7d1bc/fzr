package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const benchmarkCorpusSize = 1_000_000

var (
	benchmarkCorpusOnce sync.Once
	benchmarkCorpus     []Entry
)

func BenchmarkRankEntries(b *testing.B) {
	for _, size := range []int{1_000, 10_000, 100_000, 1_000_000} {
		entries := benchmarkEntries(size)
		for _, query := range []string{
			"a",
			".dat",
			"alpha beta signal",
			"alpha beta signal 10",
			"alpha beta signal signal",
			"alpha beta 1080p 10",
			"alpha beta 1080p 1080p",
			"alpha beta 1080p 10 alpha",
			"AlphaBeta",
			"alphabeta1080p10",
			"bencharchiveitem",
			"not-present",
		} {
			b.Run(fmt.Sprintf("%d/%s", size, query), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					matches := rankEntries(entries, query, SortPath)
					if len(matches) == 0 && query != "not-present" {
						b.Fatalf("no matches for %q", query)
					}
				}
			})
		}
	}
}

func BenchmarkRankMatchesNarrowing(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		entries := benchmarkEntries(size)
		baseMatches := rankEntries(entries, "alpha beta", SortPath)
		b.Run(fmt.Sprintf("%d/append-signal", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				matches := rankMatches(baseMatches, "alpha beta signal", SortPath)
				if len(matches) == 0 {
					b.Fatal("no narrowed matches")
				}
			}
		})
	}
}

func BenchmarkEffectiveMatchesWindow(b *testing.B) {
	for _, size := range []int{10_000, 100_000} {
		entries := benchmarkEffectiveWindowEntries(size)
		b.Run(fmt.Sprintf("%d/alpha-beta", size), func(b *testing.B) {
			b.ReportAllocs()
			matches := rankEntries(entries, "alpha beta", SortPath)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				effective := effectiveMatches(matches, "alpha beta")
				if len(effective) == 0 || len(effective) > effectiveMixedWindowMatches {
					b.Fatalf("unexpected effective match count: %d", len(effective))
				}
			}
		})
	}
}

func BenchmarkPickerApplyQuery(b *testing.B) {
	for _, size := range []int{1_000, 10_000, 100_000, 1_000_000} {
		entries := benchmarkEntries(size)
		b.Run(fmt.Sprintf("%d/full", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				model := benchmarkPickerModel(entries)
				model.query = []rune("alpha beta signal")
				model.queryCursor = len(model.query)
				model.queryDirty = true
				model.applyQuery()
				if len(model.matches) == 0 {
					b.Fatal("no matches")
				}
			}
		})
		b.Run(fmt.Sprintf("%d/narrow", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				model := benchmarkPickerModel(entries)
				model.query = []rune("alpha beta")
				model.queryCursor = len(model.query)
				model.queryDirty = true
				model.applyQuery()
				model.appendRune(' ')
				for _, r := range "signal" {
					model.appendRune(r)
				}
				model.applyQuery()
				if len(model.matches) == 0 {
					b.Fatal("no matches")
				}
			}
		})
	}
}

func BenchmarkRenderPickerVisibleRows(b *testing.B) {
	entries := benchmarkEntries(100_000)
	model := benchmarkPickerModel(entries)
	model.query = []rune("alpha beta signal")
	model.queryCursor = len(model.query)
	model.queryDirty = true
	model.applyQuery()
	model.scanning = false

	var buf bytes.Buffer
	theme := pickerThemeForColor(true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		renderPicker(&buf, model, 120, theme)
	}
}

func BenchmarkCollectEntriesTempTree(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 2_000; i++ {
		path := filepath.Join(root, fmt.Sprintf("dir-%03d", i%100), fmt.Sprintf("file-%05d.dat", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, err := collectEntries(context.Background(), ScanOptions{
			Root:       root,
			TypeFilter: FilterAll,
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(entries) == 0 {
			b.Fatal("no entries")
		}
	}
}

func BenchmarkCollectEntriesRealRoot(b *testing.B) {
	root := os.Getenv("FZR_BENCH_ROOT")
	if root == "" {
		b.Skip("set FZR_BENCH_ROOT=/path to benchmark a real directory tree")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		entries, err := collectEntries(context.Background(), ScanOptions{
			Root:       root,
			TypeFilter: FilterAll,
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(entries) == 0 {
			b.Fatal("no entries")
		}
	}
}

func benchmarkPickerModel(entries []Entry) *pickerModel {
	model := newPickerModel(SortPath)
	model.scanning = false
	model.addEntries(entries)
	return model
}

func benchmarkEntries(count int) []Entry {
	if count > benchmarkCorpusSize {
		panic(fmt.Sprintf("benchmark entry count %d exceeds corpus size %d", count, benchmarkCorpusSize))
	}
	benchmarkCorpusOnce.Do(func() {
		benchmarkCorpus = generateBenchmarkEntries(benchmarkCorpusSize)
	})
	return benchmarkCorpus[:count]
}

func generateBenchmarkEntries(count int) []Entry {
	entries := make([]Entry, count)
	for i := range entries {
		entries[i] = syntheticBenchmarkEntry(i)
	}
	return entries
}

func syntheticBenchmarkEntry(i int) Entry {
	switch {
	case i%1_000 == 0:
		return Entry{
			Path: fmt.Sprintf("benchroot/alpha/beta/signal/quality-1080p/item-%02d/quality-1080p/AlphaBetaSignal-%06d.dat", i%24+1, i),
			Type: TypeFile,
		}
	case i%257 == 0:
		return Entry{
			Path: fmt.Sprintf("benchroot/alpha/beta/group-%04d/node-%03d/record-%06d.log", i%4096, i%113, i),
			Type: TypeFile,
		}
	case i%97 == 0:
		return Entry{
			Path: fmt.Sprintf("benchroot/deep/%02d/%02d/%02d/%02d/%02d/fragment-%06d.bin", i%10, i%13, i%17, i%19, i%23, i),
			Type: TypeFile,
		}
	case i%31 == 0:
		return Entry{
			Path: fmt.Sprintf("benchroot/project-%03d/component-%03d/testdata/sample-%06d.json", i%400, i%100, i),
			Type: TypeFile,
		}
	case i%17 == 0:
		return Entry{
			Path: fmt.Sprintf("benchroot/zones/zone-%04d/segment-%02d/", i%2000, i%8+1),
			Type: TypeDir,
		}
	default:
		return Entry{
			Path: fmt.Sprintf("benchroot/archive/lane-%03d/bucket-%03d/item-%07d.txt", i%500, i%300, i),
			Type: TypeFile,
		}
	}
}

func benchmarkEffectiveWindowEntries(count int) []Entry {
	entries := make([]Entry, count)
	for i := range entries {
		if i%20 == 0 {
			entries[i] = Entry{
				Path: fmt.Sprintf("benchroot/alpha/beta/strong-%06d.dat", i),
				Type: TypeFile,
			}
			continue
		}
		entries[i] = Entry{
			Path: fmt.Sprintf("benchroot/a-l-p-h-a/b-e-t-a/weak-%06d.dat", i),
			Type: TypeFile,
		}
	}
	return entries
}
