package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestFlags(t *testing.T) {
	for _, args := range [][]string{nil, {"-mac", "TEST", "-wake-word", ""}, {"-unknown"}, {"-mac", "TEST", "-lingx-url", "invalid"}} {
		var output bytes.Buffer
		if got := run(context.Background(), args, io.NopCloser(strings.NewReader("")), &output); got != 2 {
			t.Fatalf("args=%v exit=%d", args, got)
		}
	}
}
