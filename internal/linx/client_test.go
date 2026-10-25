package linx_test

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx/linxtest"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

func newClient(t *testing.T, url string, onMCP func([]byte) []byte) (*linx.Client, *linx.AudioBuffer) {
	t.Helper()
	buf := linx.NewAudioBuffer()
	c := linx.NewClient(linx.Config{
		URL:         url,
		BackoffBase: 5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		OutboxSize:  64,
		Token:       "tk-test",
		DeviceID:    "AA:BB:CC:DD:EE:FF",
		ClientID:    "cid-test",
		UserAgent:   "vd-test/0.1",
	}, buf, onMCP, logx.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
	return c, buf
}

// 零值配置回归：main.go 只填 URL 构造客户端，OutboxSize 零值须落到默认缓冲，
// 否则服务器不可达（写泵从未启动）时首批 Send* 永久阻塞调用方。
func TestClient_ZeroValueConfigOutboxBuffered(t *testing.T) {
	c := linx.NewClient(linx.Config{URL: "ws://127.0.0.1:1"}, linx.NewAudioBuffer(), nil, logx.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			c.SendAudio([]byte("f"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("零值配置下 SendAudio 阻塞：OutboxSize 默认缓冲未生效")
	}
}

// 握手必须带 linx-emu 对齐的 5 个头（对齐 cmd/linx-emu 参考实现）。
func TestClient_DialHeaders(t *testing.T) {
	srv := linxtest.New(t)
	_, _ = newClient(t, srv.URL(), nil)
	require.Eventually(t, func() bool { return len(srv.HandshakeHeaders()) >= 1 }, 2*time.Second, 2*time.Millisecond)
	h := srv.HandshakeHeaders()[0]
	require.Equal(t, "Bearer tk-test", h.Get("Authorization"))
	require.Equal(t, "1", h.Get("Protocol-Version"))
	require.Equal(t, "AA:BB:CC:DD:EE:FF", h.Get("Device-Id"))
	require.Equal(t, "cid-test", h.Get("Client-Id"))
	require.Equal(t, "vd-test/0.1", h.Get("User-Agent"))
}

func TestClient_Hello(t *testing.T) {
	srv := linxtest.New(t)
	_, _ = newClient(t, srv.URL(), nil)
	hello := srv.WaitForTexts(t, 1)[0]
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(hello), &got))
	require.Equal(t, map[string]any{
		"type":      "hello",
		"version":   float64(1),
		"features":  map[string]any{"mcp": true},
		"transport": "websocket",
		"audio_params": map[string]any{
			"format":               "pcm",
			"sample_rate":          float64(16000),
			"channels":             float64(1),
			"bit_depth":            float64(16),
			"endianness":           "little",
			"frame_duration":       float64(20),
			"frame_size":           float64(320),
			"sample_format":        "signed_int16",
			"play_buffer_duration": float64(300),
		},
	}, got)
}

// 平台回 hello(session_id) 后，客户端应自动发 listen start（manual 模式）。
func TestClient_ServerHelloSession(t *testing.T) {
	srv := linxtest.New(t)
	c, _ := newClient(t, srv.URL(), nil)
	texts := srv.WaitForTexts(t, 2) // 设备 hello + listen start
	var listenStart map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[1]), &listenStart))
	require.Equal(t, map[string]any{
		"type":       "listen",
		"state":      "start",
		"mode":       "manual",
		"session_id": "sid-1",
	}, listenStart)
	require.Equal(t, "sid-1", c.SessionID())
}

// SendDetect 把文本伪装成已识别语音上行（listen.detect），须带当前 session_id。
func TestClient_SendDetect(t *testing.T) {
	srv := linxtest.New(t)
	c, _ := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 2) // hello + listen start，此时已持有 session
	c.SendDetect("今天天气怎么样")
	texts := srv.WaitForTexts(t, 3)
	var detect map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[2]), &detect))
	require.Equal(t, map[string]any{
		"type":       "listen",
		"state":      "detect",
		"text":       "今天天气怎么样",
		"session_id": "sid-1",
	}, detect)
}

// SendListenStop 结束一次上行会话（对齐 linx-emu：音频发完补 listen stop），须带 session_id。
func TestClient_SendListenStop(t *testing.T) {
	srv := linxtest.New(t)
	c, _ := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 2) // hello + listen start
	c.SendListenStop()
	texts := srv.WaitForTexts(t, 3)
	var stop map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[2]), &stop))
	require.Equal(t, map[string]any{
		"type":       "listen",
		"state":      "stop",
		"session_id": "sid-1",
	}, stop)
}

// 平台下发 listen stop 时须回调 OnListenStop：上行采集据此自动停麦收口。
func TestClient_OnListenStop(t *testing.T) {
	srv := linxtest.New(t)
	fired := make(chan struct{}, 1)
	c := linx.NewClient(linx.Config{
		URL: srv.URL(), BackoffBase: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		OnListenStop: func() { fired <- struct{}{} },
	}, linx.NewAudioBuffer(), nil, logx.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
	srv.WaitForTexts(t, 2)

	c.Wake()
	require.NoError(t, srv.PushText(`{"type":"listen","state":"stop"}`))
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("平台 listen stop 未触发 OnListenStop")
	}
	require.Equal(t, linx.StateThinking, c.State())
}

