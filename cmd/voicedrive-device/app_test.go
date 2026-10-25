package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/confirm"
	"github.com/Study-of-slag-weng/voicedrive1/internal/groupclient"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx/linxtest"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/mcp"
)

// syncBuf 是并发安全的输出缓冲：onExpire 定时器 goroutine 与输入循环都会写 out。
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// vdCall 是 fake voicedrive 收到的一次请求快照。
type vdCall struct {
	Method string
	Path   string
	Body   string
	MAC    string
}

// fakeVD 是 fake voicedrive HTTP server：录制全部请求，next 决定每次的统一响应。
type fakeVD struct {
	mu    sync.Mutex
	calls []vdCall
	next  func(c vdCall) groupclient.Result
}

func (f *fakeVD) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c := vdCall{Method: r.Method, Path: r.URL.Path, Body: string(body), MAC: r.Header.Get("X-VD-MAC")}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	next := f.next
	f.mu.Unlock()
	resp := next(c)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *fakeVD) snapshot() []vdCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []vdCall{}
	return append(out, f.calls...)
}

// fakeSink 记录送入指令会话的文本（CommandSink 替身）。
type fakeSink struct {
	mu   sync.Mutex
	cmds []string
}

func (f *fakeSink) SendCommand(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, text)
}

func (f *fakeSink) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	return append(out, f.cmds...)
}

// newHarness 装配全链路：fake 灵犀 WS + fake voicedrive + 真实 linx 客户端与 App。
func newHarness(t *testing.T, next func(c vdCall) groupclient.Result) (*App, *linxtest.Server, *fakeVD, *syncBuf) {
	t.Helper()
	lxs := linxtest.New(t)
	vd := &fakeVD{next: next}
	hs := httptest.NewServer(vd)
	t.Cleanup(hs.Close)

	logger := logx.NewNop()
	gc := groupclient.New(hs.URL, "AA:BB:CC:DD:EE:FF", time.Second, logger)
	out := &syncBuf{}

	var app *App
	cf := confirm.New(time.Now, func(confirm.Expired) {
		app.say(ExpiredPhrase)
	})
	reg := mcp.NewRegistry()
	require.NoError(t, registerGroupTools(reg, gc, cf, nil, logger))
	disp := mcp.NewDispatcherWithRegistry(reg)
	buf := linx.NewAudioBuffer()
	client := linx.NewClient(linx.Config{
		URL:         lxs.URL(),
		BackoffBase: 5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		OutboxSize:  64,
	}, buf, mcpHandler(disp), logger)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = client.Run(ctx) }()

	app = NewApp("小牛", cf, reg, &LinxCommandSink{Client: client}, out, logger)
	lxs.WaitForTexts(t, 2) // hello + listen start：平台已回 session_id，detect 才不会丢
	return app, lxs, vd, out
}

