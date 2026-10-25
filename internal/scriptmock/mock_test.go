package scriptmock

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRun(t *testing.T) {
	for _, tt := range []struct {
		name   string
		steps  []Step
		frames []string
		pass   bool
	}{
		{"roundtrip", []Step{{Op: "expect", Frame: json.RawMessage(`{"type":"hello"}`)}, {Op: "send", Frame: json.RawMessage(`{"type":"mcp","payload":{"method":"tools/list"}}`)}, {Op: "expect", Frame: json.RawMessage(`{"type":"mcp","payload":{"result":{"tools":[]}}}`)}}, []string{`{"type":"hello"}`, `{"type":"mcp","payload":{"result":{"tools":[]}}}`}, true},
		{"mismatch", []Step{{Op: "expect", Frame: json.RawMessage(`{"tool":"group.create"}`)}}, []string{`{"tool":"group.join"}`}, false},
		{"wrong parameters", []Step{{Op: "expect", Frame: json.RawMessage(`{"arguments":{"name":"夜班组"}}`)}}, []string{`{"arguments":{"name":"家庭群"}}`}, false},
		{"isError", []Step{{Op: "expect", Frame: json.RawMessage(`{"result":{"isError":true}}`)}}, []string{`{"result":{"isError":true}}`}, true},
		{"business error", []Step{{Op: "expect", Frame: json.RawMessage(`{"status":"error","message":"群不存在"}`)}}, []string{`{"status":"error","message":"群不存在"}`}, true},
		{"response mismatch", []Step{{Op: "expect", Frame: json.RawMessage(`{"result":{"isError":false}}`)}}, []string{`{"result":{"isError":true}}`}, false},
		{"timeout", []Step{{Op: "expect", Frame: json.RawMessage(`{}`), TimeoutMS: 20}}, nil, false},
		{"quiet", []Step{{Op: "quiet", TimeoutMS: 20}}, nil, true},
		{"unexpected traffic", []Step{{Op: "quiet", TimeoutMS: 20}}, []string{`{"type":"listen"}`}, false},
		{"disconnect", []Step{{Op: "disconnect"}}, nil, true},
		{"missing adapter", []Step{{Op: "input", Text: "小牛建群"}}, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, err := New(Script{Version: 1, TimeoutMS: 500, Steps: tt.steps}, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := httptest.NewServer(m)
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan Report, 1)
			go func() { done <- m.Run(ctx) }()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/?mac=test", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			for _, f := range tt.frames {
				if err := c.WriteMessage(websocket.TextMessage, []byte(f)); err != nil {
					t.Fatal(err)
				}
			}
			r := <-done
			if r.Passed != tt.pass {
				t.Fatalf("report=%+v", r)
			}
		})
	}
}

func TestInvalidScript(t *testing.T) {
	for _, input := range []string{`{`, `{"version":1,"unknown":true}`, `{"version":1,"steps":[]}`, `{"version":1,"steps":[{"op":"send","frame":null}]}`, `{"version":1,"steps":[{"op":"invent"}]}`} {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatal("accepted invalid script")
			}
		})
	}
}
