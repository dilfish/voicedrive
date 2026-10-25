// Package linx 实现设备侧与灵犀平台的 WebSocket 长连接客户端（02-linx）：
// 协议信封解析、会话状态机驱动、TTS 音频缓冲与 MCP 信封转发。
package linx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

// Config 是 WS 客户端配置；零值字段取 NewClient 内默认值。
type Config struct {
	URL          string        // 灵犀 WS 端点
	BackoffBase  time.Duration // 重连退避起始值（默认 1s，测试注入小值）
	BackoffMax   time.Duration // 重连退避上限（默认 30s）
	OutboxSize   int           // 下行发送队列容量（默认 64）
	Token        string        // OTA 换来的 WS Bearer token（空则不带 Authorization 头）
	DeviceID     string        // 设备 MAC，握手 Device-Id 头
	ClientID     string        // 设备侧生成的 UUID，握手 Client-Id 头
	UserAgent    string        // 握手 User-Agent 头
	OnListenStop func()        // 平台下发 listen stop 时回调（状态机迁移后）；上行采集据此停麦收口，可为 nil
}

func (c Config) withDefaults() Config {
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.OutboxSize <= 0 {
		c.OutboxSize = 64
	}
	return c
}

// outbound 是一条待发送的 WS 帧：文本控制帧或二进制音频帧。
type outbound struct {
	binary bool
	data   []byte
}

// helloBody 是连接建立后同步发出的首帧（PCM 参数对齐 linx-emu 参考实现）。
type helloBody struct {
	Type     string `json:"type"`
	Version  int    `json:"version"`
	Features struct {
		MCP bool `json:"mcp"`
	} `json:"features"`
	Transport   string      `json:"transport"`
	AudioParams audioParams `json:"audio_params"`
}

// audioParams 声明上行裸 PCM 格式：16k/单声道/16bit 小端，20ms 一帧 320 采样。
type audioParams struct {
	Format             string `json:"format"`
	SampleRate         int    `json:"sample_rate"`
	Channels           int    `json:"channels"`
	BitDepth           int    `json:"bit_depth"`
	Endianness         string `json:"endianness"`
	FrameDuration      int    `json:"frame_duration"`
	FrameSize          int    `json:"frame_size"`
	SampleFormat       string `json:"sample_format"`
	PlayBufferDuration int    `json:"play_buffer_duration"`
}

func newHello() []byte {
	h := helloBody{
		Type:      "hello",
		Version:   1,
		Transport: "websocket",
		AudioParams: audioParams{
			Format: "pcm", SampleRate: 16000, Channels: 1,
			BitDepth: 16, Endianness: "little",
			FrameDuration: 20, FrameSize: 320,
			SampleFormat: "signed_int16", PlayBufferDuration: 300,
		},
	}
	h.Features.MCP = true
	b, err := json.Marshal(h)
	if err != nil {
		// 静态结构 marshal 不可能失败；保底返回最小合法 hello。
		return []byte(`{"type":"hello","version":1,"features":{"mcp":true},"transport":"websocket"}`)
	}
	return b
}

// listenFrame 是 listen 控制帧：start 携带 mode，detect 携带 text，session_id 必带。
type listenFrame struct {
	Type      string `json:"type"`
	State     string `json:"state"`
	Mode      string `json:"mode,omitempty"`
	Text      string `json:"text,omitempty"`
	SessionID string `json:"session_id"`
}

// mcpEnvelope 是 mcp 信封的下行包装：平台发来的 payload 原样回传响应。
type mcpEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// upstream 是平台下行的文本控制帧信封（type/state/session_id 字段名 §11 联调后固化）。
type upstream struct {
	Type      string `json:"type"`
	State     string `json:"state"`
	SessionID string `json:"session_id"`
	// Payload 保留原始 mcp payload，避免二次解析。
	Payload json.RawMessage `json:"payload"`
}

// Client 是灵犀 WS 客户端。Run 阻塞驱动收发与重连；
// SendAudio/SendText/Wake/State 可从任意 goroutine 并发调用。
type Client struct {
	cfg    Config
	buf    *AudioBuffer
	onMCP  func(payload []byte) []byte
	logger logx.Logger

	// outbox 跨重连持久：未连接期间入队，重连后由写泵按序发出。
	outbox chan outbound

	// machineMu 保护 SessionMachine：事件来自 WS 读循环与本地 Wake() 两个 goroutine。
	machineMu sync.Mutex
	machine   *SessionMachine

	// sessMu 保护 sessionID：写在读循环（平台 hello），读在 SendDetect/SessionID 调用方。
	sessMu    sync.Mutex
	sessionID string
}