// pushToolsCall 以 fake 平台身份下发一条 tools/call 并等待设备应答。
func pushToolsCall(t *testing.T, lxs *linxtest.Server, id int, name, arguments string) map[string]any {
	t.Helper()
	frame := `{"type":"mcp","payload":{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + arguments + `}}}`
	require.NoError(t, lxs.PushText(frame))
	var resp map[string]any
	require.Eventually(t, func() bool {
		for _, tx := range lxs.WaitForTexts(t, 1) {
			var m map[string]any
			if err := json.Unmarshal([]byte(tx), &m); err != nil {
				continue
			}
			if m["type"] != "mcp" {
				continue
			}
			p, ok := m["payload"].(map[string]any)
			if !ok || p["id"] != float64(id) {
				continue
			}
			resp = p
			return true
		}
		return false
	}, 2*time.Second, 2*time.Millisecond)
	return resp
}

func textOf(t *testing.T, resp map[string]any) string {
	t.Helper()
	result := resp["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	return content["text"].(string)
}

// --- parseFlags ---

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		envTok  string
		args    []string
		want    config
		wantErr string
	}{
		{
			name:   "默认值",
			envTok: "",
			args:   []string{"--mac", "AA:BB"},
			want: config{MAC: "AA:BB", WakeWord: "小牛",
				LinxURL: "ws://xrobo-io.qiniuapi.com/v1/ws/",
				OTAURL:  "https://xrobo.qiniuapi.com/v1/ota/"},
		},
		{
			name:    "缺 mac 必填",
			envTok:  "",
			args:    []string{"--server", "http://x"},
			wantErr: "--mac",
		},
		{
			name:   "token 继承环境变量",
			envTok: "env-tk",
			args:   []string{"--mac", "AA:BB"},
			want: config{MAC: "AA:BB", WakeWord: "小牛", Token: "env-tk",
				LinxURL: "ws://xrobo-io.qiniuapi.com/v1/ws/",
				OTAURL:  "https://xrobo.qiniuapi.com/v1/ota/"},
		},
		{
			name: "全量参数",
			args: []string{"--mac", "AA:BB", "--alias", "床头机", "--wake-word", "小七",
				"--server", "http://vd", "--lingx-url", "ws://lx", "--ota", "https://ota/x",
				"--token", "tk-1", "--inject", "a.wav"},
			want: config{MAC: "AA:BB", Alias: "床头机", WakeWord: "小七", Server: "http://vd",
				LinxURL: "ws://lx", OTAURL: "https://ota/x", Token: "tk-1", Inject: "a.wav"},
		},
		{
			name: "mic 持续采集（默认 0）",
			args: []string{"--mac", "AA:BB", "--mic", "0"},
			want: config{MAC: "AA:BB", WakeWord: "小牛", Mic: "0",
				LinxURL: "ws://xrobo-io.qiniuapi.com/v1/ws/",
				OTAURL:  "https://xrobo.qiniuapi.com/v1/ota/"},
		},
		{
			name: "mic 定长采集",
			args: []string{"--mac", "AA:BB", "--mic", "0", "--mic-duration", "3s"},
			want: config{MAC: "AA:BB", WakeWord: "小牛", Mic: "0", MicDuration: 3 * time.Second,
				LinxURL: "ws://xrobo-io.qiniuapi.com/v1/ws/",
				OTAURL:  "https://xrobo.qiniuapi.com/v1/ota/"},
		},
		{
			name:    "inject 与 mic 互斥",
			args:    []string{"--mac", "AA:BB", "--inject", "a.wav", "--mic", "0"},
			wantErr: "--inject 与 --mic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LINX_TOKEN", tt.envTok) // flag 默认值取自该环境变量，须钉死保证用例确定性
			got, err := parseFlags(tt.args)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// errCmdReader 恒定返回 err 的 Reader（命令循环读错误用例）。
type errCmdReader struct{ err error }

func (r *errCmdReader) Read([]byte) (int, error) { return 0, r.err }

// TestCommandLoop 验证命令循环语义：逐行分发；stdin EOF 后进程保持在线，
// 阻塞至 ctx 结束（后台/nohup 启动时输入流关闭不得杀掉设备进程）。
func TestCommandLoop(t *testing.T) {
	t.Run("逐行处理并在 EOF 后保持在线", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var mu sync.Mutex
		var got []string
		out := &syncBuf{}
		done := make(chan error, 1)
		go func() {
			done <- commandLoop(ctx, strings.NewReader("hello\nworld\n"), func(line string) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, line)
			}, out)
		}()

		// EOF 后短窗内不得返回（曾经的 bug：EOF 即退出）。
		select {
		case err := <-done:
			t.Fatalf("stdin EOF 后不应退出: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("ctx 取消后 commandLoop 未返回")
		}
		mu.Lock()
		require.Equal(t, []string{"hello", "world"}, got)
		mu.Unlock()
		require.Equal(t, 3, strings.Count(out.String(), "> "), "每次读行前一个提示符（含 EOF 前）")
	})

	t.Run("读错误被包装返回", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := commandLoop(ctx, &errCmdReader{err: errors.New("boom")}, func(string) {}, io.Discard)
		require.ErrorContains(t, err, "读 stdin")
		require.ErrorContains(t, err, "boom")
	})
}

// TestListenMode --mic 走 auto（平台端点检测自收口，支持连续说话）；
// --inject/纯指令保持 manual（设备音频发完补 listen stop 收口）。
func TestListenMode(t *testing.T) {
	tests := []struct {
		name        string
		inject, mic string
		want        string
	}{
		{"麦克风采集走 auto", "", "0", "auto"},
		{"注入文件保持 manual", "/tmp/a.wav", "", "manual"},
		{"两者皆空保持 manual", "", "", "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, listenMode(tt.inject, tt.mic))
		})
	}
}

// --- OTA 引导（对齐 linx-emu 连接流程） ---

// otaScript 是脚本化 OTA 应答器：按请求次数依次回放 script 中的响应体。
func otaScript(script []string, calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		if i := int(calls.Add(1)) - 1; i < len(script) {
			body = script[i]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func TestBootstrapToken(t *testing.T) {
	var calls atomic.Int32
	hs := httptest.NewServer(otaScript([]string{
		`{"activation":{"code":"483209","message":"去控制台绑定"}}`,
		`{"activation":{"code":"483209","message":"去控制台绑定"}}`,
		`{"websocket":{"url":"wss://ov.example/v1/ws/","token":"tk-1"}}`,
	}, &calls))
	t.Cleanup(hs.Close)
	out := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token, wsURL, err := bootstrapToken(ctx, hs.URL, "AA:BB", "cid-1", userAgent, time.Millisecond, out, logx.NewNop())

	require.NoError(t, err)
	require.Equal(t, "tk-1", token)
	require.Equal(t, "wss://ov.example/v1/ws/", wsURL)
	require.Equal(t, int32(3), calls.Load())
	require.Equal(t, 1, strings.Count(out.String(), "483209"), "相同激活码只打印一次")
}

// OTA 瞬时错误不致命：告警后继续轮询直至绑定。
func TestBootstrapToken_RetriesTransientError(t *testing.T) {
	var calls atomic.Int32
	hs := httptest.NewServer(otaScript([]string{
		`{bad json`,
		`{"websocket":{"url":"","token":"tk-2"}}`,
	}, &calls))
	t.Cleanup(hs.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token, wsURL, err := bootstrapToken(ctx, hs.URL, "AA:BB", "cid-1", userAgent, time.Millisecond, &syncBuf{}, logx.NewNop())

	require.NoError(t, err)
	require.Equal(t, "tk-2", token)
	require.Empty(t, wsURL, "OTA 未回 ws 地址时保持默认端点")
}

func TestBootstrapToken_CtxCancel(t *testing.T) {
	var calls atomic.Int32
	hs := httptest.NewServer(otaScript([]string{`{"activation":{"code":"111111","message":"x"}}`}, &calls))
	t.Cleanup(hs.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := bootstrapToken(ctx, hs.URL, "AA:BB", "cid-1", userAgent, time.Millisecond, &syncBuf{}, logx.NewNop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "OTA 引导")
}

// --- HandleLine 分流（单元：fake sink，无灵犀连接） ---

func TestHandleLine_Routing(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		wantCmds []string
		wantOut  string
	}{
		{name: "空行忽略", lines: []string{"", "   "}, wantCmds: []string{}, wantOut: ""},
		{
			name:     "提醒词命中送指令",
			lines:    []string{"小牛 建个群"},
			wantCmds: []string{"建个群"},
			wantOut:  "指令",
		},
		{
			name:     "中文逗号分隔命中",
			lines:    []string{"小牛，建个群"},
			wantCmds: []string{"建个群"},
			wantOut:  "指令",
		},
		{
			name:     "仅提醒词 rest 为空仍走指令",
			lines:    []string{"小牛"},
			wantCmds: []string{""},
			wantOut:  "指令",
		},
		{
			name:     "无提醒词提示对讲需音频",
			lines:    []string{"建个群吧"},
			wantCmds: []string{},
			wantOut:  "对讲需音频，请用麦克风或注入文件",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeSink{}
			out := &syncBuf{}
			app := newUnitApp(t, sink, out, nil)
			for _, l := range tt.lines {
				app.HandleLine(l)
			}
			require.Equal(t, tt.wantCmds, sink.snapshot())
			if tt.wantOut != "" {
				require.Contains(t, out.String(), tt.wantOut)
			}
		})
	}
}

// newUnitApp 构造不依赖灵犀连接的 App；next 为 nil 时 fake voicedrive 一律 ok。
func newUnitApp(t *testing.T, sink CommandSink, out *syncBuf, next func(c vdCall) groupclient.Result) *App {
	t.Helper()
	if next == nil {
		next = func(vdCall) groupclient.Result {
			return groupclient.Result{Status: groupclient.StatusOK, Message: "好的"}
		}
	}
	vd := &fakeVD{next: next}
	hs := httptest.NewServer(vd)
	t.Cleanup(hs.Close)
	logger := logx.NewNop()
	gc := groupclient.New(hs.URL, "AA:BB", time.Second, logger)
	var app *App
	cf := confirm.New(time.Now, func(confirm.Expired) { app.say(ExpiredPhrase) })
	reg := mcp.NewRegistry()
	require.NoError(t, registerGroupTools(reg, gc, cf, nil, logger))
	app = NewApp("小牛", cf, reg, sink, out, logger)
	return app
}

// --- 确认语境分流 ---

func TestHandleLine_ConfirmBareAnswerReplay(t *testing.T) {
	sink := &fakeSink{}
	out := &syncBuf{}
	vdCalls := make(chan vdCall, 2)
	app := newUnitApp(t, sink, out, func(c vdCall) groupclient.Result {
		vdCalls <- c
		return groupclient.Result{Status: groupclient.StatusOK, Message: "已切到夜班组"}
	})
	require.NoError(t, app.confirm.Ask(confirm.Pending{
		Question: "切哪个群？",
		Options:  []string{"夜班组", "休息室"},
		Tool:     "group.switch",
		Args:     map[string]any{"name": "任意"},
		ArgKey:   "name",
	}))

	app.HandleLine("夜班组") // 免提醒词裸答

	require.Equal(t, []string{}, sink.snapshot(), "裸答不应再走指令上行")
	require.Contains(t, out.String(), "已切到夜班组")
	select {
	case c := <-vdCalls:
		require.Equal(t, "/v1/group/switch", c.Path)
		require.Contains(t, c.Body, `"name":"夜班组"`, "重放须携带消歧覆写后的参数")
	case <-time.After(time.Second):
		t.Fatal("重放未到达 voicedrive")
	}
	require.False(t, app.confirm.Active())
}

func TestHandleLine_ConfirmMissFallsBackToRouting(t *testing.T) {
	sink := &fakeSink{}
	out := &syncBuf{}
	app := newUnitApp(t, sink, out, nil)
	require.NoError(t, app.confirm.Ask(confirm.Pending{
		Question: "切哪个群？",
		Options:  []string{"夜班组", "休息室"},
		Tool:     "group.switch",
		Args:     map[string]any{"name": "x"},
		ArgKey:   "name",
	}))

	app.HandleLine("小牛 查一下群") // 未命中选项 → 当作新输入重新走分流

	require.Equal(t, []string{"查一下群"}, sink.snapshot())
	require.False(t, app.confirm.Active(), "未命中后语境作废")
}

func TestHandleLine_ConfirmExpiredAnnounce(t *testing.T) {
	sink := &fakeSink{}
	out := &syncBuf{}
	app := newUnitApp(t, sink, out, nil)
	require.NoError(t, app.confirm.Ask(confirm.Pending{
		Question: "切哪个群？",
		Options:  []string{"夜班组"},
		Tool:     "group.switch",
		Args:     map[string]any{"name": "x"},
		ArgKey:   "name",
		TTL:      10 * time.Millisecond,
	}))
	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), ExpiredPhrase)
	}, 2*time.Second, 2*time.Millisecond)

	app.HandleLine("夜班组") // 超时作废后：非语境输入，走正常分流（无提醒词 → 提示对讲）
	require.Equal(t, []string{}, sink.snapshot())
	require.Contains(t, out.String(), "对讲需音频，请用麦克风或注入文件")
}

// --- 工具注册 ---

func TestRegisterGroupTools_TenTools(t *testing.T) {
	reg := mcp.NewRegistry()
	require.NoError(t, registerGroupTools(reg, nil, nil, nil, logx.NewNop()))
	d := mcp.NewDispatcherWithRegistry(reg)
	raw := d.Handle(nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	var got struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Result.Tools, 10)
	for _, tl := range got.Result.Tools {
		require.True(t, strings.HasPrefix(tl.Name, "group."), tl.Name)
	}
}

// 切群前截断钩子（03 §6.1 预留）：先于 voicedrive 转发触发。
func TestRegisterGroupTools_SwitchPreSwitchHook(t *testing.T) {
	order := make(chan string, 2)
	vd := &fakeVD{next: func(vdCall) groupclient.Result {
		order <- "vd"
		return groupclient.Result{Status: groupclient.StatusOK, Message: "已切群"}
	}}
	hs := httptest.NewServer(vd)
	t.Cleanup(hs.Close)
	logger := logx.NewNop()
	gc := groupclient.New(hs.URL, "AA:BB", time.Second, logger)
	reg := mcp.NewRegistry()
	require.NoError(t, registerGroupTools(reg, gc, confirm.New(time.Now, nil), func() { order <- "hook" }, logger))

	raw := mcp.NewDispatcherWithRegistry(reg).Handle(nil,
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group.switch","arguments":{"name":"夜班组"}}}`))
	require.NotNil(t, raw)
	require.Equal(t, []string{"hook", "vd"}, []string{<-order, <-order}, "截断钩子必须先于转发执行")
}

