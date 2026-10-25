package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Script is a JSON scenario executed against a running voicedrive server.
type Script struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

// Step is one HTTP action and its optional response assertions.
type Step struct {
	Action     string            `json:"action,omitempty"`
	Send       json.RawMessage   `json:"send,omitempty"`
	TimeoutMS  int               `json:"timeout_ms,omitempty"`
	Capture    map[string]string `json:"capture,omitempty"`
	Name       string            `json:"name"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	MAC        string            `json:"mac,omitempty"`
	Body       json.RawMessage   `json:"body,omitempty"`
	AudioFile  string            `json:"audio_file,omitempty"`
	SilenceMS  int               `json:"silence_ms,omitempty"`
	ExpectCode int               `json:"expect_code"`
	Expect     map[string]any    `json:"expect,omitempty"`
}

type Result struct {
	Name     string        `json:"name"`
	Passed   bool          `json:"passed"`
	Duration time.Duration `json:"duration_ns"`
	Error    string        `json:"error,omitempty"`
}

type Report struct {
	Name    string   `json:"name"`
	Passed  bool     `json:"passed"`
	Results []Result `json:"results"`
}

// Load reads a JSON script file.
func Load(path string) (Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Script{}, fmt.Errorf("read script: %w", err)
	}
	var script Script
	if err := json.Unmarshal(data, &script); err != nil {
		return Script{}, fmt.Errorf("decode script: %w", err)
	}
	if len(script.Steps) == 0 {
		return Script{}, errors.New("script has no steps")
	}
	for index, step := range script.Steps {
		if (step.Action == "timeout" || step.Action == "disconnect") && index != len(script.Steps)-1 {
			return Script{}, fmt.Errorf("step %d: %s must be the last step", index+1, step.Action)
		}
	}
	return script, nil
}

// Run executes a script and returns a report. A failed assertion stops later steps.
func Run(ctx context.Context, baseURL string, script Script) Report {
	report := Report{Name: script.Name, Passed: true, Results: make([]Result, 0, len(script.Steps))}
	client := &http.Client{Timeout: 5 * time.Second}
	variables := map[string]any{}
	if len(script.Steps) == 0 {
		return Report{Name: script.Name, Results: []Result{{Error: "script has no steps"}}}
	}
	for _, step := range script.Steps {
		started := time.Now()
		result := Result{Name: step.Name, Passed: true}
		if err := runStep(ctx, client, baseURL, step, variables); err != nil {
			result.Passed = false
			result.Error = err.Error()
			report.Passed = false
		}
		result.Duration = time.Since(started)
		report.Results = append(report.Results, result)
		if !result.Passed {
			break
		}
	}
	return report
}

func runStep(ctx context.Context, client *http.Client, baseURL string, step Step, variables map[string]any) error {
	if step.Action != "" && step.Action != "http" {
		return fmt.Errorf("action %q requires WebSocket mode", step.Action)
	}
	encoded, err := json.Marshal(step)
	if err != nil {
		return err
	}
	for key, value := range variables {
		replacement, err := json.Marshal(fmt.Sprint(value))
		if err != nil {
			return err
		}
		encoded = bytes.ReplaceAll(encoded, []byte("${"+key+"}"), replacement[1:len(replacement)-1])
	}
	if err := json.Unmarshal(encoded, &step); err != nil {
		return err
	}
	method := step.Method
	if method == "" {
		method = http.MethodPost
	}
	path := step.Path
	var body io.Reader
	if step.SilenceMS < 0 || step.SilenceMS > 60000 {
		return errors.New("silence_ms must be between 0 and 60000")
	}
	if step.SilenceMS > 0 && step.AudioFile != "" {
		return errors.New("silence_ms and audio_file are mutually exclusive")
	}
	if step.SilenceMS > 0 {
		body = bytes.NewReader(make([]byte, step.SilenceMS*32))
	} else if step.AudioFile != "" {
		data, err := os.ReadFile(filepath.Clean(step.AudioFile))
		if err != nil {
			return fmt.Errorf("read audio file: %w", err)
		}
		body = bytes.NewReader(data)
	} else if len(step.Body) > 0 {
		body = bytes.NewReader(step.Body)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if step.MAC != "" {
		request.Header.Set("X-VD-MAC", step.MAC)
	}
	if step.AudioFile != "" || step.SilenceMS > 0 {
		request.Header.Set("Content-Type", "audio/pcm")
	} else if len(step.Body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	expected := step.ExpectCode
	if expected == 0 {
		expected = http.StatusOK
	}
	if response.StatusCode != expected {
		return fmt.Errorf("status = %d, want %d: %s", response.StatusCode, expected, strings.TrimSpace(string(data)))
	}
	if len(step.Expect) == 0 && len(step.Capture) == 0 {
		return nil
	}
	var actual any
	if err := json.Unmarshal(data, &actual); err != nil {
		return fmt.Errorf("decode response JSON: %w", err)
	}
	if err := assertFields(actual, step.Expect); err != nil {
		return err
	}
	for variable, path := range step.Capture {
		value, ok := lookup(actual, path)
		if !ok {
			return fmt.Errorf("capture %q: missing field %q", variable, path)
		}
		variables[variable] = value
	}
	return nil
}

func assertFields(actual any, expected map[string]any) error {
	for key, want := range expected {
		got, ok := lookup(actual, key)
		if !ok || !reflect.DeepEqual(got, want) {
			return fmt.Errorf("field %q = %#v, want %#v", key, got, want)
		}
	}
	return nil
}

func lookup(value any, path string) (any, bool) {
	current := value
	for _, part := range strings.Split(path, ".") {
		if array, ok := current.([]any); ok {
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(array) {
				return nil, false
			}
			current = array[index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
