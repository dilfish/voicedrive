// Package linxtest 提供可编程的 fake 灵犀 WS server（02-linx §10）：
// httptest + gorilla，录制设备发来的全部帧、支持测试侧主动推送控制帧/
// 二进制帧与强制断连，供 internal/linx 与 cmd 集成测试共用。
package linxtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Frame 是录制到的一帧上行（设备 → 灵犀）。
type Frame struct {
	Binary bool
	Text   string
}

// Server 是 fake 灵犀 server；并发安全。
// 行为对齐真实平台：收到设备 hello 后回 {"type":"hello","session_id":"sid-N"}
// （N 按连接序递增），供客户端握手/会话测试断言。
type Server struct {
	srv     *httptest.Server
	mu      sync.Mutex
	conns   map[*websocket.Conn]struct{}
	frames  []Frame
	hsHdrs  []http.Header // 每条连接的握手请求头快照
	nextSid int
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// New 启动 fake server，t.Cleanup 自动关闭。
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{conns: map[*websocket.Conn]struct{}{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

// URL 返回 ws:// 端点。
func (s *Server) URL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http")
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conns[ws] = struct{}{}
	s.hsHdrs = append(s.hsHdrs, r.Header.Clone())
	s.nextSid++
	sid := fmt.Sprintf("sid-%d", s.nextSid)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, ws)
		s.mu.Unlock()
		ws.Close()
	}()
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.frames = append(s.frames, Frame{Binary: mt == websocket.BinaryMessage, Text: string(data)})
		s.mu.Unlock()
		if mt == websocket.TextMessage && isHello(data) {
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			reply, err := json.Marshal(map[string]any{"type": "hello", "session_id": sid})
			if err != nil {
				return
			}
			if err := ws.WriteMessage(websocket.TextMessage, reply); err != nil {
				return
			}
		}
	}
}

func isHello(data []byte) bool {
	var m struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &m) == nil && m.Type == "hello"
}

// HandshakeHeaders 返回每条连接握手请求头的快照（含重连），顺序与连接建立一致。
func (s *Server) HandshakeHeaders() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.hsHdrs...)
}

// Frames 返回已录制帧的快照。
func (s *Server) Frames() []Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Frame(nil), s.frames...)
}

// WaitForTexts 轮询等待录制满 n 条文本帧后返回（5s 超时 Fatal）。
func (s *Server) WaitForTexts(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var texts []string
		for _, f := range s.Frames() {
			if !f.Binary {
				texts = append(texts, f.Text)
			}
		}
		if len(texts) >= n {
			return texts
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("linxtest: 等待 %d 条文本帧超时，当前 %d", n, len(s.Frames()))
	return nil
}

// WaitForBinaries 轮询等待录制满 n 条二进制帧后返回其总数（5s 超时 Fatal）。
func (s *Server) WaitForBinaries(t *testing.T, n int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, f := range s.Frames() {
			if f.Binary {
				count++
			}
		}
		if count >= n {
			return count
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("linxtest: 等待 %d 条二进制帧超时", n)
	return 0
}

// PushText 向当前全部连接推送一条文本帧（控制帧/mcp 信封）。
func (s *Server) PushText(text string) error {
	return s.push(websocket.TextMessage, []byte(text))
}

// PushBinary 向当前全部连接推送一条二进制帧（TTS 音频）。
func (s *Server) PushBinary(b []byte) error {
	return s.push(websocket.BinaryMessage, b)
}

func (s *Server) push(mt int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ws := range s.conns {
		if err := ws.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		if err := ws.WriteMessage(mt, data); err != nil {
			return err
		}
	}
	return nil
}

// KillConnections 强制关闭当前全部连接，模拟服务端断连（驱动客户端重连）。
func (s *Server) KillConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ws := range s.conns {
		ws.Close()
	}
}
