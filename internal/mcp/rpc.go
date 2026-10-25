// Package mcp 实现设备侧 MCP 服务器（02-linx §6，规则：hardware-mcp）：
// 灵犀 = MCP 客户端，本设备 = MCP 服务器；JSON-RPC 2.0 payload 由 linx
// WS 客户端从 type:"mcp" 信封剥出后交给本包 Dispatcher。
package mcp

import "encoding/json"

// JSON-RPC 2.0 / MCP 错误码。
const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
)

// protocolVersion 与 serverInfo 按 hardware-mcp 规则固定。
const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "voicedrive-device"
	ServerVersion   = "0.1.0"
)

// Request 是 JSON-RPC 2.0 请求；ID 用 RawMessage 原样回显（数字/字符串皆可），
// ID 缺失即为通知（不应答）。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 是 JSON-RPC 2.0 应答；成功只带 Result，失败只带 Error。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError 是 JSON-RPC error 对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
