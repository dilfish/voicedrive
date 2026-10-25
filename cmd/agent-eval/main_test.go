package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dilfish/voicedrive/internal/agenteval"
)

func TestRun(t *testing.T) {
	for _, tt := range []struct {
		name, actual string
		want         int
	}{{"pass", "group.list", 0}, {"below threshold", "group.leave", 2}, {"wrong selection", "group.create", 1}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			corpus := filepath.Join(dir, "corpus.json")
			results := filepath.Join(dir, "actual.json")
			report := filepath.Join(dir, "report.json")
			if err := os.WriteFile(corpus, []byte(`{"version":"v","cases":[{"id":"a","input":"小牛查群","context":"无待确认","expected":{"action":"tool","tool":"group.list"}}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			args := ""
			if tt.actual == "group.create" {
				args = `,"arguments":{"name":"夜班组"}`
			}
			data := `{"source":"fixture","model":"test","config_version":"v","results":[{"id":"a","actual":{"action":"tool","tool":"` + tt.actual + `"` + args + `}}]}`
			if err := os.WriteFile(results, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if got := run([]string{"-corpus", corpus, "-results", results, "-report", report}, &out, &stderr); got != tt.want {
				t.Fatalf("exit=%d stderr=%s", got, stderr.String())
			}
			if tt.want != 2 {
				contents, err := os.ReadFile(report)
				if err != nil {
					t.Fatal(err)
				}
				var r agenteval.Report
				if err := json.Unmarshal(contents, &r); err != nil {
					t.Fatal(err)
				}
				if len(r.Details) != 1 || !strings.Contains(r.Evidence, "fixture") {
					t.Fatalf("invalid report: %+v", r)
				}
				if code := run([]string{"-corpus", corpus, "-results", results, "-report", report}, &out, &stderr); code != 2 {
					t.Fatal("overwrote existing report")
				}
			}
		})
	}
}

func TestRunInvalidFlags(t *testing.T) {
	for _, args := range [][]string{nil, {"-unknown"}, {"-results", "missing-file"}, {"-results", "x", "unexpected"}} {
		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) != 2 {
			t.Fatalf("accepted %v", args)
		}
	}
}
