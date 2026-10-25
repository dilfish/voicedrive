package mock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func session(t *testing.T, script Script, device func(*websocket.Conn)) Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reports := make(chan Report, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			return
		}
		reports <- ServeSession(ctx, conn, script)
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	device(conn)
	select {
	case report := <-reports:
		return report
	case <-ctx.Done():
		t.Fatal("session did not complete")
		return Report{}
	}
}

func TestPlatformScripts(t *testing.T) {
	for _, filename := range []string{"platform.json", "tool-error.json"} {
		t.Run(filename, func(t *testing.T) {
			script, err := Load("../../scripts/" + filename)
			if err != nil {
				t.Fatal(err)
			}
			report := session(t, script, func(conn *websocket.Conn) {
				if err := conn.WriteJSON(map[string]any{"type": "hello", "features": map[string]bool{"mcp": true}}); err != nil {
					t.Fatal(err)
				}
				for {
					var frame struct {
						Type    string `json:"type"`
						Payload struct {
							ID     int    `json:"id"`
							Method string `json:"method"`
							Params struct {
								Name      string         `json:"name"`
								Arguments map[string]any `json:"arguments"`
							} `json:"params"`
						} `json:"payload"`
					}
					if err := conn.ReadJSON(&frame); err != nil {
						break
					}
					if frame.Type != "mcp" {
						continue
					}
					if frame.Payload.Method != "tools/call" {
						t.Errorf("unexpected method %q", frame.Payload.Method)
					}
					isError := frame.Payload.Params.Name == "mock.unknown"
					text := "已发送"
					if isError {
						text = "unknown tool"
					} else if frame.Payload.Params.Name != "group.send_voice" || frame.Payload.Params.Arguments["text"] != "我快到了" || frame.Payload.Params.Arguments["group"] != "夜班组" {
						t.Errorf("unexpected tool arguments: %+v", frame.Payload.Params)
					}
					if err := conn.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{"jsonrpc": "2.0", "id": frame.Payload.ID, "result": map[string]any{"isError": isError, "content": []any{map[string]any{"type": "text", "text": text}}}}}); err != nil {
						t.Fatal(err)
					}
					if isError {
						break
					}
				}
			})
			if !report.Passed {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestSessionFailureAndTimeout(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		report := session(t, Script{Steps: []Step{{Name: "hello", Action: "expect", Expect: map[string]any{"type": "hello"}}}}, func(conn *websocket.Conn) { _ = conn.WriteJSON(map[string]string{"type": "wrong"}) })
		if report.Passed || !strings.Contains(report.Results[0].Error, `want "hello"`) || !strings.Contains(report.Results[0].Error, `"wrong"`) {
			t.Fatalf("report = %+v", report)
		}
	})
	t.Run("injected timeout", func(t *testing.T) {
		report := session(t, Script{Steps: []Step{{Action: "timeout", TimeoutMS: 20}}}, func(conn *websocket.Conn) { _, _, _ = conn.ReadMessage() })
		if !report.Passed {
			t.Fatalf("report = %+v", report)
		}
	})
	t.Run("missing reply", func(t *testing.T) {
		report := session(t, Script{Steps: []Step{{Action: "expect", TimeoutMS: 20, Expect: map[string]any{"type": "hello"}}}}, func(conn *websocket.Conn) { _, _, _ = conn.ReadMessage() })
		if report.Passed {
			t.Fatal("missing reply passed")
		}
	})
}

func TestHTTPBodyCaptureAndStop(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"id":"g1"}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/groups/g1" || body["group"] != "g1" {
			t.Errorf("request = %s, %+v", r.URL.Path, body)
		}
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer srv.Close()
	report := Run(context.Background(), srv.URL, Script{Steps: []Step{
		{Path: "/groups", Capture: map[string]string{"id": "id"}},
		{Path: "/groups/${id}", Body: json.RawMessage(`{"group":"${id}"}`), Expect: map[string]any{"ok": true}},
		{Path: "/never"},
	}})
	if report.Passed || calls != 2 || len(report.Results) != 2 {
		t.Fatalf("report = %+v, calls = %d", report, calls)
	}
}

func TestSilentAudioUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if len(data) != 3200 || r.Header.Get("Content-Type") != "audio/pcm" || r.Header.Get("X-VD-MAC") != "test" {
			t.Errorf("upload: %d bytes, headers %v", len(data), r.Header)
		}
		for _, sample := range data {
			if sample != 0 {
				t.Error("non-silent PCM")
				break
			}
		}
	}))
	defer srv.Close()
	report := Run(context.Background(), srv.URL, Script{Steps: []Step{{Path: "/audio", MAC: "test", SilenceMS: 100}}})
	if !report.Passed {
		t.Fatalf("report = %+v", report)
	}
}
