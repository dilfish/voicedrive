// Package agenteval scores recorded decisions without pretending to run an LLM.
package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// Outcome records a routing decision or a tool invocation, not its TTS output.
type Outcome struct {
	Action    string         `json:"action"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Case supplies the context required to interpret a single utterance.
type Case struct {
	ID       string  `json:"id"`
	Input    string  `json:"input"`
	Context  string  `json:"context"`
	Expected Outcome `json:"expected"`
}

// Corpus is a versioned command benchmark.
type Corpus struct {
	Version string `json:"version"`
	Cases   []Case `json:"cases"`
}

// Result is one externally recorded decision.
type Result struct {
	ID     string  `json:"id"`
	Actual Outcome `json:"actual"`
}

// Observations identifies the provenance of a complete run.
type Observations struct {
	Source        string   `json:"source"`
	Model         string   `json:"model"`
	ConfigVersion string   `json:"config_version"`
	Results       []Result `json:"results"`
}

// Metric counts complete successes, rather than partially matching arguments.
type Metric struct {
	Correct  int     `json:"correct"`
	Total    int     `json:"total"`
	Accuracy float64 `json:"accuracy"`
}

// Detail preserves both sides of every comparison for review.
type Detail struct {
	ID       string  `json:"id"`
	Expected Outcome `json:"expected"`
	Actual   Outcome `json:"actual"`
	Passed   bool    `json:"passed"`
}

// Report is an offline score; even real-lingxi input remains user-supplied evidence.
type Report struct {
	Source        string            `json:"source"`
	Model         string            `json:"model"`
	ConfigVersion string            `json:"config_version"`
	Evidence      string            `json:"evidence"`
	Passed        bool              `json:"passed"`
	Tools         Metric            `json:"tool_selection"`
	Arguments     Metric            `json:"parameter_extraction"`
	Other         map[string]Metric `json:"non_tool_actions"`
	Details       []Detail          `json:"details"`
}

// Decode rejects unknown fields, duplicate JSON keys and trailing documents.
func Decode(r io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, 8*1024*1024+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if len(data) > 8*1024*1024 {
		return fmt.Errorf("JSON exceeds 8 MiB")
	}
	keys := json.NewDecoder(bytes.NewReader(data))
	if err := scanValue(keys); err != nil {
		return fmt.Errorf("validate JSON: %w", err)
	}
	if _, err := keys.Token(); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

func scanValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return fmt.Errorf("read key: %w", err)
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("invalid or duplicate key %v", key)
			}
			seen[name] = true
			if err := scanValue(d); err != nil {
				return fmt.Errorf("field %s: %w", name, err)
			}
		}
	case '[':
		for d.More() {
			if err := scanValue(d); err != nil {
				return fmt.Errorf("array element: %w", err)
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter %v", delim)
	}
	if _, err := d.Token(); err != nil {
		return fmt.Errorf("close container: %w", err)
	}
	return nil
}

func validateOutcome(o Outcome) error {
	if o.Action != "tool" {
		switch o.Action {
		case "clarify", "reject", "intercom", "cancel":
		default:
			return fmt.Errorf("unknown action %q", o.Action)
		}
		if o.Tool != "" || len(o.Arguments) != 0 {
			return fmt.Errorf("non-tool action contains tool arguments")
		}
		return nil
	}
	// Keep the evaluator tied to the published schema, not arbitrary model output.
	schemas := map[string]map[string]string{
		"group.create": {"name": "string", "passcode?": "string"}, "group.join": {"passcode": "string"},
		"group.switch": {"name": "string"}, "group.mute": {"name": "string", "on": "bool"}, "group.list": {},
		"group.leave": {"name": "string"}, "group.rename": {"name": "string", "new_name": "string"},
		"group.pat": {"alias": "string"}, "group.invite": {"alias": "string", "group_name": "string"},
		"group.query_member": {"group_name": "string", "alias": "string"},
	}
	schema, ok := schemas[o.Tool]
	if !ok {
		return fmt.Errorf("unknown tool %q", o.Tool)
	}
	allowed := map[string]string{}
	for key, typ := range schema {
		name := strings.TrimSuffix(key, "?")
		allowed[name] = typ
		if _, ok := o.Arguments[name]; !ok && name == key {
			return fmt.Errorf("missing argument %s", name)
		}
	}
	for key, value := range o.Arguments {
		switch allowed[key] {
		case "string":
			v, ok := value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return fmt.Errorf("invalid string argument %s", key)
			}
		case "bool":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("invalid boolean argument %s", key)
			}
		default:
			return fmt.Errorf("unknown argument %s", key)
		}
	}
	return nil
}

func sameArguments(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func metric(correct, total int) Metric {
	m := Metric{Correct: correct, Total: total}
	if total > 0 {
		m.Accuracy = float64(correct) / float64(total)
	}
	return m
}

// Evaluate validates a complete run and scores tool decisions separately from routing.
func Evaluate(c Corpus, o Observations) (Report, error) {
	r := Report{Source: o.Source, Model: o.Model, ConfigVersion: o.ConfigVersion, Other: map[string]Metric{}, Evidence: "fixture-only"}
	if strings.TrimSpace(c.Version) == "" || o.ConfigVersion != c.Version || strings.TrimSpace(o.Model) == "" {
		return r, fmt.Errorf("version/model missing or configuration version mismatch")
	}
	if o.Source != "fixture" && o.Source != "real-lingxi" {
		return r, fmt.Errorf("source must be fixture or real-lingxi")
	}
	if o.Source == "real-lingxi" {
		r.Evidence = "user-supplied-real-lingxi-recording; not independently verified"
	}
	if len(c.Cases) == 0 {
		return r, fmt.Errorf("empty corpus")
	}
	ids := map[string]bool{}
	for _, tc := range c.Cases {
		if strings.TrimSpace(tc.ID) == "" || ids[tc.ID] || strings.TrimSpace(tc.Input) == "" || strings.TrimSpace(tc.Context) == "" {
			return r, fmt.Errorf("invalid or duplicate case %q", tc.ID)
		}
		ids[tc.ID] = true
		if err := validateOutcome(tc.Expected); err != nil {
			return r, fmt.Errorf("case %s: %w", tc.ID, err)
		}
	}
	actual := map[string]Outcome{}
	for _, result := range o.Results {
		if !ids[result.ID] {
			return r, fmt.Errorf("unknown result ID %q", result.ID)
		}
		if _, ok := actual[result.ID]; ok {
			return r, fmt.Errorf("duplicate result ID %q", result.ID)
		}
		if err := validateOutcome(result.Actual); err != nil {
			return r, fmt.Errorf("result %s: %w", result.ID, err)
		}
		actual[result.ID] = result.Actual
	}
	otherPassed := true
	for _, tc := range c.Cases {
		a, ok := actual[tc.ID]
		if !ok {
			return r, fmt.Errorf("missing result %s", tc.ID)
		}
		selection := a.Action == tc.Expected.Action && a.Tool == tc.Expected.Tool
		passed := selection && sameArguments(a.Arguments, tc.Expected.Arguments)
		r.Details = append(r.Details, Detail{ID: tc.ID, Expected: tc.Expected, Actual: a, Passed: passed})
		if tc.Expected.Action == "tool" {
			r.Tools.Total++
			r.Arguments.Total++
			if selection {
				r.Tools.Correct++
			}
			if passed {
				r.Arguments.Correct++
			}
		} else {
			m := r.Other[tc.Expected.Action]
			m.Total++
			if passed {
				m.Correct++
			} else {
				otherPassed = false
			}
			r.Other[tc.Expected.Action] = m
		}
	}
	r.Tools = metric(r.Tools.Correct, r.Tools.Total)
	r.Arguments = metric(r.Arguments.Correct, r.Arguments.Total)
	for key, m := range r.Other {
		r.Other[key] = metric(m.Correct, m.Total)
	}
	r.Passed = r.Tools.Total > 0 && r.Tools.Correct*100 >= r.Tools.Total*90 && r.Arguments.Correct*100 >= r.Arguments.Total*85 && otherPassed
	return r, nil
}
