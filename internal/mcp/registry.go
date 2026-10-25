package mcp

import (
	"fmt"
	"strings"
)

// Args 是 tools/call 的 arguments（已按 schema 做过类型校验）。
type Args map[string]any

// Property 是工具参数声明；Type 仅允许 "string"/"int"/"bool"
// （hardware-mcp 规范硬限制，Register 时校验）。
type Property struct {
	Name     string
	Type     string
	Required bool
}

// Tool 是一个 MCP 工具；Description 面向 LLM，须写清触发场景与参数含义；
// Handler 返回 ≤40 字话术，经 content text 直接送 TTS。
type Tool struct {
	Name        string
	Description string
	Properties  []Property
	Handler     func(args Args) string
}

// jsonSchemaType 是 Property.Type 到 JSON Schema type 的映射。
var jsonSchemaType = map[string]string{
	"string": "string",
	"int":    "integer",
	"bool":   "boolean",
}

// Registry 是工具注册表；工具数少，tools/list 单页返回（注册序）。
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register 注册工具：名称/描述/handler 非空、参数类型合法、不可重复。
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("mcp: empty name")
	}
	if strings.TrimSpace(t.Description) == "" {
		return fmt.Errorf("mcp: tool %s empty description", t.Name)
	}
	if t.Handler == nil {
		return fmt.Errorf("mcp: tool %s nil handler", t.Name)
	}
	for _, p := range t.Properties {
		if _, ok := jsonSchemaType[p.Type]; !ok {
			return fmt.Errorf("mcp: tool %s property %s: property type %q invalid (only string/int/bool)", t.Name, p.Name, p.Type)
		}
	}
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("mcp: duplicate tool %s", t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// Get 按名取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List 按注册序返回全部工具。
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// inputSchema 生成 tools/list 的 JSON Schema 对象：
// properties 仅 type（三种基础类型），required 列必填项。
func (t Tool) inputSchema() map[string]any {
	props := make(map[string]any, len(t.Properties))
	var required []string
	for _, p := range t.Properties {
		props[p.Name] = map[string]any{"type": jsonSchemaType[p.Type]}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// validateArgs 按 Properties 校验 arguments 中**已声明**参数的类型；
// 未声明的键原样透传（前向兼容），缺失的必填项不在此处拦截
// （业务校验归 handler 与服务端，LLM 可能省略参数）。
func (t Tool) validateArgs(args Args) error {
	for _, p := range t.Properties {
		v, ok := args[p.Name]
		if !ok || v == nil {
			continue
		}
		switch p.Type {
		case "string":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s.%s must be string", t.Name, p.Name)
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s.%s must be bool", t.Name, p.Name)
			}
		case "int":
			f, ok := v.(float64)
			if !ok || f != float64(int64(f)) {
				return fmt.Errorf("%s.%s must be int", t.Name, p.Name)
			}
		}
	}
	return nil
}
