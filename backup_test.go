package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHumanSize(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1048576, "1.0M"},
		{1073741824, "1.0G"},
		{1099511627776, "1.0T"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := humanSize(tt.input)
			if got != tt.expected {
				t.Errorf("humanSize(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestWrapTargetError(t *testing.T) {
	inner := errors.New("something went wrong")
	err := wrapTargetError("/some/path/with spaces", inner)

	if err == nil {
		t.Fatal("expected wrapped error, got nil")
	}

	msg := err.Error()
	if msg != `backup "/some/path/with spaces": something went wrong` {
		t.Errorf("unexpected error message: %s", msg)
	}

	// Ensure it supports errors.Is / errors.As
	if !errors.Is(err, inner) {
		t.Error("wrapped error should support errors.Is")
	}
}

func TestWrapTargetError_InBackupErrorPath(t *testing.T) {
	// Simulate what happens in main() when backup fails
	innerErr := errors.New("permission denied")
	err := wrapTargetError("/secret/sensitive", innerErr)

	if !errors.Is(err, innerErr) {
		t.Error("expected errors.Is to work through wrapping")
	}

	if !strings.Contains(err.Error(), "/secret/sensitive") {
		t.Errorf("error message should contain original target name, got: %s", err.Error())
	}
}

func TestCalculateSize(t *testing.T) {
	tmp := t.TempDir()

	// Create some files
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "b.bin"), make([]byte, 2048), 0644); err != nil {
		t.Fatal(err)
	}

	// Nested dir
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "c.log"), []byte("logdata"), 0644); err != nil {
		t.Fatal(err)
	}

	size := calculateSize(tmp)
	expected := int64(5 + 2048 + 7) // hello + 2048 bytes + logdata

	if size != expected {
		t.Errorf("calculateSize() = %d, want %d", size, expected)
	}
}

func TestCalculateSize_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	size := calculateSize(tmp)
	if size != 0 {
		t.Errorf("expected 0 for empty dir, got %d", size)
	}
}
