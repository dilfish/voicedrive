package scriptmock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMCPProtocol(t *testing.T) {
	for _, method := range []string{"initialize", "tools/list", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			request := json.RawMessage(fmt.Sprintf(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":1,"method":%q,"params":{"name":"group.create","arguments":{"name":"夜班组"}}}}`, method))
			response := json.RawMessage(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":1,"result":{"isError":false,"content":[{"type":"text","text":"群建好了"}]}}}`)
			m, err := New(Script{Version: 1, TimeoutMS: 1000, Steps: []Step{{Op: "send", Frame: request}, {Op: "expect", Frame: response}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := httptest.NewServer(m)
			defer s.Close()
			result := make(chan Report, 1)
			go func() { result <- m.Run(context.Background()) }()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			_, data, err := c.ReadMessage()
			if err != nil || string(data) != string(request) {
				t.Fatalf("request=%s err=%v", data, err)
			}
			if err := c.WriteMessage(websocket.TextMessage, response); err != nil {
				t.Fatal(err)
			}
			if r := <-result; !r.Passed || len(r.Received) != 1 {
				t.Fatalf("report=%+v", r)
			}
		})
	}
}

func TestAdapter(t *testing.T) {
	for _, status := range []int{204, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer s.Close()
			a, err := NewHTTPAdapter(s.URL)
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range []error{a.Input(context.Background(), "小牛建群"), a.Advance(context.Background(), time.Second)} {
				if (err != nil) != (status != 204) {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
	for _, url := range []string{"https://example.com", "http://127.0.0.1/?token=x", "http://user:pass@localhost/"} {
		if _, err := NewHTTPAdapter(url); err == nil {
			t.Fatal("accepted unsafe adapter")
		}
	}
}

func TestBinaryAndPeerClose(t *testing.T) {
	for _, binary := range []bool{true, false} {
		t.Run(fmt.Sprint(binary), func(t *testing.T) {
			m, err := New(Script{Version: 1, TimeoutMS: 500, Steps: []Step{{Op: "quiet", TimeoutMS: 100}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := httptest.NewServer(m)
			defer s.Close()
			done := make(chan Report, 1)
			go func() { done <- m.Run(context.Background()) }()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if binary {
				if err := c.WriteMessage(websocket.BinaryMessage, []byte{1, 2}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			}
			r := <-done
			if binary {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if r.Passed {
				t.Fatal("unexpected success")
			}
			if binary && len(r.Received) != 1 {
				t.Fatal("binary not recorded")
			}
		})
	}
}

func TestIndependentInstances(t *testing.T) {
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			m, err := New(Script{Version: 1, TimeoutMS: 20, Steps: []Step{{Op: "quiet"}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := m.Run(context.Background())
			if r.Passed || !strings.Contains(r.Error, "waiting for device") {
				t.Fatalf("report=%+v", r)
			}
		})
	}
}
