package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Dispatcher 是 JSON-RPC 2.0 分发器；无状态、可并发调用。
type Dispatcher struct {
	reg *Registry
}

// NewDispatcher 构造携带空注册表的分发器，工具经 Register 注入。
func NewDispatcher() *Dispatcher { return &Dispatcher{reg: NewRegistry()} }

// Register 委托注册表（见 Registry.Register 的校验规则）。
func (d *Dispatcher) Register(t Tool) error { return d.reg.Register(t) }

// Handle 处理一条 JSON-RPC 请求并返回应答字节：
//   - 畸形 payload → -32700；未知方法/工具 → -32601；参数非法 → -32602；
//   - 通知（无 id）不应答，返回 nil。
func (d *Dispatcher) Handle(_ context.Context, raw []byte) []byte {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return mustJSON(Response{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &RPCError{Code: CodeParseError, Message: fmt.Sprintf("parse error: %v", err)}})
	}
	if len(req.ID) == 0 {
		return nil // 通知不应答
	}
	resp := d.dispatch(req)
	resp.ID = req.ID
	return mustJSON(resp)
}

func (d *Dispatcher) dispatch(req Request) Response {
	switch req.Method {
	case "initialize":
		return okResp(map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": true},
			"serverInfo":      map[string]any{"name": ServerName, "version": ServerVersion},
		})
	case "tools/list":
		tools := make([]map[string]any, 0, len(d.reg.order))
		for _, t := range d.reg.List() {
			tools = append(tools, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.inputSchema(),
			})
		}
		return okResp(map[string]any{"tools": tools})
	case "tools/call":
		return d.toolsCall(req.Params)
	default:
		return errResp(CodeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

func (d *Dispatcher) toolsCall(params json.RawMessage) Response {
	var p struct {
		Name      string          `json:"name"`
		Arguments *map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return errResp(CodeInvalidParams, "invalid params")
	}
	tool, ok := d.reg.Get(p.Name)
	if !ok {
		return errResp(CodeMethodNotFound, fmt.Sprintf("unknown tool %q", p.Name))
	}
	args := Args{}
	if p.Arguments != nil {
		args = Args(*p.Arguments)
	}
	if err := tool.validateArgs(args); err != nil {
		return errResp(CodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}
	return okResp(map[string]any{
		"isError": false,
		"content": []any{map[string]any{"type": "text", "text": tool.Handler(args)}},
	})
}

func okResp(result any) Response { return Response{JSONRPC: "2.0", Result: result} }
func errResp(code int, msg string) Response {
	return Response{JSONRPC: "2.0", Error: &RPCError{Code: code, Message: msg}}
}

func mustJSON(r Response) []byte {
	b, err := json.Marshal(r)
	if err != nil {
		// Response 只含 map/slice/字符串，Marshal 不可能失败；保底返回协议级错误。
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	return b
}
