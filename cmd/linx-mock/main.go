// linx-mock 在本地冒充灵矽平台：OTA 网关 + WebSocket 设备网关 + 端侧 MCP 客户端。
// 用于测试期间替代真实接口，不产生任何外网请求。
//
//	go run ./cmd/linx-mock                     # 默认 127.0.0.1:18090
//	go run ./cmd/linx-emu -ota http://127.0.0.1:18090/v1/ota/ -ws ws://127.0.0.1:18090/v1/ws/ -say "把音量调到30"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type device struct {
	code     string
	otaCount int
	token    string
}

type srv struct {
	mu        sync.Mutex
	devices   map[string]*device
	bindAfter int // 第几次 OTA 后自动绑定，默认 2（模拟"第一次拿码、第二次已绑"）
	public    string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18090", "监听地址")
	bindAfter := flag.Int("bind-after", 2, "第几次 OTA 后自动绑定；0 表示只接受 /_bind 手动绑定")
	flag.Parse()

	s := &srv{
		devices:   make(map[string]*device),
		bindAfter: *bindAfter,
		public:    "ws://" + *addr + "/v1/ws/",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ota/", s.handleOTA)
	mux.HandleFunc("/v1/ota/activate", s.handleActivate)
	mux.HandleFunc("/v1/ws/", s.handleWS)
	mux.HandleFunc("/_bind", s.handleBind)
	mux.HandleFunc("/_devices", s.handleDevices)

	log.Printf("mock 灵矽已启动于 http://%s", *addr)
	log.Printf("  OTA : http://%s/v1/ota/", *addr)
	log.Printf("  WS  : %s", s.public)
	log.Print("  提示: 用 -bind-after 控制第几次 OTA 自动绑定，或 POST /_bind {\"mac\":\"...\"}")
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// handleOTA 模拟真实行为：未绑定返回激活码，已绑定返回 ws 地址与 token。
func (s *srv) handleOTA(w http.ResponseWriter, r *http.Request) {
	mac := r.Header.Get("Device-Id")
	if mac == "" {
		http.Error(w, "missing Device-Id", http.StatusBadRequest)
		return
	}
	var body map[string]any
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	d := s.devices[mac]
	if d == nil {
		d = &device{code: newCode()}
		s.devices[mac] = d
	}
	d.otaCount++
	if s.bindAfter > 0 && d.otaCount >= s.bindAfter && d.token == "" {
		d.token = "mock-token-" + mac
		log.Printf("[ota] %s 自动绑定（第 %d 次上报）", mac, d.otaCount)
	}
	bound, token, code := d.token != "", d.token, d.code
	s.mu.Unlock()

	resp := map[string]any{
		"server_time": map[string]any{
			"timestamp": time.Now().UnixMilli(), "timezone": "Asia/Shanghai", "timezone_offset": 480,
		},
	}
	if bound {
		resp["websocket"] = map[string]any{"url": s.public, "token": token}
		log.Printf("[ota] %s 已绑定，下发 token", mac)
	} else {
		resp["activation"] = map[string]any{
			"code": code, "challenge": mac,
			"message": "http://" + strings.TrimPrefix(s.public, "ws://") + "\n" + code,
		}
		log.Printf("[ota] %s 未绑定，激活码 %s", mac, code)
	}
	writeJSON(w, resp)
}

func (s *srv) handleActivate(w http.ResponseWriter, r *http.Request) {
	mac := r.Header.Get("Device-Id")
	s.mu.Lock()
	d := s.devices[mac]
	s.mu.Unlock()
	if d == nil || d.token == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	fmt.Fprint(w, "success")
}

// handleBind 模拟"在控制台输入激活码"，供脚本直接调用。
func (s *srv) handleBind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAC string `json:"mac"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.MAC == "" {
		http.Error(w, "need {\"mac\":\"...\"}", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	d := s.devices[req.MAC]
	if d == nil {
		d = &device{code: newCode()}
		s.devices[req.MAC] = d
	}
	d.token = "mock-token-" + req.MAC
	s.mu.Unlock()
	log.Printf("[bind] %s 已手动绑定", req.MAC)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *srv) handleDevices(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make(map[string]any, len(s.devices))
	for mac, d := range s.devices {
		out[mac] = map[string]any{"bound": d.token != "", "ota_count": d.otaCount, "code": d.code}
	}
	s.mu.Unlock()
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newCode() string {
	return strconv.Itoa(100000 + int(time.Now().UnixNano()%900000))
}

// ---- WebSocket 网关 ----

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *srv) handleWS(w http.ResponseWriter, r *http.Request) {
	mac := r.Header.Get("Device-Id")
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	d := s.devices[mac]
	s.mu.Unlock()
	if d == nil || d.token == "" || auth != d.token {
		log.Printf("[ws] %s 鉴权失败 (token=%q)", mac, auth)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] 升级失败: %v", err)
		return
	}
	log.Printf("[ws] %s 已连接", mac)
	ses := &session{conn: conn, mac: mac, done: make(chan struct{})}
	ses.run()
	log.Printf("[ws] %s 已断开", mac)
}

type session struct {
	conn    *websocket.Conn
	mac     string
	wmu     sync.Mutex
	nextID  atomic.Int64
	aborted atomic.Bool
	done    chan struct{}

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResult
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

func (s *session) run() {
	defer s.conn.Close()
	s.pending = make(map[int64]chan rpcResult)

	for {
		mt, data, err := s.conn.ReadMessage()
		if err != nil {
			close(s.done)
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var msg struct {
			Type      string          `json:"type"`
			State     string          `json:"state"`
			Text      string          `json:"text"`
			SessionID string          `json:"session_id"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}

		switch msg.Type {
		case "hello":
			s.onHello(msg.SessionID)
		case "listen":
			if msg.State == "detect" && msg.Text != "" {
				go s.turn(msg.Text)
			}
		case "abort":
			s.aborted.Store(true)
			log.Printf("[turn] 收到 abort，停止播报")
			s.send(map[string]any{"type": "tts", "state": "stop", "is_aborted": true})
		case "mcp":
			s.onMCPResponse(msg.Payload)
		}
	}
}

