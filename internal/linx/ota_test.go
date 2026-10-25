package linx_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
)

// fakeOTA 录制 OTA 请求并按脚本回放响应。
type fakeOTA struct {
	mu     sync.Mutex
	reqs   []http.Header
	bodies []string
	script []string // 依次返回的响应体
	calls  int
	status int // 非 0 时覆盖 script 返回该状态码
}

func (f *fakeOTA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Header.Clone())
	f.bodies = append(f.bodies, string(b))
	i := f.calls
	f.calls++
	status, body := http.StatusOK, ""
	if f.status != 0 {
		status = f.status
	} else if i < len(f.script) {
		body = f.script[i]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestRequestOTA(t *testing.T) {
	tests := []struct {
		name      string
		script    []string
		status    int
		wantErr   string
		wantAct   *linx.OTAActivation
		wantWS    *linx.OTAWebSocket
		checkBody bool
	}{
		{
			name:    "未绑定返回激活码",
			script:  []string{`{"activation":{"code":"483209","message":"去控制台绑定"}}`},
			wantAct: &linx.OTAActivation{Code: "483209", Message: "去控制台绑定"},
		},
		{
			name:   "已绑定返回 token 与 ws 地址",
			script: []string{`{"websocket":{"url":"wss://xrobo-io.example/v1/ws/","token":"tk-1"}}`},
			wantWS: &linx.OTAWebSocket{URL: "wss://xrobo-io.example/v1/ws/", Token: "tk-1"},
		},
		{
			name:    "业务错误返回错误",
			script:  []string{`{"error":"device frozen"}`},
			wantErr: "device frozen",
		},
		{
			name:    "HTTP 非 200 返回错误",
			status:  500,
			wantErr: "OTA HTTP 500",
		},
		{
			name:    "畸形响应返回错误",
			script:  []string{`{bad json`},
			wantErr: "解析 OTA 响应",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeOTA{script: tt.script, status: tt.status}
			hs := httptest.NewServer(f)
			t.Cleanup(hs.Close)

			got, err := linx.RequestOTA(context.Background(), hs.URL, "AA:BB", "cid-1", "vd/0.1")
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantAct, got.Activation)
			require.Equal(t, tt.wantWS, got.WebSocket)
		})
	}
}

func TestRequestOTA_HeadersAndBody(t *testing.T) {
	f := &fakeOTA{script: []string{`{"websocket":{"token":"tk"}}`}}
	hs := httptest.NewServer(f)
	t.Cleanup(hs.Close)

	_, err := linx.RequestOTA(context.Background(), hs.URL, "AA:BB:CC", "cid-9", "voicedrive-device/0.1.0")
	require.NoError(t, err)

	h := f.reqs[0]
	require.Equal(t, "1", h.Get("Activation-Version"))
	require.Equal(t, "AA:BB:CC", h.Get("Device-Id"))
	require.Equal(t, "cid-9", h.Get("Client-Id"))
	require.Equal(t, "voicedrive-device/0.1.0", h.Get("User-Agent"))
	require.Equal(t, "zh-CN", h.Get("Accept-Language"))
	require.Equal(t, "application/json", h.Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.bodies[0]), &body))
	require.Equal(t, "AA:BB:CC", body["mac_address"])
	require.Equal(t, "cid-9", body["uuid"])
	require.Equal(t, "esp32s3", body["chip_model_name"])
	board, ok := body["board"].(map[string]any)
	require.True(t, ok, "board 必填")
	require.Equal(t, "AA:BB:CC", board["mac"])
}