// NewClient 构造客户端；onMCP 为 nil 时收到 mcp 信封仅告警不回复。
func NewClient(cfg Config, buf *AudioBuffer, onMCP func(payload []byte) []byte, logger logx.Logger) *Client {
	// 先落默认值再建 outbox：零值 OutboxSize 若用于 make 会得到无缓冲通道，
	// 服务器不可达时首批 Send* 将永久阻塞调用方（main.go 只填 URL）。
	cfg = cfg.withDefaults()
	return &Client{
		cfg:     cfg,
		buf:     buf,
		onMCP:   onMCP,
		logger:  logger,
		outbox:  make(chan outbound, cfg.OutboxSize),
		machine: NewSessionMachine(),
	}
}

// Run 阻塞运行直至 ctx 取消（返回 nil）。断连后按指数退避重连并重发 hello。
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.BackoffBase
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := c.session(ctx)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		c.logger.Warnf("linx: 连接断开 %v，%v 后重连", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > c.cfg.BackoffMax {
			backoff = c.cfg.BackoffMax
		}
	}
}

// session 建立一次连接并驱动到断开：dial → 同步写 hello → 写泵 + 读循环。
func (c *Client) session(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.cfg.URL, c.dialHeaders())
	if err != nil {
		return fmt.Errorf("linx: dial: %w", err)
	}
	defer conn.Close()

	// hello 必须先于写泵启动同步写出，保证每条连接的首帧都是 hello。
	if err := writeWithDeadline(conn, websocket.TextMessage, newHello()); err != nil {
		return fmt.Errorf("linx: send hello: %w", err)
	}

	pumpDone := make(chan struct{})
	pumpStop := make(chan struct{})
	go c.writePump(ctx, conn, pumpStop, pumpDone)

	sessDone := make(chan struct{})
	go func() {
		// 读循环无 ctx 参数（阻塞 ReadMessage），靠关连接解除阻塞。
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-sessDone:
		}
	}()

	readErr := c.readLoop(conn)
	close(sessDone)

	// 关停写泵：先关 stop 唤醒空转 select，再关连接解除阻塞中的写；
	// 写泵不随会话退出会泄漏 goroutine，且旧泵与新泵争夺 outbox 吞消息。
	close(pumpStop)
	_ = conn.Close()
	select {
	case <-pumpDone:
	case <-time.After(time.Second):
		c.logger.Warnf("linx: 写泵未随连接关闭退出")
	}
	return readErr
}

func (c *Client) writePump(ctx context.Context, conn *websocket.Conn, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case out, ok := <-c.outbox:
			if !ok {
				return
			}
			mt := websocket.TextMessage
			if out.binary {
				mt = websocket.BinaryMessage
			}
			if err := writeWithDeadline(conn, mt, out.data); err != nil {
				c.logger.Warnf("linx: 发送失败 %v", err)
				return
			}
		}
	}
}

func (c *Client) readLoop(conn *websocket.Conn) error {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("linx: read: %w", err)
		}
		switch mt {
		case websocket.BinaryMessage:
			c.buf.Push(data)
		case websocket.TextMessage:
			c.handleText(data)
		}
	}
}

