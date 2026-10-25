package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func newDispatcher(t *testing.T) *Dispatcher {
	d := NewDispatcher()
	require.NoError(t, d.Register(Tool{
		Name:        "group.create",
		Description: "建群",
		Properties: []Property{
			{Name: "name", Type: "string", Required: true},
			{Name: "passcode", Type: "string"},
		},
		Handler: func(args Args) string { return "群建好了" },
	}))
	require.NoError(t, d.Register(Tool{
		Name:        "group.mute",
		Description: "静音",
		Properties: []Property{
			{Name: "name", Type: "string", Required: true},
			{Name: "on", Type: "bool", Required: true},
		},
		Handler: func(args Args) string { return "已静音" },
	}))
	return d
}

func TestDispatcher_Initialize(t *testing.T) {
	d := newDispatcher(t)
	got := jsonBytes(t, d.Handle(nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)))
	want := map[string]any{
		"jsonrpc": "2.0", "id": float64(1),
		"result": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": true},
			"serverInfo":      map[string]any{"name": "voicedrive-device", "version": "0.1.0"},
		},
	}
	require.Equal(t, want, got)
}

func TestDispatcher_ToolsList(t *testing.T) {
	d := newDispatcher(t)
	got := jsonBytes(t, d.Handle(nil, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)))
	result := got["result"].(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, 2)
	first := tools[0].(map[string]any)
	require.Equal(t, "group.create", first["name"])
	require.Equal(t, "建群", first["description"])
	schema := first["inputSchema"].(map[string]any)
	require.Equal(t, "object", schema["type"])
	props := schema["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "string"}, props["name"])
	require.ElementsMatch(t, []any{"name"}, schema["required"])
	// bool 类型工具 schema 同样只含三种基础类型
	second := tools[1].(map[string]any)
	props2 := second["inputSchema"].(map[string]any)["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "boolean"}, props2["on"])
}

func TestDispatcher_ToolsCall(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    map[string]any
	}{
		{"string 参数", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"group.create","arguments":{"name":"夜班组"}}}`, map[string]any{
			"jsonrpc": "2.0", "id": float64(3),
			"result": map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": "群建好了"}}},
		}},
		{"混合 bool 参数", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"group.mute","arguments":{"name":"夜班组","on":true}}}`, map[string]any{
			"jsonrpc": "2.0", "id": float64(4),
			"result": map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": "已静音"}}},
		}},
		{"缺 arguments 视作空参调用", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"group.create"}}`, map[string]any{
			"jsonrpc": "2.0", "id": float64(5),
			"result": map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": "群建好了"}}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDispatcher(t)
			require.Equal(t, tt.want, jsonBytes(t, d.Handle(nil, []byte(tt.payload))))
		})
	}
}

func TestDispatcher_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		code    int
		message string
	}{
		{"畸形 JSON", `{`, -32700, "parse error"},
		{"未知方法", `{"jsonrpc":"2.0","id":6,"method":"resources/list","params":{}}`, -32601, "unknown method"},
		{"未知工具", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"group.nope","arguments":{}}}`, -32601, "unknown tool"},
		{"params 非对象", `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":"x"}`, -32602, "invalid params"},
		{"arguments 非对象", `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"group.create","arguments":123}}`, -32602, "invalid params"},
		{"参数类型非法", `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"group.create","arguments":{"name":123}}}`, -32602, "invalid params"},
		{"bool 工具收 string", `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"group.mute","arguments":{"name":"x","on":"yes"}}}`, -32602, "invalid params"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDispatcher(t)
			got := jsonBytes(t, d.Handle(nil, []byte(tt.payload)))
			require.Equal(t, "2.0", got["jsonrpc"])
			errObj := got["error"].(map[string]any)
			require.Equal(t, float64(tt.code), errObj["code"])
			require.Contains(t, errObj["message"], tt.message)
		})
	}
}

func TestDispatcher_Notification(t *testing.T) {
	d := newDispatcher(t)
	require.Nil(t, d.Handle(nil, []byte(`{"jsonrpc":"2.0","method":"tools/list","params":{}}`)))
}

func TestRegistry_RegisterValidation(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
		want string
	}{
		{"缺 name", Tool{Description: "d", Handler: func(Args) string { return "" }}, "empty name"},
		{"缺 description", Tool{Name: "t", Handler: func(Args) string { return "" }}, "empty description"},
		{"缺 handler", Tool{Name: "t", Description: "d"}, "nil handler"},
		{"类型违规 float", Tool{Name: "t", Description: "d", Handler: func(Args) string { return "" },
			Properties: []Property{{Name: "p", Type: "float"}}}, "property type"},
		{"类型违规 array", Tool{Name: "t", Description: "d", Handler: func(Args) string { return "" },
			Properties: []Property{{Name: "p", Type: "array"}}}, "property type"},
		{"重复注册", Tool{Name: "group.create", Description: "d", Handler: func(Args) string { return "" }}, "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDispatcher(t)
			err := d.Register(tt.tool)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func jsonBytes(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	require.NotNil(t, raw)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	return got
}
