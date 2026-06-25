package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const defaultMatchStyle = "green,bold,underline"

type matchStyle struct {
	color     string
	bold      bool
	underline bool
	plain     bool
}

type pickerTheme struct {
	matchStart  string
	matchReset  string
	dimStart    string
	dimReset    string
	statusStart string
	statusReset string
	selectStart string
	selectReset string
	reset       string
}

func pickerThemeForStderr(stderr *os.File, style matchStyle) pickerTheme {
	return pickerThemeForColorAndStyle(shouldUsePickerColor(stderr, os.Getenv), style)
}

func pickerThemeForWriter(stderr io.Writer, style matchStyle) pickerTheme {
	file, ok := stderr.(*os.File)
	if !ok {
		// Non-file writers cannot be queried for terminal capability, so keep
		// auto color off for tests and embedded callers that supply buffers.
		return pickerThemeForColorAndStyle(false, style)
	}
	return pickerThemeForStderr(file, style)
}

func pickerThemeForColor(color bool) pickerTheme {
	return pickerThemeForColorAndStyle(color, mustParseMatchStyle(defaultMatchStyle))
}

func pickerThemeForColorAndStyle(color bool, style matchStyle) pickerTheme {
	theme := pickerTheme{
		selectStart: "\x1b[7m",
		selectReset: "\x1b[0m",
		reset:       "\x1b[0m",
	}
	if !color {
		return theme
	}
	matchStart, matchReset := matchStyleANSI(style)
	return pickerTheme{
		matchStart:  matchStart,
		matchReset:  matchReset,
		dimStart:    "\x1b[2m",
		dimReset:    "\x1b[22m",
		statusStart: "\x1b[38;5;242m",
		statusReset: "\x1b[0m",
		selectStart: "\x1b[7m",
		selectReset: "\x1b[0m",
		reset:       "\x1b[0m",
	}
}

func parseMatchStyle(value string) (matchStyle, error) {
	if value == "" {
		return matchStyle{}, fmt.Errorf("style cannot be empty")
	}
	var style matchStyle
	tokens := strings.Split(value, ",")
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			return matchStyle{}, fmt.Errorf("style contains an empty token")
		}
		switch token {
		case "plain":
			style.plain = true
		case "green", "yellow":
			if style.color != "" && style.color != token {
				return matchStyle{}, fmt.Errorf("style cannot use both %s and %s", style.color, token)
			}
			style.color = token
		case "bold":
			style.bold = true
		case "underline":
			style.underline = true
		default:
			return matchStyle{}, fmt.Errorf("unknown style token %q", token)
		}
	}
	if style.plain && (len(tokens) > 1 || style.color != "" || style.bold || style.underline) {
		return matchStyle{}, fmt.Errorf("style plain cannot be combined with other tokens")
	}
	return style, nil
}

func mustParseMatchStyle(value string) matchStyle {
	style, err := parseMatchStyle(value)
	if err != nil {
		panic(err)
	}
	return style
}

func matchStyleANSI(style matchStyle) (string, string) {
	if style.plain {
		// Plain disables only match styling; selection and status resets still
		// come from the surrounding picker theme.
		return "", ""
	}
	var start, reset strings.Builder
	switch style.color {
	case "green":
		start.WriteString("\x1b[32m")
		reset.WriteString("\x1b[39m")
	case "yellow":
		start.WriteString("\x1b[33m")
		reset.WriteString("\x1b[39m")
	}
	if style.bold {
		start.WriteString("\x1b[1m")
		reset.WriteString("\x1b[22m")
	}
	if style.underline {
		start.WriteString("\x1b[4m")
		reset.WriteString("\x1b[24m")
	}
	return start.String(), reset.String()
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
	// The picker uses dim and underline in addition to basic color, so require
	// a terminal signal that 256-color-style escapes are likely to render well.
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
