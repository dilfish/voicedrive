// Package groupclient 是 voicedrive 群管理 HTTP API 客户端
// （01-voice-group §5 / 02-linx §6.4）：MCP 工具 handler 统一经本客户端转发。
//
// 身份只认 Header：X-VD-MAC 由客户端注入，方法全部为 typed 签名，
// 不存在可透传任意字段的入口——模型无法传参伪造身份（01 §5.1 硬规则）。
package groupclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

// NetworkErrorPhrase 是传输层失败（超时/连接/5xx/畸形响应）的固定话术，
// 供 MCP handler 直接送 TTS（02-linx §6.4）。
const NetworkErrorPhrase = "网络不太好，再说一遍试试"

// Result 是统一响应体：status ∈ ok/clarify/error，message ≤40 字直接送 TTS；
// clarify 时携带 ≤3 个候选选项（进入确认语境，02-linx §7）。
type Result struct {
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Options []string `json:"options,omitempty"`
}

// 状态取值。
const (
	StatusOK      = "ok"
	StatusClarify = "clarify"
	StatusError   = "error"
)

// Client 是 voicedrive HTTP API 客户端；零值不可用，经 New 构造。
type Client struct {
	base   string
	mac    string
	http   *http.Client
	logger logx.Logger
}

// New 构造客户端；timeout 为单请求超时（02-linx §6.4：3s，≤5s 端到端预算）。
func New(base, mac string, timeout time.Duration, logger logx.Logger) *Client {
	return &Client{
		base:   base,
		mac:    mac,
		http:   &http.Client{Timeout: timeout},
		logger: logger,
	}
}

// Create 建群（F1）：passcode 未口述传空串，由服务端生成。
func (c *Client) Create(name, passcode string) (Result, error) {
	return c.post("group.create", "/v1/group/create", struct {
		Name     string `json:"name"`
		Passcode string `json:"passcode,omitempty"`
	}{name, passcode})
}

// Join 口令入群（F2）。
func (c *Client) Join(passcode string) (Result, error) {
	return c.post("group.join", "/v1/group/join", struct {
		Passcode string `json:"passcode"`
	}{passcode})
}

// Switch 切焦点群（F4）。
func (c *Client) Switch(name string) (Result, error) {
	return c.post("group.switch", "/v1/group/switch", struct {
		Name string `json:"name"`
	}{name})
}

// Mute 静音/取消静音某群（F5）。
func (c *Client) Mute(name string, on bool) (Result, error) {
	return c.post("group.mute", "/v1/group/mute", struct {
		Name string `json:"name"`
		On   bool   `json:"on"`
	}{name, on})
}

// List 查我加入的群与当前焦点（F6）。
func (c *Client) List() (Result, error) {
	return c.do("group.list", http.MethodGet, "/v1/group/list", nil)
}

// Leave 退群（F10）。
func (c *Client) Leave(name string) (Result, error) {
	return c.post("group.leave", "/v1/group/leave", struct {
		Name string `json:"name"`
	}{name})
}

// Rename 改群名（F11）。
func (c *Client) Rename(name, newName string) (Result, error) {
	return c.post("group.rename", "/v1/group/rename", struct {
		Name    string `json:"name"`
		NewName string `json:"new_name"`
	}{name, newName})
}

// Pat 拍一拍某人（F8）：alias 为目标别名，非本机身份。
func (c *Client) Pat(alias string) (Result, error) {
	return c.post("group.pat", "/v1/group/pat", struct {
		Alias string `json:"alias"`
	}{alias})
}

// Invite 邀请好友进群（F9）。
func (c *Client) Invite(alias, groupName string) (Result, error) {
	return c.post("group.invite", "/v1/group/invite", struct {
		Alias     string `json:"alias"`
		GroupName string `json:"group_name"`
	}{alias, groupName})
}

// QueryMember 查某群里有没有某人（F13）。
func (c *Client) QueryMember(groupName, alias string) (Result, error) {
	return c.post("group.query_member", "/v1/group/query_member", struct {
		GroupName string `json:"group_name"`
		Alias     string `json:"alias"`
	}{groupName, alias})
}

func (c *Client) post(tool, path string, params any) (Result, error) {
	return c.do(tool, http.MethodPost, path, params)
}

// do 发送请求：typed params 序列化为确定序 JSON，其 sha256 作为幂等指纹
// header（与 01 §5.1 服务端 idem:{mac}:{tool}:{fingerprint} 30s SETNX 对齐）。
func (c *Client) do(tool, method, path string, params any) (Result, error) {
	var body []byte
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Result{}, fmt.Errorf("groupclient %s marshal: %w", tool, err)
		}
		body = b
	}
	sum := sha256.Sum256(body)
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("groupclient %s new request: %w", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-VD-MAC", c.mac)
	req.Header.Set("X-VD-Fingerprint", hex.EncodeToString(sum[:]))

	resp, err := c.http.Do(req)
	if err != nil {
		c.logger.Warnf("groupclient %s transport: %v", tool, err)
		return Result{}, fmt.Errorf("groupclient %s do: %w", tool, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.logger.Warnf("groupclient %s close body: %v", tool, cerr)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("groupclient %s read body: %w", tool, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("groupclient %s http %d: %s", tool, resp.StatusCode, string(raw))
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, fmt.Errorf("groupclient %s decode: %w", tool, err)
	}
	return result, nil
}
