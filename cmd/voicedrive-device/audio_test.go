package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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
	go func() { done <- streamUplink(ctx, c, pr, 0) }()

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
