package main

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestParseArgsCaseSensitive(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseArgs([]string{"--case-sensitive", "-i", "/tmp"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.caseSensitive {
		t.Fatal("caseSensitive = false, want true")
	}
	if !cfg.interactive {
		t.Fatal("interactive = false, want true")
	}
	if cfg.root != "/tmp" {
		t.Fatalf("root = %q, want /tmp", cfg.root)
	}
	if cfg.matchStyle.color != "green" || !cfg.matchStyle.bold || !cfg.matchStyle.underline {
		t.Fatalf("matchStyle = %#v, want default green bold underline", cfg.matchStyle)
	}
}

func TestParseArgsShortFlags(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseArgs([]string{"-f", "-s", "mtime", "-c", "-C", "-I", "target", "--follow-symlinks", "/tmp"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.typeFilter != FilterFiles {
		t.Fatalf("typeFilter = %v, want files", cfg.typeFilter)
	}
	if cfg.sortMode != SortMTime {
		t.Fatalf("sortMode = %q, want mtime", cfg.sortMode)
	}
	if !cfg.caseSensitive {
		t.Fatal("caseSensitive = false, want true")
	}
	if !cfg.ignoreCommon {
		t.Fatal("ignoreCommon = false, want true")
	}
	if !cfg.followLinks {
		t.Fatal("followLinks = false, want true")
	}
	if !reflect.DeepEqual([]string(cfg.ignoredNames), []string{"target"}) {
		t.Fatalf("ignoredNames = %#v, want target", []string(cfg.ignoredNames))
	}
	if cfg.root != "/tmp" {
		t.Fatalf("root = %q, want /tmp", cfg.root)
	}
}

func TestParseArgsLongIgnoreFlags(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseArgs([]string{"--ignore-common", "--ignore", "target", "--ignore", "dist"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ignoreCommon {
		t.Fatal("ignoreCommon = false, want true")
	}
	if !reflect.DeepEqual([]string(cfg.ignoredNames), []string{"target", "dist"}) {
		t.Fatalf("ignoredNames = %#v, want target and dist", []string(cfg.ignoredNames))
	}
}

func TestParseArgsStyle(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseArgs([]string{"--style", "yellow,bold,underline", "-i", "/tmp"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.matchStyle.color != "yellow" || !cfg.matchStyle.bold || !cfg.matchStyle.underline {
		t.Fatalf("matchStyle = %#v, want yellow bold underline", cfg.matchStyle)
	}
}

func TestParseArgsPlainStyle(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseArgs([]string{"--style", "plain", "-i", "/tmp"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.matchStyle.plain {
		t.Fatalf("matchStyle = %#v, want plain", cfg.matchStyle)
	}
}

func TestParseArgsRejectsInvalidStyle(t *testing.T) {
	for _, value := range []string{"green,,bold", "green,yellow", "plain,bold", "blue"} {
		t.Run(value, func(t *testing.T) {
			var stderr bytes.Buffer
			if _, err := parseArgs([]string{"--style", value, "-i", "/tmp"}, &stderr); err == nil {
				t.Fatal("expected invalid style to fail")
			}
		})
	}
}

func TestIgnoredNamesForConfig(t *testing.T) {
	got := ignoredNamesForConfig(config{})
	if got != nil {
		t.Fatalf("ignored names = %#v, want nil", got)
	}

	got = ignoredNamesForConfig(config{
		ignoreCommon: true,
		ignoredNames: stringListFlag{"target"},
	})
	want := append([]string{}, CommonIgnoredDirNames...)
	want = append(want, "target")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ignored names = %#v, want %#v", got, want)
	}
}

func TestParseArgsRejectsLatest(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseArgs([]string{"--latest"}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("err = %v, want unknown flag", err)
	}
}

func TestParseArgsRejectsFilesAndDirsShortFlags(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseArgs([]string{"-f", "-d"}, &stderr); err == nil {
		t.Fatal("expected -f and -d together to fail")
	}
}

func TestRunNoArgsPrintsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage: fzr [options] root") {
		t.Fatalf("stdout = %q, want usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunOptionWithoutRootPrintsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--files"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage: fzr [options] root") {
		t.Fatalf("stdout = %q, want usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHelpPrintsHelpToStdout(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run([]string{arg}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), "Usage: fzr [options] root") {
				t.Fatalf("stdout = %q, want usage", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestHelpPrintsCommonIgnoreNames(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, name := range CommonIgnoredDirNames {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("help output missing common ignore name %q", name)
		}
	}
}

func TestRunEvalZshPrintsIntegration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--eval", "zsh"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "zle -N fzr-append-path-to-buffer") {
		t.Fatalf("eval output missing widget registration: %q", got)
	}
	if !strings.Contains(got, "bindkey \"^F\" fzr-append-path-to-buffer") {
		t.Fatalf("eval output missing Ctrl-F binding: %q", got)
	}
	if !strings.Contains(got, "emulate -L zsh") {
		t.Fatalf("eval output missing local zsh emulation: %q", got)
	}
	if !strings.Contains(got, "POSTDISPLAY=") {
		t.Fatalf("eval output missing zle postdisplay cleanup: %q", got)
	}
	if !strings.Contains(got, "zle reset-prompt") {
		t.Fatalf("eval output missing zle prompt reset: %q", got)
	}
	if strings.Contains(got, "autosuggest") {
		t.Fatalf("eval output references autosuggestions directly: %q", got)
	}
	if strings.Contains(got, "zle_bracketed_paste") {
		t.Fatalf("eval output controls bracketed paste directly: %q", got)
	}
	if strings.Contains(got, "command -v fzr") {
		t.Fatalf("eval output contains command guard: %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestZshIntegrationSyntax(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	cmd := exec.Command(zsh, "-n")
	cmd.Stdin = strings.NewReader(zshIntegrationScript)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("zsh -n failed: %v\n%s", err, output)
	}
}

func TestParseArgsRejectsEvalRoot(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseArgs([]string{"--eval", "zsh", "."}, &stderr); err == nil {
		t.Fatal("expected --eval with root argument to fail")
	}
}

func TestParseArgsRejectsUnknownEvalShell(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseArgs([]string{"--eval", "fish"}, &stderr); err == nil {
		t.Fatal("expected unknown --eval shell to fail")
	}
}
