package grouptools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestDispatcher(t *testing.T, handler http.HandlerFunc, observer func(string, map[string]any, Response)) *Dispatcher {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	d, err := New(Config{BaseURL: s.URL, MAC: "device-a", HTTPClient: s.Client(), OnResult: observer})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func rpc(t *testing.T, d *Dispatcher, raw string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(d.Handle(context.Background(), json.RawMessage(raw)), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProtocolAndSchemas(t *testing.T) {
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") }, nil)
	init := rpc(t, d, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)["result"].(map[string]any)
	if init["protocolVersion"] != "2024-11-05" || !reflect.DeepEqual(init["capabilities"], map[string]any{"tools": map[string]any{}}) {
		t.Fatal(init)
	}
	tools := rpc(t, d, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`)["result"].(map[string]any)["tools"].([]any)
	expected := map[string]map[string]string{
		"group.create": {"name": "string", "passcode": "string"}, "group.join": {"passcode": "string"},
		"group.switch": {"name": "string"}, "group.mute": {"name": "string", "on": "boolean"},
		"group.list": {}, "group.leave": {"name": "string"}, "group.rename": {"name": "string", "new_name": "string"},
		"group.pat": {"alias": "string"}, "group.invite": {"alias": "string", "group_name": "string"}, "group.query_member": {"group_name": "string", "alias": "string"},
	}
	if len(tools) != len(expected) {
		t.Fatal(tools)
	}
	for _, item := range tools {
		tool := item.(map[string]any)
		name := tool["name"].(string)
		want, ok := expected[name]
		if !ok {
			t.Fatal(name)
		}
		delete(expected, name)
		if tool["description"] == "" {
			t.Fatal(tool)
		}
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatal(schema)
		}
		props := schema["properties"].(map[string]any)
		if len(props) != len(want) {
			t.Fatal(props)
		}
		for key, prop := range props {
			typ := prop.(map[string]any)["type"]
			if typ != "string" && typ != "boolean" && typ != "integer" {
				t.Fatal(typ)
			}
			if typ != want[key] {
				t.Fatal(prop)
			}
		}
		required := map[string]bool{}
		if values, ok := schema["required"].([]any); ok {
			for _, v := range values {
				required[v.(string)] = true
			}
		}
		for key := range want {
			if required[key] != (key != "passcode" || name != "group.create") {
				t.Fatalf("required %s: %v", name, required)
			}
		}
	}
}

func TestRoundTrips(t *testing.T) {
	cases := []struct{ name, args string }{
		{"create", `{"name":"车队","passcode":"12345"}`}, {"join", `{"passcode":"12345"}`}, {"switch", `{"name":"车队"}`},
		{"mute", `{"name":"车队","on":false}`}, {"list", `{}`}, {"leave", `{"name":"车队"}`}, {"rename", `{"name":"车队","new_name":"夜班"}`},
		{"pat", `{"alias":"老王"}`}, {"invite", `{"alias":"老王","group_name":"车队"}`}, {"query_member", `{"alias":"老王","group_name":"车队"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				method := "POST"
				if tc.name == "list" {
					method = "GET"
				}
				if r.Method != method || r.URL.Path != "/v1/group/"+tc.name || r.Header.Get("X-VD-MAC") != "device-a" {
					t.Errorf("bad request %s %s", r.Method, r.URL)
				}
				if len(r.Header.Get("Idempotency-Key")) != 64 {
					t.Error("missing SHA256")
				}
				if method == "POST" {
					var got, want map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					_ = json.Unmarshal([]byte(tc.args), &want)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("%v != %v", got, want)
					}
				}
				fmt.Fprint(w, `{"status":"ok","message":"操作成功"}`)
			}, nil)
			result := rpc(t, d, fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"group.%s","arguments":%s}}`, tc.name, tc.args))
			body := result["result"].(map[string]any)
			if body["isError"] != false || body["content"].([]any)[0].(map[string]any)["text"] != "操作成功" || calls != 1 {
				t.Fatal(result, calls)
			}
		})
	}
}

func TestInvalidRequestsAndNotifications(t *testing.T) {
	var calls atomic.Int32
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"status":"ok","message":"好了"}`)
	}, nil)
	cases := []struct {
		raw  string
		code float64
	}{
		{`{`, -32700}, {`[]`, -32600}, {`null`, -32600}, {`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`, -32600},
		{`{"jsonrpc":"2.0","id":{},"method":"tools/list"}`, -32600}, {`{"jsonrpc":"2.0","id":1,"method":"missing"}`, -32601},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing"}}`, -32601},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[]}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.join","arguments":{}}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.mute","arguments":{"name":"a","on":"true"}}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.join","arguments":{"passcode":null}}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.join","arguments":{"passcode":"  "}}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.join","arguments":{"passcode":"1234","mac":"forged"}}}`, -32602},
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.list","arguments":[]}}`, -32602},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := rpc(t, d, tc.raw)
			if got["error"].(map[string]any)["code"] != tc.code {
				t.Fatal(got)
			}
		})
	}
	for _, raw := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","method":"missing"}`, `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"group.join","arguments":{"passcode":"1234"}}}`} {
		if got := d.Handle(context.Background(), json.RawMessage(raw)); len(got) != 0 {
			t.Fatal(string(got))
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid calls reached HTTP: %d", calls.Load())
	}
}

