package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type config struct {
	root          string
	interactive   bool
	typeFilter    TypeFilter
	sortMode      SortMode
	caseSensitive bool
	ignoreCommon  bool
	ignoredNames  stringListFlag
	evalShell     string
	showHelp      bool
	followLinks   bool
	matchStyle    matchStyle
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return fmt.Sprint([]string(*f))
}

func (f *stringListFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("ignore name cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		var signalErr *terminationSignalError
		if errors.As(err, &signalErr) {
			reraiseSignal(signalErr.signal)
		}
		if errors.Is(err, errPickerCanceled) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func reraiseSignal(received os.Signal) {
	sig, ok := received.(syscall.Signal)
	if !ok {
		os.Exit(1)
	}
	// Signal delivery was intercepted only long enough to restore the terminal.
	// Reset before signaling ourselves so the shell observes normal signal exit
	// semantics instead of a generic fzr error.
	signal.Reset(sig)
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Go's signal machinery may deliver the reset signal after Kill returns. Give
	// it time to terminate the process, then keep the conventional numeric status
	// as a fallback if the platform delays or suppresses self-delivery.
	time.Sleep(time.Second)
	os.Exit(128 + int(sig))
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		printUsage(stdout)
		return nil
	}

	cfg, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if cfg.evalShell != "" {
		fmt.Fprint(stdout, zshIntegrationScript)
		return nil
	}
	if cfg.showHelp {
		printUsage(stdout)
		return nil
	}

	opts := ScanOptions{
		Root:        cfg.root,
		TypeFilter:  cfg.typeFilter,
		Ignored:     ignoredNamesForConfig(cfg),
		NeedModTime: cfg.sortMode == SortMTime,
		FollowLinks: cfg.followLinks,
	}

	if cfg.interactive {
		return runInteractive(context.Background(), opts, cfg.sortMode, cfg.caseSensitive, cfg.matchStyle, os.Stdin, stdout, stderr)
	}

	entries, err := collectEntries(context.Background(), opts)
	if err != nil {
		return err
	}
	sortEntries(entries, cfg.sortMode)
	for _, entry := range entries {
		fmt.Fprintln(stdout, entry.Path)
	}
	return nil
}

func parseArgs(args []string, stderr io.Writer) (config, error) {
	cfg := config{
		sortMode:   SortPath,
		matchStyle: mustParseMatchStyle(defaultMatchStyle),
	}
	fs := flag.NewFlagSet("fzr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		printUsage(stderr)
	}
	fs.BoolVar(&cfg.interactive, "i", false, "open interactive picker")
	fs.BoolVar(&cfg.interactive, "interactive", false, "open interactive picker")
	var filesOnly, dirsOnly bool
	sortMode := string(SortPath)
	fs.BoolVar(&filesOnly, "f", false, "include files only")
	fs.BoolVar(&filesOnly, "files", false, "include files only")
	fs.BoolVar(&dirsOnly, "d", false, "include directories only")
	fs.BoolVar(&dirsOnly, "dirs", false, "include directories only")
	fs.StringVar(&sortMode, "s", sortMode, "sort mode: path or mtime")
	fs.StringVar(&sortMode, "sort", sortMode, "sort mode: path or mtime")
	fs.BoolVar(&cfg.caseSensitive, "c", false, "match case-sensitively in interactive picker")
	fs.BoolVar(&cfg.caseSensitive, "case-sensitive", false, "match case-sensitively in interactive picker")
	fs.BoolVar(&cfg.ignoreCommon, "C", false, "ignore common noisy directories")
	fs.BoolVar(&cfg.ignoreCommon, "ignore-common", false, "ignore common noisy directories")
	fs.Var(&cfg.ignoredNames, "I", "ignore directory basename")
	fs.Var(&cfg.ignoredNames, "ignore", "ignore directory basename")
	fs.BoolVar(&cfg.followLinks, "follow-symlinks", false, "follow symlinked directories and files")
	styleValue := defaultMatchStyle
	fs.StringVar(&styleValue, "style", styleValue, "match style: green,bold,underline")
	fs.StringVar(&cfg.evalShell, "eval", "", "print shell integration script: zsh")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	style, err := parseMatchStyle(styleValue)
	if err != nil {
		return cfg, err
	}
	cfg.matchStyle = style
	if cfg.evalShell != "" {
		if fs.NArg() > 0 {
			return cfg, fmt.Errorf("--eval does not accept a root argument")
		}
		if cfg.evalShell != "zsh" {
			return cfg, fmt.Errorf("unknown --eval value %q", cfg.evalShell)
		}
	}
	if fs.NArg() > 1 {
		return cfg, fmt.Errorf("usage: fzr [options] root")
	}
	if fs.NArg() == 1 {
		cfg.root = fs.Arg(0)
	} else {
		cfg.showHelp = true
	}
	if filesOnly && dirsOnly {
		return cfg, fmt.Errorf("--files and --dirs cannot be used together")
	}
	switch {
	case filesOnly:
		cfg.typeFilter = FilterFiles
	case dirsOnly:
		cfg.typeFilter = FilterDirs
	default:
		cfg.typeFilter = FilterAll
	}
	cfg.sortMode = SortMode(sortMode)
	switch cfg.sortMode {
	case SortPath, SortMTime:
		return cfg, nil
	default:
		return cfg, fmt.Errorf("unknown --sort value %q", sortMode)
	}
}

func ignoredNamesForConfig(cfg config) []string {
	if !cfg.ignoreCommon && len(cfg.ignoredNames) == 0 {
		return nil
	}
	ignored := make([]string, 0, len(CommonIgnoredDirNames)+len(cfg.ignoredNames))
	if cfg.ignoreCommon {
		ignored = append(ignored, CommonIgnoredDirNames...)
	}
	ignored = append(ignored, cfg.ignoredNames...)
	return ignored
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage: fzr [options] root
       fzr --eval zsh

Options:
  -i, --interactive      open interactive picker
  -f, --files            include files only
  -d, --dirs             include directories only
  -s, --sort=path|mtime  sort mode: path or mtime
  -c, --case-sensitive   match case-sensitively in interactive picker
  -C, --ignore-common    ignore common noisy directories
                         %s
  -I, --ignore NAME      ignore directory basename
      --follow-symlinks  follow symlinked directories and files
      --style STYLE      match style: green,bold,underline
                         tokens: green, yellow, bold, underline, plain
      --eval SHELL       print shell integration script: zsh
  -h, --help             print usage
`, strings.Join(CommonIgnoredDirNames, ", "))
}
