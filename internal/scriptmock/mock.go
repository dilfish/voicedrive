// Package scriptmock implements a bounded, single-device scripted platform peer.
package scriptmock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Step describes one deterministic protocol action.
type Step struct {
	Op        string          `json:"op"`
	Frame     json.RawMessage `json:"frame,omitempty"`
	TimeoutMS int             `json:"timeout_ms,omitempty"`
	Text      string          `json:"text,omitempty"`
	AdvanceMS int             `json:"advance_ms,omitempty"`
}

// Script is a complete, isolated device session.
type Script struct {
	Version   int    `json:"version"`
	MAC       string `json:"mac,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	Steps     []Step `json:"steps"`
}

// Adapter must inject input through the real device router, never directly call tools.
type Adapter interface {
	Input(context.Context, string) error
	Advance(context.Context, time.Duration) error
}

// Frame preserves every received frame, including binary audio.
type Frame struct {
	Type int    `json:"type"`
	Data []byte `json:"data_base64"`
}

// Result provides exact expected/actual evidence for a step.
type Result struct {
	Index      int             `json:"index"`
	Op         string          `json:"op"`
	Passed     bool            `json:"passed"`
	DurationMS int64           `json:"duration_ms"`
	Expected   json.RawMessage `json:"expected,omitempty"`
	Actual     *Frame          `json:"actual,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Report explicitly describes protocol tests rather than model quality.
type Report struct {
	Passed     bool     `json:"passed"`
	Error      string   `json:"error,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	Steps      []Result `json:"steps"`
	Received   []Frame  `json:"received"`
}

// Parse rejects unknown fields and extra documents before validating steps.
func Parse(r io.Reader) (Script, error) {
	var s Script
	d := json.NewDecoder(io.LimitReader(r, 1024*1024+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, fmt.Errorf("decode script: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("expected one JSON document")
	}
	if err := validate(s); err != nil {
		return s, fmt.Errorf("validate script: %w", err)
	}
	return s, nil
}

func validate(s Script) error {
	if s.Version != 1 || len(s.Steps) == 0 || len(s.Steps) > 1000 || s.TimeoutMS < 0 || s.TimeoutMS > 240000 {
		return fmt.Errorf("invalid version, step count or timeout")
	}
	for i, step := range s.Steps {
		if step.TimeoutMS < 0 || step.TimeoutMS > 60000 || step.AdvanceMS < 0 {
			return fmt.Errorf("step %d: invalid timing", i)
		}
		switch step.Op {
		case "send", "expect":
			var object map[string]any
			if err := json.Unmarshal(step.Frame, &object); err != nil || object == nil {
				return fmt.Errorf("step %d: frame must be JSON object", i)
			}
		case "quiet", "disconnect":
		case "input":
			if step.Text == "" {
				return fmt.Errorf("step %d: input text required", i)
			}
		case "advance":
			if step.AdvanceMS == 0 {
				return fmt.Errorf("step %d: advance_ms required", i)
			}
		default:
			return fmt.Errorf("step %d: unknown operation %q", i, step.Op)
		}
		if step.Op != "send" && step.Op != "expect" && len(step.Frame) != 0 {
			return fmt.Errorf("step %d: unexpected frame", i)
		}
		if step.Op != "input" && step.Text != "" {
			return fmt.Errorf("step %d: unexpected text", i)
		}
		if step.Op != "advance" && step.AdvanceMS != 0 {
			return fmt.Errorf("step %d: unexpected advance_ms", i)
		}
	}
	return nil
}

// Mock owns one connection; independent instances share no session state.
type Mock struct {
	script      Script
	adapter     Adapter
	mu          sync.Mutex
	claimed     bool
	finished    bool
	connections chan *websocket.Conn
}

// New validates a script before exposing its HTTP handler.
func New(s Script, a Adapter) (*Mock, error) {
	if err := validate(s); err != nil {
		return nil, fmt.Errorf("new mock: %w", err)
	}
	if s.TimeoutMS == 0 {
		s.TimeoutMS = 5000
	}
	return &Mock{script: s, adapter: a, connections: make(chan *websocket.Conn, 1)}, nil
}

// ServeHTTP accepts exactly one matching device and transfers connection ownership to Run.
func (m *Mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimed || m.finished {
		http.Error(w, "session unavailable", http.StatusConflict)
		return
	}
	if m.script.MAC != "" && r.URL.Query().Get("mac") != m.script.MAC {
		http.Error(w, "unexpected MAC", http.StatusForbidden)
		return
	}
	u := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024}
	c, err := u.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	m.claimed = true
	m.connections <- c
}

type event struct {
	frame Frame
	err   error
}

// Run serializes writes and steps; a dedicated reader records all incoming frames.
// The caller must close its HTTP server after Run, including on connect timeout.
func (m *Mock) Run(parent context.Context) Report {
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.finished = true
		select {
		case pending := <-m.connections:
			if err := pending.Close(); err != nil {
				return
			}
		default:
		}
	}()
	start := time.Now()
	r := Report{Steps: []Result{}, Received: []Frame{}}
	ctx, cancel := context.WithTimeout(parent, time.Duration(m.script.TimeoutMS)*time.Millisecond)
	defer cancel()
	var c *websocket.Conn
	select {
	case c = <-m.connections:
	case <-ctx.Done():
		r.Error = "waiting for device: " + ctx.Err().Error()
		r.DurationMS = time.Since(start).Milliseconds()
		return r
	}
	c.SetReadLimit(1024 * 1024)
	events := make(chan event, 64)
	done := make(chan struct{})
	var framesMu sync.Mutex
	frames := []Frame{}
	go func() {
		defer close(done)
		for {
			typ, data, err := c.ReadMessage()
			e := event{frame: Frame{Type: typ, Data: data}, err: err}
			if err == nil {
				framesMu.Lock()
				frames = append(frames, e.frame)
				framesMu.Unlock()
			}
			select {
			case events <- e:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for i, step := range m.script.Steps {
		begin := time.Now()
		result := Result{Index: i, Op: step.Op, Expected: step.Frame}
		wait := time.Duration(step.TimeoutMS) * time.Millisecond
		if wait == 0 {
			wait = time.Second
		}
		stepCtx, stop := context.WithTimeout(ctx, wait)
		var err error
		switch step.Op {
		case "send":
			deadline, _ := stepCtx.Deadline()
			if e := c.SetWriteDeadline(deadline); e != nil {
				err = fmt.Errorf("write deadline: %w", e)
			} else if e := c.WriteMessage(websocket.TextMessage, step.Frame); e != nil {
				err = fmt.Errorf("send: %w", e)
			}
		case "expect", "quiet":
			select {
			case e := <-events:
				if e.err != nil {
					err = fmt.Errorf("receive: %w", e.err)
					break
				}
				result.Actual = &e.frame
				if step.Op == "quiet" {
					err = fmt.Errorf("expected zero upstream frames; actual type=%d data=%q", e.frame.Type, e.frame.Data)
					break
				}
				var expected, actual any
				if e := json.Unmarshal(step.Frame, &expected); e != nil {
					err = fmt.Errorf("expected JSON: %w", e)
					break
				}
				if e := json.Unmarshal(e.frame.Data, &actual); e != nil {
					err = fmt.Errorf("actual JSON: %w", e)
					break
				}
				if e.frame.Type != websocket.TextMessage || !reflect.DeepEqual(expected, actual) {
					err = fmt.Errorf("expected %s; actual %s", step.Frame, e.frame.Data)
				}
			case <-stepCtx.Done():
				if step.Op != "quiet" || ctx.Err() != nil {
					err = fmt.Errorf("step timeout: %w", stepCtx.Err())
				}
			}
		case "input":
			if m.adapter == nil {
				err = fmt.Errorf("BLOCKED: real device input adapter not configured")
			} else if e := m.adapter.Input(stepCtx, step.Text); e != nil {
				err = fmt.Errorf("device input: %w", e)
			}
		case "advance":
			if m.adapter == nil {
				err = fmt.Errorf("BLOCKED: real device clock adapter not configured")
			} else if e := m.adapter.Advance(stepCtx, time.Duration(step.AdvanceMS)*time.Millisecond); e != nil {
				err = fmt.Errorf("device clock: %w", e)
			}
		case "disconnect":
			if e := c.Close(); e != nil {
				err = fmt.Errorf("disconnect: %w", e)
			}
		}
		stop()
		result.DurationMS = time.Since(begin).Milliseconds()
		result.Passed = err == nil
		if err != nil {
			result.Error = err.Error()
			r.Error = fmt.Sprintf("step %d (%s): %v", i, step.Op, err)
		}
		r.Steps = append(r.Steps, result)
		if err != nil {
			break
		}
	}
	cancel()
	// Closing the transport unblocks ReadMessage; cancellation unblocks a full queue.
	if err := c.Close(); err != nil && r.Error == "" && len(m.script.Steps) > 0 && m.script.Steps[len(m.script.Steps)-1].Op != "disconnect" {
		r.Error = fmt.Sprintf("close connection: %v", err)
	}
	<-done
	framesMu.Lock()
	r.Received = append(r.Received, frames...)
	framesMu.Unlock()
	r.Passed = r.Error == ""
	r.DurationMS = time.Since(start).Milliseconds()
	return r
}
