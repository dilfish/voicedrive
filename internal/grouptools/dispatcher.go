// Package grouptools exposes the device's fixed group-management MCP tools.
package grouptools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Response is the server's business result, including disambiguation choices.
type Response struct {
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Options []string `json:"options,omitempty"`
}

// Config is immutable after New. OnResult runs synchronously for each completed
// HTTP attempt (including failures), with independent argument/option copies.
// Concurrent callers may invoke OnResult concurrently; the observer must be safe
// for that usage. Validation failures do not invoke it.
type Config struct {
	BaseURL    string
	MAC        string
	HTTPClient *http.Client
	OnResult   func(name string, args map[string]any, response Response)
}

type Dispatcher struct {
	baseURL  string
	mac      string
	client   *http.Client
	onResult func(string, map[string]any, Response)
	tools    []tool
}

type property struct {
	Name, Type, Description string
	Optional                bool
}
type tool struct {
	Name, Description string
	Properties        []property
}

// New constructs a dispatcher without making any network requests.
func New(cfg Config) (*Dispatcher, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid group API base URL")
	}
	if strings.TrimSpace(cfg.MAC) == "" || strings.ContainsAny(cfg.MAC, "\r\n") {
		return nil, errors.New("invalid device MAC")
	}
	client := http.Client{}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	// Do not forward device identity or replay operations to a redirect target.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Dispatcher{baseURL: strings.TrimRight(cfg.BaseURL, "/"), mac: cfg.MAC, client: &client, onResult: cfg.OnResult, tools: registry()}, nil
}

func registry() []tool {
	s := func(name, desc string) property { return property{Name: name, Type: "string", Description: desc} }
	return []tool{
		{"group.create", "建群；用户未口述口令时勿填，由系统生成", []property{s("name", "新群名称"), {Name: "passcode", Type: "string", Description: "用户口述的入群口令", Optional: true}}},
		{"group.join", "通过口令入群，入群后即可直接说话", []property{s("passcode", "入群口令")}},
		{"group.switch", "把我的焦点切到某群，群名可模糊", []property{s("name", "目标群名称")}},
		{"group.mute", "静音或取消静音某群的收听", []property{s("name", "目标群名称"), {Name: "on", Type: "boolean", Description: "true 静音，false 取消静音"}}},
		{"group.list", "查询我加入的所有群与当前焦点群", nil},
		{"group.leave", "直接退出指定群", []property{s("name", "要退出的群名称")}},
		{"group.rename", "修改指定群的名称", []property{s("name", "原群名称"), s("new_name", "新群名称")}},
		{"group.pat", "拍一拍某人，对方决定是否切群", []property{s("alias", "对方别名")}},
		{"group.invite", "邀请好友进群，对方须确认", []property{s("alias", "好友别名"), s("group_name", "邀请加入的群名称")}},
		{"group.query_member", "查询某群里是否有某人", []property{s("group_name", "要查询的群名称"), s("alias", "要查询的成员别名")}},
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }
func invalidParams() error        { return &rpcError{-32602, "invalid params"} }

func (d *Dispatcher) validate(name string, args map[string]any) (map[string]any, error) {
	var found *tool
	for i := range d.tools {
		if d.tools[i].Name == name {
			found = &d.tools[i]
			break
		}
	}
	if found == nil {
		return nil, &rpcError{-32601, "unknown tool"}
	}
	clean := make(map[string]any, len(args))
	for key, value := range args {
		var spec *property
		for i := range found.Properties {
			if found.Properties[i].Name == key {
				spec = &found.Properties[i]
				break
			}
		}
		if spec == nil {
			return nil, invalidParams()
		}
		switch spec.Type {
		case "string":
			v, ok := value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return nil, invalidParams()
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return nil, invalidParams()
			}
		default:
			return nil, invalidParams()
		}
		clean[key] = value
	}
	for _, p := range found.Properties {
		if _, ok := clean[p.Name]; !ok && !p.Optional {
			return nil, invalidParams()
		}
	}
	return clean, nil
}

