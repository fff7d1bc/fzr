package main

import (
	"os"
	"testing"
)

func TestSupports256ColorEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "term 256color", env: map[string]string{"TERM": "xterm-256color"}, want: true},
		{name: "colorterm truecolor", env: map[string]string{"TERM": "xterm", "COLORTERM": "truecolor"}, want: true},
		{name: "colors fallback", env: map[string]string{"TERM": "xterm", "COLORS": "256"}, want: true},
		{name: "wezterm", env: map[string]string{"TERM_PROGRAM": "WezTerm"}, want: true},
		{name: "plain term", env: map[string]string{"TERM": "xterm"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supports256ColorEnv(testEnv(tt.env)); got != tt.want {
				t.Fatalf("supports256ColorEnv = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldUsePickerColorEnv(t *testing.T) {
	if shouldUsePickerColorEnv(true, testEnv(map[string]string{"TERM": "dumb", "COLORS": "256"})) {
		t.Fatal("expected TERM=dumb to disable color")
	}
	if shouldUsePickerColorEnv(true, testEnv(map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"})) {
		t.Fatal("expected NO_COLOR to disable color")
	}
	if shouldUsePickerColorEnv(false, testEnv(map[string]string{"TERM": "xterm-256color"})) {
		t.Fatal("expected non-tty to disable auto color")
	}
	if !shouldUsePickerColorEnv(true, testEnv(map[string]string{"TERM": "xterm-256color"})) {
		t.Fatal("expected tty with 256-color TERM to enable color")
	}
}

func TestShouldUsePickerColorUsesStderrTTY(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if shouldUsePickerColor(file, testEnv(map[string]string{"TERM": "xterm-256color"})) {
		t.Fatal("expected regular file stderr to disable auto color")
	}
}

func testEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
