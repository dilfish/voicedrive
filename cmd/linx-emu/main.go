// linx-emu 冒充一台 ESP32-S3，只做一件事：把灵矽的设备接口跑通。
//
//  1. OTA 上报：未绑定返回 6 位激活码
//  2. 控制台绑定后重跑：拿到 ws 地址与 token
//  3. WS 握手 + hello(features.mcp) + 应答 initialize / tools/list
//
// 用法:
//
//	go run ./cmd/linx-emu -mac D4:06:06:B6:A9:FA
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defOTA = "https://xrobo.qiniuapi.com/v1/ota/"
	defWS  = "wss://xrobo-io.qiniuapi.com/v1/ws/"
	ua     = "voicedrive-emu/0.1.0"
)

func main() {
	mac := flag.String("mac", "D4:06:06:B6:A9:FA", "设备 MAC，绑定以它为准")
	token := flag.String("token", os.Getenv("LINX_TOKEN"), "WS token，留空自动走 OTA")
	wsURL := flag.String("ws", defWS, "WS 地址")
	otaURL := flag.String("ota", defOTA, "OTA 地址")
	listen := flag.Bool("listen", true, "连上后发 listen start")
	say := flag.String("say", "", "连上后用 listen.detect 注入一句话，免麦克风验证 tools/call")
	abortAfter := flag.Duration("abort", 0, "TTS 开始后多久发 abort 打断（如 1500ms）")
	flag.Parse()

	clientID := uuid()

	// 1. OTA：未绑定拿激活码，已绑定拿 ws + token。
	if *token == "" {
		r := ota(*otaURL, *mac, clientID)
		if r.Activation != nil {
			fmt.Printf("未绑定，激活码: %s\n%s\n去控制台「添加设备」输入后重跑。\n",
				r.Activation.Code, r.Activation.Message)
			return
		}
		if r.WebSocket == nil || r.WebSocket.Token == "" {
			log.Fatal("OTA 没有返回 websocket.token，设备可能仍未绑定")
		}
		*token = r.WebSocket.Token
		if r.WebSocket.URL != "" {
			*wsURL = r.WebSocket.URL
		}
		log.Printf("OTA ok, ws=%s", *wsURL)
	}

	// 2. WS 握手，四个头与文档一致。
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+*token)
	hdr.Set("Protocol-Version", "1")
	hdr.Set("Device-Id", *mac)
	hdr.Set("Client-Id", clientID)
	hdr.Set("User-Agent", ua)

	conn, resp, err := websocket.DefaultDialer.Dial(*wsURL, hdr)
	if err != nil {
		if resp != nil {
			log.Fatalf("握手失败 HTTP %d: %v", resp.StatusCode, err)
		}
		log.Fatalf("握手失败: %v", err)
	}
	defer conn.Close()
	log.Printf("已连接 %s", *wsURL)

	// 3. hello：features.mcp=true 是这次要验的核心。
	_ = conn.WriteJSON(map[string]any{
		"type": "hello", "version": 1, "transport": "websocket",
		"features": map[string]any{"mcp": true},
		"audio_params": map[string]any{
			"format": "pcm", "sample_rate": 16000, "channels": 1,
			"bit_depth": 16, "endianness": "little",
			"frame_duration": 20, "frame_size": 320,
			"sample_format": "signed_int16", "play_buffer_duration": 300,
		},
	})
	log.Print(">> hello")

	// 4. 读循环：打印一切，MCP 请求就地应答。
	frames := 0
	resumed := false
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("连接结束: %v", err)
			return
		}
		if mt == websocket.BinaryMessage {
			if frames++; frames == 1 || frames%50 == 0 {
				log.Printf("<< 音频帧 #%d (%d bytes)", frames, len(data))
			}
			continue
		}
		log.Printf("<< %s", redact(data))

		var msg struct {
			Type      string          `json:"type"`
			SessionID string          `json:"session_id"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "tts":
			var t struct {
				State string `json:"state"`
			}
			_ = json.Unmarshal(data, &t)
			if t.State == "start" && *abortAfter > 0 {
				go func(sid string) {
					time.Sleep(*abortAfter)
					_ = conn.WriteJSON(map[string]any{
						"type": "abort", "reason": "user_interrupt", "session_id": sid,
					})
					log.Print(">> abort (user_interrupt)")
				}(msg.SessionID)
			}
			// 打断被确认后，再注入一次同一句话，验证同一 session 是否还能继续。
			if t.State == "stop" && *abortAfter > 0 && *say != "" && !resumed {
				resumed = true
				var s struct {
					IsAborted bool `json:"is_aborted"`
				}
				_ = json.Unmarshal(data, &s)
				if s.IsAborted {
					log.Print("--- 打断已确认，2s 后在原 session 再问一次 ---")
					time.Sleep(2 * time.Second)
					_ = conn.WriteJSON(map[string]any{
						"type": "listen", "state": "detect",
						"text": *say, "session_id": msg.SessionID,
					})
					log.Printf(">> listen detect %q (第二轮)", *say)
				}
			}

		case "hello":
			if *listen {
				_ = conn.WriteJSON(map[string]any{
					"type": "listen", "state": "start", "mode": "manual",
					"session_id": msg.SessionID,
				})
				log.Print(">> listen start")
			}
			if *say != "" {
				// listen.detect 带 text，等价于"用户说了这句话"，无需真麦克风。
				time.Sleep(1500 * time.Millisecond)
				_ = conn.WriteJSON(map[string]any{
					"type": "listen", "state": "detect",
					"text": *say, "session_id": msg.SessionID,
				})
				log.Printf(">> listen detect %q", *say)
			}
		case "mcp":
			reply(conn, msg.Payload)
		}
	}
}

// reply 应答一条 JSON-RPC。只实现 initialize / tools/list / tools/call。
func reply(conn *websocket.Conn, raw json.RawMessage) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return
	}

	switch req.Method {
	case "initialize":
		log.Print(">> mcp initialize")
		writeResult(conn, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "voicedrive-emu", "version": "0.1.0"},
		})

	case "notifications/initialized", "initialized":
		// notification，无需响应

	case "tools/list":
		log.Printf(">> mcp tools/list (%d 个工具)", len(tools()))
		writeResult(conn, req.ID, map[string]any{"tools": tools(), "nextCursor": nil})

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		text, isErr := runTool(p.Name, p.Arguments)
		log.Printf(">> mcp tools/call %s -> %s", p.Name, text)
		writeResult(conn, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		})

	default:
		if len(req.ID) == 0 || string(req.ID) == "null" {
			return
		}
		log.Printf(">> mcp %s (未实现)", req.Method)
		_ = conn.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "未知方法: " + req.Method},
		}})
	}
}

func writeResult(conn *websocket.Conn, id json.RawMessage, result any) {
	_ = conn.WriteJSON(map[string]any{"type": "mcp", "payload": map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	}})
}

// runTool 是工具实现。返回文本和是否为错误。
func runTool(name string, args map[string]any) (string, bool) {
	switch name {
	case "self.device.status":
		return "电量 82%，4G 信号良好，当前群：夜班组。", false
	case "self.audio.volume":
		v, ok := args["volume"].(float64)
		if !ok {
			return "缺少参数 volume", true
		}
		return fmt.Sprintf("音量已设置为 %v", v), false
	}
	return "未知工具: " + name, true
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name": "self.device.status", "description": "获取设备状态", "type": 0,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		},
		{
			"name": "self.audio.volume", "description": "设置音量 0-100", "type": 0,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"volume": map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
				},
				"required": []string{"volume"},
			},
		},
	}
}

type otaResp struct {
	Error      string                          `json:"error"`
	Activation *struct{ Code, Message string } `json:"activation"`
	WebSocket  *struct{ URL, Token string }    `json:"websocket"`
}

// ota 上报设备信息，只带文档要求的必填字段。
func ota(url, mac, clientID string) *otaResp {
	body := map[string]any{
		"version": 0,
		"uuid":    clientID,
		"application": map[string]any{
			"name": "voicedrive-emu", "version": "0.1.0",
			"compile_time": time.Now().Format("2006-01-02 15:04:05"),
			"elf_sha256":   fmt.Sprintf("%064d", 0),
		},
		"board":           map[string]any{"type": "voicedrive-emu", "mac": mac},
		"mac_address":     mac,
		"chip_model_name": "esp32s3",
	}
	raw, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Activation-Version", "1")
	req.Header.Set("Device-Id", mac)
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "zh-CN")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		log.Fatalf("OTA 请求失败: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("OTA HTTP %d: %s", resp.StatusCode, data)
	}

	var out otaResp
	if json.Unmarshal(data, &out) != nil {
		log.Fatalf("OTA 响应无法解析: %s", data)
	}
	if out.Error != "" {
		log.Fatalf("OTA 业务错误: %s", out.Error)
	}
	return &out
}

// redact 把报文里的 token 一类字段遮掉，避免日志泄露凭证。
func redact(data []byte) string {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return string(data)
	}
	scrub(v)
	out, err := json.Marshal(v)
	if err != nil {
		return string(data)
	}
	return string(out)
}

func scrub(v any) {
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if lk := strings.ToLower(k); strings.Contains(lk, "token") || strings.Contains(lk, "secret") {
				node[k] = "«redacted»"
				continue
			}
			scrub(val)
		}
	case []any:
		for _, item := range node {
			scrub(item)
		}
	}
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