// Call validates and forwards one tool invocation. Network/HTTP/response failures
// return both a fixed, speakable business error response and a non-nil Go error.
// Unknown tools and invalid arguments instead return JSON-RPC-compatible errors.
func (d *Dispatcher) Call(ctx context.Context, name string, args map[string]any) (response Response, err error) {
	clean, err := d.validate(name, args)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		if d.onResult != nil {
			copied := make(map[string]any, len(clean))
			for k, v := range clean {
				copied[k] = v
			}
			observed := response
			observed.Options = append([]string(nil), response.Options...)
			d.onResult(name, copied, observed)
		}
	}()
	response = Response{Status: "error", Message: "网络不太好，再说一遍试试"}
	body, _ := json.Marshal(clean)
	// encoding/json sorts map keys. The tool name and canonical JSON are separated
	// by a newline so identical argument maps always produce the same fingerprint.
	fingerprint := sha256.Sum256(append([]byte(name+"\n"), body...))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	method := http.MethodPost
	var reader io.Reader = bytes.NewReader(body)
	if name == "group.list" {
		method = http.MethodGet
		reader = nil
	}
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+"/v1/group/"+strings.TrimPrefix(name, "group."), reader)
	if err != nil {
		return response, err
	}
	req.Header.Set("X-VD-MAC", d.mac)
	req.Header.Set("Idempotency-Key", hex.EncodeToString(fingerprint[:]))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return response, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return response, fmt.Errorf("group API HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return response, err
	}
	if len(data) > 1<<20 {
		return response, errors.New("group API response too large")
	}
	var result Response
	if err := json.Unmarshal(data, &result); err != nil {
		return response, err
	}
	if (result.Status != "ok" && result.Status != "error" && result.Status != "clarify") || strings.TrimSpace(result.Message) == "" {
		return response, errors.New("invalid group API response")
	}
	runes := []rune(result.Message)
	if len(runes) > 40 {
		result.Message = string(runes[:40])
	}
	if len(result.Options) > 3 {
		result.Options = result.Options[:3]
	}
	return result, nil
}

func (d *Dispatcher) list() any {
	items := make([]any, 0, len(d.tools))
	for _, t := range d.tools {
		props := map[string]any{}
		required := []string{}
		for _, p := range t.Properties {
			props[p.Name] = map[string]any{"type": p.Type, "description": p.Description}
			if !p.Optional {
				required = append(required, p.Name)
			}
		}
		items = append(items, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}})
	}
	return map[string]any{"tools": items}
}

func reply(id json.RawMessage, result any, err *rpcError) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	r := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		r["error"] = err
	} else {
		r["result"] = result
	}
	data, _ := json.Marshal(r)
	return data
}

// Handle processes one JSON-RPC 2.0 message. Valid notifications execute but
// return nil, including unknown methods and invalid notification parameters.
// JSON-RPC batch messages are not supported by this MVP.
func (d *Dispatcher) Handle(ctx context.Context, raw json.RawMessage) json.RawMessage {
	if !json.Valid(raw) {
		return reply(nil, nil, &rpcError{-32700, "parse error"})
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return reply(nil, nil, &rpcError{-32600, "invalid request"})
	}
	id, hasID := envelope["id"]
	if hasID {
		var value any
		_ = json.Unmarshal(id, &value)
		switch value.(type) {
		case nil, string, float64:
		default:
			return reply(nil, nil, &rpcError{-32600, "invalid request"})
		}
	}
	var version, method string
	if json.Unmarshal(envelope["jsonrpc"], &version) != nil || version != "2.0" || json.Unmarshal(envelope["method"], &method) != nil || method == "" {
		return reply(id, nil, &rpcError{-32600, "invalid request"})
	}
	result, protocolErr := d.dispatch(ctx, method, envelope["params"])
	if !hasID {
		return nil
	}
	return reply(id, result, protocolErr)
}

func (d *Dispatcher) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	if len(params) > 0 {
		trim := bytes.TrimSpace(params)
		if len(trim) == 0 || trim[0] != '{' {
			return nil, &rpcError{-32602, "invalid params"}
		}
	}
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "voicedrive-device", "version": "0.1.0"}}, nil
	case "tools/list":
		return d.list(), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.Name == "" {
			return nil, &rpcError{-32602, "invalid params"}
		}
		args := map[string]any{}
		if len(p.Arguments) > 0 {
			if bytes.Equal(bytes.TrimSpace(p.Arguments), []byte("null")) || json.Unmarshal(p.Arguments, &args) != nil {
				return nil, &rpcError{-32602, "invalid params"}
			}
		}
		response, err := d.Call(ctx, p.Name, args)
		var protocolErr *rpcError
		if errors.As(err, &protocolErr) {
			return nil, protocolErr
		}
		return map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": response.Message}}}, nil
	default:
		return nil, &rpcError{-32601, "method not found"}
	}
}
