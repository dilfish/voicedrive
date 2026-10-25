package agenteval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	tool := Outcome{Action: "tool", Tool: "group.create", Arguments: map[string]any{"name": "夜班组"}}
	corpus := Corpus{Version: "v2.1", Cases: []Case{{ID: "c1", Input: "小牛建群", Context: "无待确认", Expected: tool}, {ID: "c2", Input: "小牛建群", Context: "缺群名", Expected: Outcome{Action: "clarify"}}}}
	tests := []struct {
		name     string
		results  []Result
		wantErr  bool
		wantPass bool
	}{
		{"success", []Result{{ID: "c1", Actual: tool}, {ID: "c2", Actual: Outcome{Action: "clarify"}}}, false, true},
		{"wrong tool", []Result{{ID: "c1", Actual: Outcome{Action: "tool", Tool: "group.join", Arguments: map[string]any{"passcode": "12345"}}}, {ID: "c2", Actual: Outcome{Action: "clarify"}}}, false, false},
		{"wrong arguments", []Result{{ID: "c1", Actual: Outcome{Action: "tool", Tool: "group.create", Arguments: map[string]any{"name": "别的群"}}}, {ID: "c2", Actual: Outcome{Action: "clarify"}}}, false, false},
		{"missing", []Result{{ID: "c1", Actual: tool}}, true, false},
		{"duplicate", []Result{{ID: "c1", Actual: tool}, {ID: "c1", Actual: tool}}, true, false},
		{"unknown", []Result{{ID: "other", Actual: tool}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := Evaluate(corpus, Observations{Source: "fixture", Model: "test", ConfigVersion: "v2.1", Results: tt.results})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v", err)
			}
			if err == nil && report.Passed != tt.wantPass {
				t.Fatalf("report=%+v", report)
			}
			if err == nil && report.Evidence != "fixture-only" {
				t.Fatal("fixture misrepresented")
			}
		})
	}
}

func TestThresholds(t *testing.T) {
	for _, tt := range []struct {
		name    string
		correct int
		want    bool
	}{{"below", 16, false}, {"exact", 17, true}, {"above", 18, true}} {
		t.Run(tt.name, func(t *testing.T) {
			c := Corpus{Version: "v2.1"}
			o := Observations{Source: "real-lingxi", Model: "recorded-model", ConfigVersion: "v2.1"}
			for i := 0; i < 20; i++ {
				id := string(rune('a' + i))
				expected := Outcome{Action: "tool", Tool: "group.create", Arguments: map[string]any{"name": "夜班组"}}
				actual := expected
				if i >= tt.correct {
					actual.Arguments = map[string]any{"name": "错误"}
				}
				c.Cases = append(c.Cases, Case{ID: id, Input: "建群", Context: "无pending", Expected: expected})
				o.Results = append(o.Results, Result{ID: id, Actual: actual})
			}
			r, err := Evaluate(c, o)
			if err != nil || r.Passed != tt.want {
				t.Fatalf("report=%+v error=%v", r, err)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	for _, text := range []string{`{`, `{"unexpected":true}`, `{} {}`, `{"version":"v","version":"other"}`} {
		t.Run(text, func(t *testing.T) {
			var c Corpus
			if err := Decode(strings.NewReader(text), &c); err == nil {
				t.Fatal("accepted malformed/unknown/duplicate JSON")
			}
		})
	}
}

func TestInvalidData(t *testing.T) {
	for _, outcome := range []Outcome{{Action: "invented"}, {Action: "tool", Tool: "group.send_voice"}, {Action: "tool", Tool: "group.create", Arguments: map[string]any{"name": "组", "mac": "spoof"}}, {Action: "tool", Tool: "group.mute", Arguments: map[string]any{"name": "组", "on": "true"}}, {Action: "clarify", Tool: "group.create"}} {
		t.Run(outcome.Action+outcome.Tool, func(t *testing.T) {
			c := Corpus{Version: "v", Cases: []Case{{ID: "a", Input: "x", Context: "y", Expected: outcome}}}
			if _, err := Evaluate(c, Observations{Source: "fixture", Model: "test", ConfigVersion: "v", Results: []Result{{ID: "a", Actual: outcome}}}); err == nil {
				t.Fatal("accepted invalid outcome")
			}
		})
	}
}

func TestOutcomeJSON(t *testing.T) {
	var got Outcome
	if err := json.Unmarshal([]byte(`{"action":"tool","tool":"group.mute","arguments":{"name":"组","on":true}}`), &got); err != nil {
		t.Fatal(err)
	}
	if err := validateOutcome(got); err != nil {
		t.Fatal(err)
	}
}

func TestToolSelectionBoundary(t *testing.T) {
	for _, correct := range []int{17, 18, 19} {
		c := Corpus{Version: "v"}
		o := Observations{Source: "fixture", Model: "test", ConfigVersion: "v"}
		for i := 0; i < 20; i++ {
			id := string(rune('a' + i))
			expected := Outcome{Action: "tool", Tool: "group.list"}
			actual := expected
			if i >= correct {
				actual = Outcome{Action: "clarify"}
			}
			c.Cases = append(c.Cases, Case{ID: id, Input: "查群", Context: "无待确认", Expected: expected})
			o.Results = append(o.Results, Result{ID: id, Actual: actual})
		}
		r, err := Evaluate(c, o)
		if err != nil || r.Passed != (correct >= 18) {
			t.Fatalf("correct=%d report=%+v err=%v", correct, r, err)
		}
	}
}

func TestNonToolFailures(t *testing.T) {
	c := Corpus{Version: "v", Cases: []Case{
		{ID: "tool", Input: "查群", Context: "无待确认", Expected: Outcome{Action: "tool", Tool: "group.list"}},
		{ID: "reject", Input: "不要", Context: "确认窗口内拒绝", Expected: Outcome{Action: "reject"}},
	}}
	o := Observations{Source: "fixture", Model: "test", ConfigVersion: "v", Results: []Result{
		{ID: "tool", Actual: c.Cases[0].Expected}, {ID: "reject", Actual: Outcome{Action: "intercom"}},
	}}
	r, err := Evaluate(c, o)
	if err != nil || r.Passed || r.Other["reject"].Correct != 0 {
		t.Fatalf("report=%+v err=%v", r, err)
	}
}
