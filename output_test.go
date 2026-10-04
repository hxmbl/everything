// Tests for reading file content and for output helpers.

package main

import (
	"os"
	"testing"
)

func TestReadWholeFile(t *testing.T) {
	// Test normal read - head is provided externally, file is at position after head
	f, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	content := "hello world"
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	// Read the head first (simulating the peek)
	head := make([]byte, 3)
	n, err := f.Read(head)
	if err != nil {
		t.Fatal(err)
	}
	head = head[:n]

	// Now read the rest using readWholeFile (file position is after head)
	data, err := readWholeFile(head, f)
	if err != nil {
		t.Errorf("readWholeFile failed: %v", err)
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}

	// Test error case - unreadable file
	badFile, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(badFile.Name())
	badFile.Close()

	// Try to read from closed file
	_, err = readWholeFile([]byte("x"), badFile)
	if err == nil {
		t.Error("expected error for closed file")
	}
}
