package commandapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestInputToBackend(t *testing.T) {
	for _, wake := range []string{"小牛", "HeyBot"} {
		t.Run(wake, func(t *testing.T) {
			backendCalls := make(chan string, 2)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-VD-MAC") != "TEST-01" {
					t.Error("missing trusted identity")
				}
				backendCalls <- r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"status":"ok","message":"群建好了"}`)); err != nil {
					t.Error(err)
				}
			}))
			defer backend.Close()
			platformResult := make(chan error, 1)
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u := websocket.Upgrader{}
				c, err := u.Upgrade(w, r, nil)
				if err != nil {
					platformResult <- err
					return
				}
				defer c.Close()
				if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					platformResult <- err
					return
				}
				if _, _, err := c.ReadMessage(); err != nil {
					platformResult <- err
					return
				}
				if err := c.WriteJSON(map[string]any{"type": "hello", "session_id": "s1"}); err != nil {
					platformResult <- err
					return
				}
				for {
					var frame map[string]any
					if err := c.ReadJSON(&frame); err != nil {
						platformResult <- err
						return
					}
					if frame["type"] == "listen" && frame["state"] == "detect" {
						if frame["text"] != "建个群" {
							t.Errorf("text=%v", frame["text"])
						}
						break
					}
				}
				request := json.RawMessage(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.create","arguments":{"name":"夜班组"}}}}`)
				if err := c.WriteMessage(websocket.TextMessage, request); err != nil {
					platformResult <- err
					return
				}
				var reply map[string]any
				err = c.ReadJSON(&reply)
				if err == nil && reply["type"] != "mcp" {
					t.Errorf("reply=%v", reply)
				}
				platformResult <- err
			}))
			defer platform.Close()
			a, err := New(Config{URL: "ws" + strings.TrimPrefix(platform.URL, "http"), Server: backend.URL, MAC: "TEST-01", Token: "local-test-placeholder", WakeWord: wake})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			if err := a.WaitReady(ctx); err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
			// This plain input must not reach the platform; its server's first detect
			// assertion will reject it if routing leaks it.
			if err := a.Input(ctx, "建个群吧"); err != nil {
				t.Fatal(err)
			}
			if err := a.Input(ctx, wake+"，建个群"); err != nil {
				t.Fatal(err)
			}
			select {
			case path := <-backendCalls:
				if path != "/v1/group/create" {
					t.Fatalf("path=%s", path)
				}
			case <-ctx.Done():
				t.Fatal("no backend call")
			}
			if err := <-platformResult; err != nil {
				t.Fatal(err)
			}
			cancel()
			<-done
		})
	}
}