// --- 集成：硬边界 / 端到端 / clarify 回路 ---

// 硬边界（02 §10 专项）：无提醒词输入必须零业务上行、零工具调用。
// hello 与 listen start 属协议固有帧，不算业务上行。
func TestHardBoundary_NoWakeNoUplink(t *testing.T) {
	app, lxs, vd, _ := newHarness(t, func(vdCall) groupclient.Result {
		t.Error("无提醒词输入不得触发任何工具调用")
		return groupclient.Result{Status: groupclient.StatusOK}
	})
	app.HandleLine("建个群吧")

	time.Sleep(50 * time.Millisecond)
	texts := lxs.WaitForTexts(t, 2)
	require.Len(t, texts, 2, "只应有 hello + listen start，不得有任何业务上行")
	var hello, listenStart map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[0]), &hello))
	require.Equal(t, "hello", hello["type"])
	require.NoError(t, json.Unmarshal([]byte(texts[1]), &listenStart))
	require.Equal(t, "listen", listenStart["type"])
	require.Zero(t, len(vd.snapshot()), "voicedrive 零收到请求")
}

// 端到端：提醒词命中 → listen.detect 文本上行 → 平台 tools/call → 话术应答。
func TestE2E_HappyPath(t *testing.T) {
	app, lxs, vd, _ := newHarness(t, func(c vdCall) groupclient.Result {
		return groupclient.Result{Status: groupclient.StatusOK, Message: "夜班组已建好"}
	})
	app.HandleLine("小牛 建个群")

	texts := lxs.WaitForTexts(t, 3)
	var up map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[2]), &up))
	require.Equal(t, "listen", up["type"])
	require.Equal(t, "detect", up["state"])
	require.Equal(t, "建个群", up["text"])

	resp := pushToolsCall(t, lxs, 7, "group.create", `{"name":"夜班组"}`)
	require.Equal(t, "夜班组已建好", textOf(t, resp))

	calls := vd.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, "/v1/group/create", calls[0].Path)
	require.Equal(t, "AA:BB:CC:DD:EE:FF", calls[0].MAC, "身份只认 X-VD-MAC Header")
	require.NotContains(t, calls[0].Body, "AA:BB", "body 不得携带身份")
}

// clarify 回路：clarify 追问 → 裸答命中 → 携带消歧参数重放。
func TestE2E_ClarifyLoop(t *testing.T) {
	var n atomic.Int32
	app, lxs, vd, out := newHarness(t, func(c vdCall) groupclient.Result {
		if n.Add(1) == 1 {
			return groupclient.Result{
				Status:  groupclient.StatusClarify,
				Message: "切哪个群？",
				Options: []string{"夜班组", "休息室"},
			}
		}
		return groupclient.Result{Status: groupclient.StatusOK, Message: "已切到夜班组"}
	})
	resp := pushToolsCall(t, lxs, 1, "group.switch", `{"name":"随便"}`)
	require.Equal(t, "切哪个群？", textOf(t, resp))
	require.True(t, app.confirm.Active())

	app.HandleLine("夜班组") // 免提醒词裸答

	require.Contains(t, out.String(), "已切到夜班组")
	calls := vd.snapshot()
	require.Len(t, calls, 2)
	require.Contains(t, calls[1].Body, `"name":"夜班组"`)
	require.False(t, app.confirm.Active())
	// 裸答不应产生新的指令上行：文本帧仍只有 hello + listen start + mcp 应答
	require.Len(t, lxs.WaitForTexts(t, 3), 3)
}