func TestClarifyObserverAndNormalization(t *testing.T) {
	var keys []string
	var observed int
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		fmt.Fprint(w, `{"status":"clarify","message":"你说的是哪个群？","options":["甲","乙","丙","丁"]}`)
	}, func(name string, args map[string]any, response Response) {
		observed++
		if name != "group.invite" || args["alias"] != "老王" || response.Status != "clarify" || len(response.Options) != 3 {
			t.Errorf("bad observation %s %v %+v", name, args, response)
		}
		args["alias"] = "mutation"
		response.Options[0] = "mutation"
	})
	args := map[string]any{"alias": "老王", "group_name": "车队"}
	a, err := d.Call(context.Background(), "group.invite", args)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Call(context.Background(), "group.invite", map[string]any{"group_name": "车队", "alias": "老王"})
	if err != nil {
		t.Fatal(err)
	}
	if observed != 2 || keys[0] != keys[1] || args["alias"] != "老王" || a.Options[0] != "甲" {
		t.Fatal(observed, keys, args, a)
	}
}

func TestFailuresTimeoutAndLength(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"http", `{}`, 500}, {"invalid", `not json`, 200}, {"status", `{"status":"unknown","message":"x"}`, 200}, {"long", `{"status":"ok","message":"` + strings.Repeat("好", 60) + `"}`, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }, nil)
			response, err := d.Call(context.Background(), "group.list", nil)
			if utf8.RuneCountInString(response.Message) > 40 {
				t.Fatal(response)
			}
			if tc.name != "long" && (err == nil || response.Message != "网络不太好，再说一遍试试") {
				t.Fatal(response, err)
			}
			result := rpc(t, d, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.list"}}`)
			if result["error"] != nil {
				t.Fatal(result)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, nil)
		start := time.Now()
		response, err := d.Call(context.Background(), "group.list", nil)
		if err == nil || response.Message != "网络不太好，再说一遍试试" || time.Since(start) > 4*time.Second || time.Since(start) < 2800*time.Millisecond {
			t.Fatal(response, err, time.Since(start))
		}
	})
}

func TestConcurrentCalls(t *testing.T) {
	var seen atomic.Int32
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok","message":"好了"}`) }, func(string, map[string]any, Response) { seen.Add(1) })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Call(context.Background(), "group.list", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if seen.Load() != 20 {
		t.Fatal(seen.Load())
	}
}

func TestConfig(t *testing.T) {
	for _, cfg := range []Config{{}, {BaseURL: "ftp://example", MAC: "a"}, {BaseURL: "http://example?x=y", MAC: "a"}, {BaseURL: "http://example", MAC: "a\r\nforged"}} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted invalid config")
		}
	}
}

func TestHandleClarifyAndFingerprint(t *testing.T) {
	var observed atomic.Int32
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		want := fmt.Sprintf("%x", sha256.Sum256([]byte("group.switch\n{\"name\":\"车队\"}")))
		if r.Header.Get("Idempotency-Key") != want {
			t.Errorf("fingerprint differs from canonical tool + arguments SHA256")
		}
		fmt.Fprint(w, `{"status":"clarify","message":"你说的是哪个群？","options":["甲","乙"]}`)
	}, func(name string, args map[string]any, response Response) {
		observed.Add(1)
		if name != "group.switch" || args["name"] != "车队" || !reflect.DeepEqual(response.Options, []string{"甲", "乙"}) {
			t.Errorf("lost confirmation context: %s %v %+v", name, args, response)
		}
	})
	result := rpc(t, d, `{"jsonrpc":"2.0","id":"clarify","method":"tools/call","params":{"name":"group.switch","arguments":{"name":"车队"}}}`)
	text := result["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != "你说的是哪个群？" || observed.Load() != 1 {
		t.Fatal(result, observed.Load())
	}
}

func TestRejectIdentityAndUnknownArguments(t *testing.T) {
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid arguments reached HTTP") }, nil)
	for _, key := range []string{"mac", "from_mac", "to_mac", "identity", "device_id", "X-VD-MAC", "alias", "unknown"} {
		t.Run(key, func(t *testing.T) {
			_, err := d.Call(context.Background(), "group.join", map[string]any{"passcode": "1234", key: "forged"})
			var protocolErr *rpcError
			if !errors.As(err, &protocolErr) || protocolErr.Code != -32602 {
				t.Fatal(err)
			}
		})
	}
}
