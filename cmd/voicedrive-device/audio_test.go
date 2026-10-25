package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx/linxtest"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

// wavBytes 组装 44 字节标准头的 16k/mono/s16 WAV（与 ffmpeg s16le 输出同构）。
func wavBytes(pcm []byte) []byte {
	b := make([]byte, 44)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+len(pcm)))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)))
	return append(b, pcm...)
}

// newAudioClient 起真实 linx 客户端连 fake server，返回就绪（已持有 session）的客户端。
func newAudioClient(t *testing.T, url string) *linx.Client {
	t.Helper()
	c := linx.NewClient(linx.Config{
		URL: url, BackoffBase: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	}, linx.NewAudioBuffer(), nil, logx.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
	return c
}

func TestUplinkWAV(t *testing.T) {
	srv := linxtest.New(t)
	c := newAudioClient(t, srv.URL())
	srv.WaitForTexts(t, 2) // hello + listen start

	pcm := make([]byte, pcmFrameBytes*2+100)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	path := filepath.Join(t.TempDir(), "a.wav")
	require.NoError(t, os.WriteFile(path, wavBytes(pcm), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, uplinkWAV(ctx, c, path))

	// 恰 2 个整帧且内容逐字节精确：证明帧未被缓冲复写污染（别名 bug）。
	require.Equal(t, 2, srv.WaitForBinaries(t, 2))
	var binaries []string
	for _, f := range srv.Frames() {
		if f.Binary {
			binaries = append(binaries, f.Text)
		}
	}
	require.Equal(t, string(pcm[:pcmFrameBytes]), binaries[0])
	require.Equal(t, string(pcm[pcmFrameBytes:pcmFrameBytes*2]), binaries[1])

	// 收口：音频发完补 listen stop（带 session_id）。
	texts := srv.WaitForTexts(t, 3)
	var stop map[string]any
	require.NoError(t, json.Unmarshal([]byte(texts[2]), &stop))
	require.Equal(t, map[string]any{
		"type": "listen", "state": "stop", "session_id": "sid-1",
	}, stop)
	require.Equal(t, linx.StateListening, c.State()) // Wake 已注入
}

func TestUplinkWAV_Errors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content []byte
		wantErr string
	}{
		{name: "文件不存在", path: filepath.Join(t.TempDir(), "no.wav"), wantErr: "读 WAV"},
		{
			name:    "内容非 WAV",
			content: []byte("not a wav"),
			wantErr: "解析 WAV",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path
			if tt.content != nil {
				path = filepath.Join(t.TempDir(), "a.wav")
				require.NoError(t, os.WriteFile(path, tt.content, 0o600))
			}
			err := uplinkWAV(context.Background(), nil, path)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// ctx 取消后：已排队帧读到的不再上行、也不补 listen stop（平台已收口或进程退出）。
func TestStreamUplink_CancelStopsEarly(t *testing.T) {
	srv := linxtest.New(t)
	c := newAudioClient(t, srv.URL())
	srv.WaitForTexts(t, 2)

	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- streamUplink(ctx, c, pr, 0, true) }()

	frame := bytes.Repeat([]byte{7}, pcmFrameBytes)
	_, err := pw.Write(frame) // io.Pipe 写完即整帧已被消费
	require.NoError(t, err)
	require.Equal(t, 1, srv.WaitForBinaries(t, 1))

	cancel()
	_, err = pw.Write(frame) // 第二帧：ctx 已取消，读到也不得上行
	require.NoError(t, err)
	require.NoError(t, pw.Close())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 streamUplink 未返回")
	}
	time.Sleep(50 * time.Millisecond) // 观察窗：确认没有迟到帧/stop 帧
	count, texts := 0, 0
	for _, f := range srv.Frames() {
		if f.Binary {
			count++
		} else {
			texts++
		}
	}
	require.Equal(t, 1, count, "取消后不得再上行帧")
	require.Equal(t, 2, texts, "取消路径不发 listen stop（仍只有 hello + listen start）")
}

// mic 连续采集（stopAtEOF=false，auto 模式）：流结束不得补 listen stop，
// 收口由平台端点检测负责，设备补 stop 会打断连续对话。
func TestStreamUplink_NoStopAtEOF(t *testing.T) {
	srv := linxtest.New(t)
	c := newAudioClient(t, srv.URL())
	srv.WaitForTexts(t, 2)

	err := streamUplink(context.Background(), c,
		bytes.NewReader(bytes.Repeat([]byte{7}, pcmFrameBytes)), 0, false)
	require.NoError(t, err)
	require.Equal(t, 1, srv.WaitForBinaries(t, 1))

	time.Sleep(50 * time.Millisecond) // 观察窗：确认没有迟到的 stop 帧
	texts := 0
	for _, f := range srv.Frames() {
		if !f.Binary {
			texts++
		}
	}
	require.Equal(t, 2, texts, "auto 模式 EOF 不补 listen stop（仍只有 hello + listen start）")
}

// captureLogger 记录 Warnf 输出的 fake logger（等待提示用例）。
type captureLogger struct {
	mu    sync.Mutex
	warns []string
}

func (c *captureLogger) Debugf(string, ...any) {}
func (c *captureLogger) Infof(string, ...any)  {}
func (c *captureLogger) Errorf(string, ...any) {}

func (c *captureLogger) Warnf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warns = append(c.warns, fmt.Sprintf(format, args...))
}

func (c *captureLogger) warns_() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.warns...)
}

// TestWaitSession 等待平台会话：就绪即返回；等不到时周期打提示并随取消返回错误。
func TestWaitSession(t *testing.T) {
	t.Run("就绪后立即返回", func(t *testing.T) {
		srv := linxtest.New(t)
		c := newAudioClient(t, srv.URL())
		lg := &captureLogger{}
		err := waitSession(t.Context(), c, time.Hour, lg)
		require.NoError(t, err)
		require.Empty(t, lg.warns_())
	})

	t.Run("等不到会话周期提示并随取消返回错误", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		// 不 Run 的客户端永远没有 session_id。
		c := linx.NewClient(linx.Config{URL: "ws://127.0.0.1:1"}, linx.NewAudioBuffer(), nil, logx.NewNop())
		lg := &captureLogger{}
		done := make(chan error, 1)
		go func() { done <- waitSession(ctx, c, 100*time.Millisecond, lg) }()

		time.Sleep(350 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			require.ErrorContains(t, err, "等待会话被取消")
		case <-time.After(2 * time.Second):
			t.Fatal("ctx 取消后 waitSession 未返回")
		}
		warns := lg.warns_()
		require.GreaterOrEqual(t, len(warns), 2, "等待期间应周期打提示，got=%v", warns)
		require.Contains(t, warns[0], "等待平台会话")
	})
}

func TestMicContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	tests := []struct {
		name        string
		dur         time.Duration
		wantDeadine bool
	}{
		{name: "0 持续采集无截止", dur: 0, wantDeadine: false},
		{name: "正时长有截止兜底", dur: 5 * time.Second, wantDeadine: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cctx, cancel := micContext(parent, tt.dur)
			defer cancel()
			dl, ok := cctx.Deadline()
			require.Equal(t, tt.wantDeadine, ok)
			if ok {
				require.WithinDuration(t, time.Now().Add(tt.dur+2*time.Second), dl, time.Second)
			}
		})
	}
}