func TestClient_MCPRoundtrip(t *testing.T) {
	srv := linxtest.New(t)
	_, _ = newClient(t, srv.URL(), func(payload []byte) []byte {
		var req map[string]any
		require.NoError(t, json.Unmarshal(payload, &req))
		return []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	})
	srv.WaitForTexts(t, 2) // hello + listen start
	require.NoError(t, srv.PushText(`{"type":"mcp","payload":{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}}`))
	texts := srv.WaitForTexts(t, 3)
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[2]), &resp))
	require.Equal(t, "mcp", resp["type"])
	payload := resp["payload"].(map[string]any)
	require.Equal(t, "2.0", payload["jsonrpc"])
	require.Equal(t, float64(1), payload["id"])
}

func TestClient_TTSBinaryIntoAudioBuffer(t *testing.T) {
	srv := linxtest.New(t)
	_, buf := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 1)
	require.NoError(t, srv.PushBinary([]byte("tts-frame")))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frame, err := buf.Pop(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("tts-frame"), frame)
}

func TestClient_LifecycleDrivesState(t *testing.T) {
	srv := linxtest.New(t)
	c, _ := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 1)

	c.Wake()
	require.Equal(t, linx.StateListening, c.State())
	require.NoError(t, srv.PushText(`{"type":"listen","state":"stop"}`))
	require.Eventually(t, func() bool { return c.State() == linx.StateThinking }, 2*time.Second, 2*time.Millisecond)
	require.NoError(t, srv.PushText(`{"type":"tts","state":"start"}`))
	require.Eventually(t, func() bool { return c.State() == linx.StateSpeaking }, 2*time.Second, 2*time.Millisecond)
	require.NoError(t, srv.PushText(`{"type":"tts","state":"stop"}`))
	require.Eventually(t, func() bool { return c.State() == linx.StateIdle }, 2*time.Second, 2*time.Millisecond)
}

func TestClient_ReconnectAndResendHello(t *testing.T) {
	srv := linxtest.New(t)
	_, _ = newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 2) // 首连 hello + listen start
	srv.KillConnections()
	texts := srv.WaitForTexts(t, 4) // 重连后重新 hello + listen start
	var hello map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[2]), &hello))
	require.Equal(t, "hello", hello["type"])
}

// 服务器主动断连后写泵必须随会话退出：否则每次重连泄漏一个 goroutine，
// 且旧泵与新泵争夺 outbox，可能吞掉重连后的首条上行消息。
func TestClient_WritePumpExitsOnServerClose(t *testing.T) {
	srv := linxtest.New(t)
	_, _ = newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 2)
	srv.KillConnections()
	srv.WaitForTexts(t, 4) // 第一次重连成功
	srv.KillConnections()
	srv.WaitForTexts(t, 6) // 第二次重连成功

	// 任一时刻只应存在当前会话的 1 个写泵；泄漏的旧泵同样停在该 select 上。
	require.Eventually(t, func() bool {
		return writePumpGoroutines() <= 1
	}, 2*time.Second, 5*time.Millisecond, "重连后写泵 goroutine 未随旧会话退出（泄漏）")
}

// writePumpGoroutines 统计处于 (*Client).writePump 帧的 goroutine 数；
// 匹配串须带 ")." 前缀，避免把本测试自身栈里的 writePump 字样计入。
func writePumpGoroutines() int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, ").writePump(") {
			n++
		}
	}
	return n
}

func TestClient_SendAudioAndText(t *testing.T) {
	srv := linxtest.New(t)
	c, _ := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 1)
	c.SendAudio([]byte("opus-1"))
	c.SendAudio([]byte("opus-2"))
	require.Equal(t, 2, srv.WaitForBinaries(t, 2))
	frames := srv.Frames()
	var binaries []string
	for _, f := range frames {
		if f.Binary {
			binaries = append(binaries, f.Text)
		}
	}
	require.Equal(t, []string{"opus-1", "opus-2"}, binaries)
}

func TestClient_UnrecognizedTextIgnored(t *testing.T) {
	srv := linxtest.New(t)
	c, _ := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 1)
	require.NoError(t, srv.PushText(`{"type":"whatever","data":"x"}`))
	require.NoError(t, srv.PushText(`not json at all`))
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, linx.StateIdle, c.State())
	// 连接仍可用
	c.SendAudio([]byte("still-alive"))
	require.Equal(t, 1, srv.WaitForBinaries(t, 1))
}

func TestClient_ConcurrentSendWhileReceiving(t *testing.T) {
	srv := linxtest.New(t)
	c, buf := newClient(t, srv.URL(), nil)
	srv.WaitForTexts(t, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			c.SendAudio([]byte{byte(i)})
			c.Wake()
		}
	}()
	for i := 0; i < 20; i++ {
		require.NoError(t, srv.PushBinary([]byte("tts")))
	}
	<-done
	require.Equal(t, 50, srv.WaitForBinaries(t, 50))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := buf.Pop(ctx)
	require.NoError(t, err)
}