func (s *session) onHello(sid string) {
	log.Print("[ws] hello 握手，回 hello + initialize + tools/list")
	s.send(map[string]any{
		"type": "hello", "version": 1, "transport": "websocket", "session_id": "mock-" + s.mac,
		"audio_params": map[string]any{
			"format": "pcm", "sample_rate": 16000, "channels": 1,
			"bits_per_sample": 16, "frame_duration": 20, "play_buffer_duration": 300,
		},
	})
	s.rpc(map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "MockClient", "version": "1.0.0"},
		"capabilities":    map[string]any{"sampling": map[string]any{}},
	}, "initialize", 0)
	s.rpc(nil, "tools/list", 0)
}

func (s *session) onMCPResponse(raw json.RawMessage) {
	var resp struct {
		ID     int64                     `json:"id"`
		Result json.RawMessage           `json:"result"`
		Error  *struct{ Message string } `json:"error"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return
	}
	s.pendingMu.Lock()
	ch := s.pending[resp.ID]
	delete(s.pending, resp.ID)
	s.pendingMu.Unlock()
	if ch == nil {
		return
	}
	if resp.Error != nil {
		ch <- rpcResult{err: fmt.Errorf("%s", resp.Error.Message)}
		return
	}
	ch <- rpcResult{result: resp.Result}
}

// callTool 发起一次 tools/call 并等待设备回执。
func (s *session) callTool(name string, args map[string]any) (string, error) {
	id := s.nextID.Add(1)
	ch := make(chan rpcResult, 1)
	s.pendingMu.Lock()
	s.pending[id] = ch
	s.pendingMu.Unlock()

	s.rpc(map[string]any{"name": name, "arguments": args}, "tools/call", id)

	select {
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		var out struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		_ = json.Unmarshal(r.result, &out)
		if len(out.Content) == 0 {
			return "", fmt.Errorf("空 content")
		}
		return out.Content[0].Text, nil
	case <-time.After(3 * time.Second):
		return "", fmt.Errorf("设备 3s 未回执")
	case <-s.done:
		return "", fmt.Errorf("连接已断开")
	}
}

// turn 模拟一轮完整对话：stt → (tools/call) → tts。
func (s *session) turn(text string) {
	s.aborted.Store(false)
	log.Printf("[turn] 用户说: %s", text)
	s.send(map[string]any{"type": "stt", "text": text, "session_id": "mock-" + s.mac})

	reply := "收到：" + text
	if name, args := planTool(text); name != "" {
		if got, err := s.callTool(name, args); err == nil {
			reply = got
			log.Printf("[turn] %s 回执: %s", name, got)
		} else {
			reply = "调用失败：" + err.Error()
			log.Printf("[turn] %s 失败: %v", name, err)
		}
	}

	s.send(map[string]any{"type": "tts", "state": "start", "session_id": "mock-" + s.mac})
	s.send(map[string]any{"type": "tts", "state": "sentence_start", "text": reply})
	s.send(map[string]any{"type": "llm", "text": "🙂", "emotion": "happy", "action": "thinking"})

	// 播 2 秒 PCM 静音（640B = 20ms），期间可被 abort 打断。
	frame := make([]byte, 640)
	for i := 0; i < 100; i++ {
		if s.aborted.Load() {
			return
		}
		s.sendBinary(frame)
		time.Sleep(20 * time.Millisecond)
	}
	if !s.aborted.Load() {
		s.send(map[string]any{"type": "tts", "state": "stop", "is_aborted": false})
	}
}

// planTool 用最笨的规则把话映射到工具，足够驱动测试。
func planTool(text string) (string, map[string]any) {
	if strings.Contains(text, "音量") {
		v := 50
		for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r < '0' || r > '9' }) {
			if n, err := strconv.Atoi(f); err == nil {
				v = n
				break
			}
		}
		return "self.audio.volume", map[string]any{"volume": v}
	}
	if strings.Contains(text, "状态") || strings.Contains(text, "电量") {
		return "self.device.status", map[string]any{}
	}
	return "", nil
}

func (s *session) rpc(params any, method string, id int64) {
	if id == 0 {
		id = s.nextID.Add(1)
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	s.send(map[string]any{"type": "mcp", "payload": payload})
}

func (s *session) send(v any) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = s.conn.WriteJSON(v)
}

func (s *session) sendBinary(b []byte) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = s.conn.WriteMessage(websocket.BinaryMessage, b)
}
