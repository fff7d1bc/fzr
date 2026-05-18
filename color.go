package main

import (
	"os"
	"strconv"
	"strings"
)

type pickerTheme struct {
	matchStart  string
	matchReset  string
	dimStart    string
	dimReset    string
	selectStart string
	selectReset string
	reset       string
}

func pickerThemeForStderr(stderr *os.File) pickerTheme {
	return pickerThemeForColor(shouldUsePickerColor(stderr, os.Getenv))
}

func pickerThemeForColor(color bool) pickerTheme {
	theme := pickerTheme{
		selectStart: "\x1b[7m",
		selectReset: "\x1b[0m",
		reset:       "\x1b[0m",
	}
	if !color {
		return theme
	}
	return pickerTheme{
		matchStart:  "\x1b[33m\x1b[1m\x1b[4m",
		matchReset:  "\x1b[24m\x1b[22m\x1b[39m",
		dimStart:    "\x1b[2m",
		dimReset:    "\x1b[22m",
		selectStart: "\x1b[7m",
		selectReset: "\x1b[0m",
		reset:       "\x1b[0m",
	}
}

func shouldUsePickerColor(stderr *os.File, getenv func(string) string) bool {
	info, err := stderr.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return shouldUsePickerColorEnv(true, getenv)
}

func shouldUsePickerColorEnv(isTTY bool, getenv func(string) string) bool {
	if getenv("NO_COLOR") != "" {
		return false
	}
	if getenv("TERM") == "dumb" {
		return false
	}
	if !isTTY {
		return false
	}
	return supports256ColorEnv(getenv)
}

func supports256ColorEnv(getenv func(string) string) bool {
	if colorterm := strings.ToLower(getenv("COLORTERM")); strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit") {
		return true
	}
	if term := strings.ToLower(getenv("TERM")); strings.Contains(term, "256color") {
		return true
	}
	if termProgram := strings.ToLower(getenv("TERM_PROGRAM")); termProgram == "wezterm" {
		return true
	}
	if v := getenv("TERM_PROGRAM_VERSION"); v != "" && strings.ToLower(getenv("TERM_PROGRAM")) == "apple_terminal" {
		return true
	}
	if colors := getenv("COLORS"); colors != "" {
		n, err := strconv.Atoi(colors)
		return err == nil && n >= 256
	}
	return false
}
