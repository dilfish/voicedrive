package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
