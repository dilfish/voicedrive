package mock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunScriptAndAssertions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"group":{"id":"g1"}}`))
	}))
	defer server.Close()
	report := Run(context.Background(), server.URL, Script{Name: "demo", Steps: []Step{{Name: "create", Path: "/v1/groups", ExpectCode: http.StatusCreated, Expect: map[string]any{"group.id": "g1"}}}})
	if !report.Passed || len(report.Results) != 1 {
		t.Fatalf("report = %#v", report)
	}
}
