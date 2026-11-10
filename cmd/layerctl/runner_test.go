package main

import (
	"bytes"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const standbyNoise = "not the primary signer"

func TestCopyFilteredDropsMatchingLines(t *testing.T) {
	in := "keep 1\nERR failed signing vote err=\"signerEndpoint returned error #0: not the primary signer\"\nkeep 2\nno newline"
	var out bytes.Buffer
	var n atomic.Int64
	copyFiltered(&out, strings.NewReader(in), [][]byte{[]byte(standbyNoise)}, &n)
	if got, want := out.String(), "keep 1\nkeep 2\nno newline"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if n.Load() != 1 {
		t.Fatalf("dropped %d, want 1", n.Load())
	}
}

func TestCopyFilteredKeepsLongLines(t *testing.T) {
	long := strings.Repeat("x", 200000) + "\n"
	var out bytes.Buffer
	var n atomic.Int64
	copyFiltered(&out, strings.NewReader(long), [][]byte{[]byte(standbyNoise)}, &n)
	if out.String() != long {
		t.Fatalf("long line changed: got %d bytes", out.Len())
	}
}

func TestLogDropPatterns(t *testing.T) {
	t.Setenv("LAYER_LOG_DROP", "")
	os.Unsetenv("LAYER_LOG_DROP")
	if p := logDropPatterns(); len(p) != 0 {
		t.Fatalf("unset should mean off, got %q", p)
	}
	t.Setenv("LAYER_LOG_DROP", "")
	if p := logDropPatterns(); len(p) != 0 {
		t.Fatalf("empty should mean off, got %q", p)
	}
	t.Setenv("LAYER_LOG_DROP", " a , b ,")
	if p := logDropPatterns(); len(p) != 2 || string(p[0]) != "a" || string(p[1]) != "b" {
		t.Fatalf("got %q", p)
	}
}
