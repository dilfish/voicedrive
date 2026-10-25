package lingxi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func server(t *testing.T, f func(*websocket.Conn, *http.Request)) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error("upgrade failed")
			return
		}
		defer closeBestEffort(c)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		f(c, r)
	}))
	t.Cleanup(s.Close)
	return "ws" + strings.TrimPrefix(s.URL, "http")
}

func readObject(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	var v map[string]any
	if err := c.ReadJSON(&v); err != nil {
		t.Errorf("read JSON: %v", err)
	}
	return v
}

func start(t *testing.T, ctx context.Context, cfg Config) (*Client, <-chan error) {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return c, done
}

func TestProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	events := make(chan json.RawMessage, 2)
	audio := make(chan []byte, 1)
	requests := make(chan json.RawMessage, 2)
	serverDone := make(chan struct{})
	u := server(t, func(ws *websocket.Conn, r *http.Request) {
		defer close(serverDone)
		if r.Header.Get("Authorization") != "Bearer test-only" || r.Header.Get("Protocol-Version") != "1" || r.Header.Get("Device-Id") != "test-mac" || r.Header.Get("Client-Id") == "" || r.UserAgent() != "voicedrive-emu/0.1.0" {
			t.Error("incorrect handshake headers")
		}
		hello := readObject(t, ws)
		want := `{"type":"hello","version":1,"transport":"websocket","features":{"mcp":true},"audio_params":{"format":"pcm","sample_rate":16000,"channels":1,"bit_depth":16,"endianness":"little","frame_duration":20,"frame_size":320,"sample_format":"signed_int16","play_buffer_duration":300}}`
		var expected map[string]any
		_ = json.Unmarshal([]byte(want), &expected)
		got, _ := json.Marshal(hello)
		exp, _ := json.Marshal(expected)
		if string(got) != string(exp) {
			t.Errorf("unexpected hello: %s", got)
		}
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "sid"})
		for _, state := range []string{"start", "detect"} {
			m := readObject(t, ws)
			if m["type"] != "listen" || m["state"] != state || m["session_id"] != "sid" {
				t.Errorf("bad command: %v", m)
			}
			if state == "start" && m["mode"] != "manual" {
				t.Error("missing manual mode")
			}
			if state == "detect" && m["text"] != "hello world" {
				t.Error("missing text")
			}
		}
		mt, data, err := ws.ReadMessage()
		if err != nil || mt != websocket.BinaryMessage || len(data) != 640 {
			t.Error("incorrect upstream audio")
		}
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3})
		_ = ws.WriteJSON(map[string]any{"type": "tts", "state": "start"})
		_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/list"}})
		m := readObject(t, ws)
		p, ok := m["payload"].(map[string]any)
		if m["type"] != "mcp" || !ok || p["id"] != float64(7) {
			t.Error("incorrect MCP reply")
		}
		_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}})
		// The next frame must be the command, not a notification response.
		for _, state := range []string{"start", "detect"} {
			if m := readObject(t, ws); m["state"] != state {
				t.Error("notification produced a response")
			}
		}
	})
	c, done := start(t, ctx, Config{URL: u, MAC: "test-mac", Token: "test-only", OnEvent: func(v json.RawMessage) { events <- v }, OnAudio: func(v []byte) { audio <- v }, OnMCP: func(_ context.Context, v json.RawMessage) json.RawMessage {
		requests <- v
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(v, &req)
		if len(req.ID) == 0 {
			return nil
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{}}`)
	}})
	if err := c.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Command(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}
	if err := c.SendAudio(ctx, make([]byte, 640)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-audio:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// hello may also be forwarded as an event.
	for {
		select {
		case raw := <-events:
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			if m["type"] == "tts" {
				goto eventReceived
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
eventReceived:
	for i := 0; i < 2; i++ {
		select {
		case <-requests:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := c.Command(ctx, "after notification"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-serverDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestReadyAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	seen := make(chan struct{})
	u := server(t, func(ws *websocket.Conn, _ *http.Request) {
		readObject(t, ws)
		_ = ws.WriteJSON(map[string]any{"type": "hello"})
		close(seen)
		_, _, _ = ws.ReadMessage()
	})
	c, done := start(t, ctx, Config{URL: u, MAC: "mac", Token: "test-only"})
	<-seen
	short, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if err := c.WaitReady(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ready before session: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run leaked")
	}
	if err := c.WaitReady(testContext(t)); err == nil {
		t.Error("closed client ready")
	}
	if err := c.Command(testContext(t), "x"); err == nil {
		t.Error("closed command succeeded")
	}
	if err := c.Run(testContext(t)); err == nil {
		t.Error("Run reused client")
	}
}

func TestConcurrentWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	const count = 40
	finished := make(chan struct{})
	u := server(t, func(ws *websocket.Conn, _ *http.Request) {
		readObject(t, ws)
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "sid"})
		for i := 0; i < count*3; {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				t.Error("read concurrent frames")
				return
			}
			i++
			if mt == websocket.BinaryMessage {
				if len(data) != 640 {
					t.Error("bad frame")
				}
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["state"] != "start" {
				t.Error("command batch interleaved")
			}
			if m = readObject(t, ws); m["state"] != "detect" {
				t.Error("command batch interleaved")
			}
			i++
		}
		close(finished)
		_, _, _ = ws.ReadMessage()
	})
	c, done := start(t, ctx, Config{URL: u, MAC: "mac", Token: "test-only"})
	if err := c.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := c.Command(ctx, fmt.Sprint(i)); err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			if err := c.SendAudio(ctx, make([]byte, 640)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run leaked")
	}
}

func TestValidationAndSafeErrors(t *testing.T) {
	for _, u := range []string{"", "://test-only", "https://example.invalid/test-only", "ws://user:test-only@example.invalid"} {
		_, err := New(Config{URL: u, MAC: "mac", Token: "test-only"})
		if err == nil || strings.Contains(err.Error(), "test-only") {
			t.Error("unsafe or missing URL validation error")
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "test-only", http.StatusUnauthorized) }))
	defer s.Close()
	c, err := New(Config{URL: "ws" + strings.TrimPrefix(s.URL, "http") + "/?token=test-only", MAC: "mac", Token: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Run(testContext(t))
	if err == nil || strings.Contains(err.Error(), "test-only") || strings.Contains(err.Error(), "token=") {
		t.Error("unsafe handshake error")
	}
}

func TestPendingOperationsAndCallbackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	entered := make(chan struct{})
	u := server(t, func(ws *websocket.Conn, _ *http.Request) {
		readObject(t, ws)
		_ = ws.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"method": "tools/list", "id": 1}})
		_, _, _ = ws.ReadMessage()
	})
	c, done := start(t, ctx, Config{URL: u, MAC: "mac", Token: "test-only", OnMCP: func(ctx context.Context, _ json.RawMessage) json.RawMessage {
		close(entered)
		<-ctx.Done()
		return nil
	}})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if err := c.Command(short, "not ready"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("pending command: %v", err)
	}
	if err := c.SendAudio(ctx, []byte{1}); err == nil {
		t.Error("invalid PCM accepted")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback cancellation leaked Run")
	}
}

func TestPingAndSafeCloseError(t *testing.T) {
	ctx := testContext(t)
	u := server(t, func(ws *websocket.Conn, _ *http.Request) {
		readObject(t, ws)
		_ = ws.WriteJSON(map[string]any{"type": "hello", "session_id": "sid"})
		ws.SetPongHandler(func(data string) error {
			if data != "ping" {
				t.Error("incorrect pong")
			}
			return ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "test-only-private-server-error"))
		})
		_ = ws.WriteMessage(websocket.PingMessage, []byte("ping"))
		_, _, _ = ws.ReadMessage()
	})
	_, done := start(t, ctx, Config{URL: u, MAC: "mac", Token: "test-only"})
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "test-only") {
			t.Error("raw server close reason escaped")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
