package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// ServeSession accepts a single device and runs a platform script. The caller
// owns the HTTP server and persists the returned report. No audio hardware is used.
func ServeSession(ctx context.Context, conn *websocket.Conn, script Script) Report {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	conn.SetReadLimit(1 << 20)
	report := Report{Name: script.Name, Passed: len(script.Steps) > 0, Results: []Result{}}
	for _, step := range script.Steps {
		started := time.Now()
		timeout := 3 * time.Second
		if step.TimeoutMS > 0 {
			timeout = time.Duration(step.TimeoutMS) * time.Millisecond
		}
		deadline := started.Add(timeout)
		if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
			deadline = limit
		}
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
		err := websocketStep(conn, step)
		result := Result{Name: step.Name, Passed: err == nil, Duration: time.Since(started)}
		if err != nil {
			result.Error = err.Error()
			report.Passed = false
		}
		report.Results = append(report.Results, result)
		if err != nil {
			break
		}
	}
	return report
}

func websocketStep(conn *websocket.Conn, step Step) error {
	switch step.Action {
	case "send":
		if !json.Valid(step.Send) {
			return fmt.Errorf("send requires valid JSON")
		}
		return conn.WriteMessage(websocket.TextMessage, step.Send)
	case "expect":
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("expected device JSON, actual read error: %w", err)
		}
		if kind != websocket.TextMessage {
			return fmt.Errorf("expected text frame, actual type %d", kind)
		}
		var actual any
		if err := json.Unmarshal(data, &actual); err != nil {
			return fmt.Errorf("decode device frame: %w", err)
		}
		if len(step.Expect) == 0 {
			return fmt.Errorf("expect requires field assertions")
		}
		return assertFields(actual, step.Expect)
	case "timeout":
		// Deliberately withhold the platform response for the configured interval.
		_, data, err := conn.ReadMessage()
		if err != nil {
			if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
				return nil
			}
			return fmt.Errorf("expected timeout, actual error: %w", err)
		}
		return fmt.Errorf("expected timeout, actual frame: %s", data)
	case "disconnect":
		return conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseServiceRestart, "scripted disconnect"), time.Now().Add(time.Second))
	default:
		return fmt.Errorf("unsupported WebSocket action %q", step.Action)
	}
}

// Upgrade upgrades a device connection using the standard same-origin policy.
func Upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return (&websocket.Upgrader{HandshakeTimeout: 3 * time.Second}).Upgrade(w, r, nil)
}
