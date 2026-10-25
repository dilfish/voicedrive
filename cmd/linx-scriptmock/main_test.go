package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Study-of-slag-weng/voicedrive1/internal/scriptmock"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.json")
	report := filepath.Join(dir, "report.json")
	if err := os.WriteFile(script, []byte(`{"version":1,"timeout_ms":10,"steps":[{"op":"quiet"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run(context.Background(), []string{"-script", script, "-listen", "127.0.0.1:0", "-report", report}, &output); code != 1 {
		t.Fatalf("code=%d output=%s", code, &output)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var r scriptmock.Report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Passed || r.Error == "" {
		t.Fatal("missing failure report")
	}
	if code := run(context.Background(), []string{"-script", script, "-report", report}, &output); code != 2 {
		t.Fatal("overwrote report")
	}
}

func TestFlags(t *testing.T) {
	for _, args := range [][]string{nil, {"-script", "x", "-listen", "0.0.0.0:8080"}, {"-script", "x", "-timeout", "5m"}, {"-unknown"}} {
		var out bytes.Buffer
		if run(context.Background(), args, &out) != 2 {
			t.Fatalf("accepted %v", args)
		}
	}
}
