package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dilfish/voicedrive/internal/demo"
)

func TestReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	var out bytes.Buffer
	if code := run([]string{"-report", path}, &out); code != 1 {
		t.Fatalf("exit=%d output=%s", code, &out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report demo.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Overall == demo.StatusPassed || report.Started {
		t.Fatal("false product success")
	}
	if run([]string{"-report", path}, &out) != 2 {
		t.Fatal("overwrote evidence")
	}
}