// handleText 路由平台下行控制帧；无法识别的帧仅记 debug，不视为错误。
func (c *Client) handleText(data []byte) {
	var msg upstream
	if err := json.Unmarshal(data, &msg); err != nil {
		c.logger.Debugf("linx: 忽略非 JSON 文本帧: %s", data)
		return
	}
	switch msg.Type {
	case "hello":
		// 平台确认握手并分配 session_id：记住它，随后自动开启 manual 监听，
		// 与 linx-emu 参考实现一致（后续 listen.detect / abort 都要带该 id）。
		c.sessMu.Lock()
		c.sessionID = msg.SessionID
		c.sessMu.Unlock()
		if b, err := json.Marshal(listenFrame{
			Type: "listen", State: "start", Mode: "manual", SessionID: msg.SessionID,
		}); err == nil {
			c.SendText(b)
		} else {
			c.logger.Errorf("linx: 序列化 listen start 失败: %v", err)
		}
	case "mcp":
		if c.onMCP == nil {
			c.logger.Warnf("linx: 收到 mcp 信封但未配置处理器")
			return
		}
		resp := c.onMCP(msg.Payload)
		if resp == nil { // 通知类，无需回复
			return
		}
		if b, err := json.Marshal(mcpEnvelope{Type: "mcp", Payload: resp}); err == nil {
			c.SendText(b)
		} else {
			c.logger.Errorf("linx: 序列化 mcp 响应失败: %v", err)
		}
	case "listen":
		if msg.State == "stop" {
			c.applyEvent(EventASRStop)
			if c.cfg.OnListenStop != nil {
				c.cfg.OnListenStop()
			}
		}
	case "tts":
		switch msg.State {
		case "start":
			c.applyEvent(EventTTSStart)
		case "stop":
			c.applyEvent(EventTTSStop)
		}
	default:
		c.logger.Debugf("linx: 忽略未识别帧 type=%q", msg.Type)
	}
}

func (c *Client) applyEvent(ev Event) {
	c.machineMu.Lock()
	err := c.machine.Transition(ev)
	c.machineMu.Unlock()
	if err != nil {
		c.logger.Warnf("linx: %v", err)
	}
}

// SendAudio 将一帧 opus 音频入队，由写泵按序上行。
func (c *Client) SendAudio(frame []byte) {
	c.outbox <- outbound{binary: true, data: frame}
}

// SendText 将一条文本控制帧入队，由写泵按序上行。
func (c *Client) SendText(b []byte) {
	c.outbox <- outbound{data: b}
}

// SendDetect 把一段文本伪装成已识别语音上行（listen.detect，等价"用户说了这句话"）。
// 尚未收到平台 hello（无 session_id）时丢弃并告警：detect 必须归属某个会话。
func (c *Client) SendDetect(text string) {
	c.sendListen(listenFrame{Type: "listen", State: "detect", Text: text})
}

// SendListenStop 结束一次音频上行会话（listen stop，对齐 linx-emu：音频发完补此帧）。
func (c *Client) SendListenStop() {
	c.sendListen(listenFrame{Type: "listen", State: "stop"})
}

// sendListen 统一发送 listen 帧：无 session_id 时丢弃并告警（帧必须归属某个会话）。
func (c *Client) sendListen(f listenFrame) {
	c.sessMu.Lock()
	sid := c.sessionID
	c.sessMu.Unlock()
	if sid == "" {
		c.logger.Warnf("linx: 无 session_id，丢弃 listen %s 帧", f.State)
		return
	}
	f.SessionID = sid
	b, err := json.Marshal(f)
	if err != nil {
		c.logger.Errorf("linx: 序列化 listen %s 失败: %v", f.State, err)
		return
	}
	c.SendText(b)
}

// SessionID 返回平台最近一次 hello 分配的会话 id（并发安全，供 UI/上层读取）。
func (c *Client) SessionID() string {
	c.sessMu.Lock()
	defer c.sessMu.Unlock()
	return c.sessionID
}

// Wake 注入本地唤醒事件（listening 期重复命中保持 listening）。
func (c *Client) Wake() {
	c.applyEvent(EventWake)
}

// State 返回当前会话状态（并发安全，供 UI/采集模块读取）。
func (c *Client) State() SessionState {
	c.machineMu.Lock()
	defer c.machineMu.Unlock()
	return c.machine.State()
}

func writeWithDeadline(conn *websocket.Conn, mt int, data []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return conn.WriteMessage(mt, data)
}

// dialHeaders 构造握手头，对齐 linx-emu 参考实现的 5 个必带字段。
func (c *Client) dialHeaders() http.Header {
	hdr := http.Header{}
	if c.cfg.Token != "" {
		hdr.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	hdr.Set("Protocol-Version", "1")
	if c.cfg.DeviceID != "" {
		hdr.Set("Device-Id", c.cfg.DeviceID)
	}
	if c.cfg.ClientID != "" {
		hdr.Set("Client-Id", c.cfg.ClientID)
	}
	if c.cfg.UserAgent != "" {
		hdr.Set("User-Agent", c.cfg.UserAgent)
	}
	return hdr
}
