package agenteval

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	f, err := os.Open("../../docs/agent-v2/" + name)
	if err != nil {
		t.Fatal(err)
	}
	err = Decode(f, target)
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestPublishedCommands(t *testing.T) {
	var c Corpus
	readFixture(t, "commands.json", &c)
	o := Observations{Source: "fixture", Model: "corpus-self-check-not-an-LLM", ConfigVersion: c.Version}
	toolCount := 0
	for _, tc := range c.Cases {
		if tc.Expected.Action == "tool" {
			toolCount++
		}
		o.Results = append(o.Results, Result{ID: tc.ID, Actual: tc.Expected})
	}
	if toolCount < 30 {
		t.Fatalf("only %d tool cases", toolCount)
	}
	r, err := Evaluate(c, o)
	if err != nil || !r.Passed {
		t.Fatalf("report=%+v err=%v", r, err)
	}
}

func TestPublishedResolution(t *testing.T) {
	var c Corpus
	readFixture(t, "resolution.json", &c)
	if c.Version == "" || len(c.Cases) < 30 {
		t.Fatal("missing version or fewer than 30 cases")
	}
	seen := map[string]bool{}
	for _, tc := range c.Cases {
		if tc.ID == "" || seen[tc.ID] || tc.Input == "" || tc.Context == "" {
			t.Fatalf("invalid case %+v", tc)
		}
		seen[tc.ID] = true
		switch tc.Expected.Action {
		case "matched":
			if id, ok := tc.Expected.Arguments["group_id"].(string); !ok || id == "" {
				t.Fatalf("missing group ID: %s", tc.ID)
			}
		case "ambiguous":
			if opts, ok := tc.Expected.Arguments["options"].([]any); !ok || len(opts) < 2 || len(opts) > 3 {
				t.Fatalf("invalid options: %s", tc.ID)
			}
		case "not_found":
			if len(tc.Expected.Arguments) != 0 {
				t.Fatalf("not_found has arguments: %s", tc.ID)
			}
		default:
			t.Fatalf("invalid resolution: %s", tc.ID)
		}
	}
}

func TestPublishedTools(t *testing.T) {
	var tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Phase       string `json:"phase"`
	}
	readFixture(t, "tools.json", &tools)
	if len(tools) != 10 {
		t.Fatalf("got %d tools", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool.Name] || tool.Description == "" || (tool.Phase != "V0" && tool.Phase != "V0.5") {
			t.Fatalf("invalid tool %+v", tool)
		}
		seen[tool.Name] = true
		args := map[string]any{}
		switch tool.Name {
		case "group.create", "group.switch", "group.leave":
			args["name"] = "组"
		case "group.join":
			args["passcode"] = "12345"
		case "group.mute":
			args["name"] = "组"
			args["on"] = true
		case "group.rename":
			args["name"] = "组"
			args["new_name"] = "新组"
		case "group.pat":
			args["alias"] = "老王"
		case "group.invite", "group.query_member":
			args["alias"] = "老王"
			args["group_name"] = "组"
		}
		if err := validateOutcome(Outcome{Action: "tool", Tool: tool.Name, Arguments: args}); err != nil {
			t.Fatal(err)
		}
	}
}
