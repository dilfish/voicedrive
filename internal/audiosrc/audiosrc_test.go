package audiosrc_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/audiosrc"
)

// chunk 组装一个 RIFF 子块（4 字节 id + 4 字节小端长度 + body）。
func chunk(id string, body []byte) []byte {
	b := make([]byte, 8)
	copy(b, id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	return append(b, body...)
}

// riff 组装最小 WAV：12 字节头 + 任意子块序列。
func riff(chunks ...[]byte) []byte {
	head := make([]byte, 12)
	copy(head, "RIFF")
	binary.LittleEndian.PutUint32(head[4:], 4) // 占位，WAVData 不依赖该值
	copy(head[8:], "WAVE")
	return append(head, bytes.Join(chunks, nil)...)
}

func TestWAVData(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		want    []byte
		wantErr string
	}{
		{
			name: "标准：跳过 fmt 取 data",
			raw: riff(
				chunk("fmt ", make([]byte, 16)),
				chunk("data", []byte{1, 2, 3, 4, 5}),
			),
			want: []byte{1, 2, 3, 4, 5},
		},
		{
			name: "data 在最前",
			raw: riff(
				chunk("data", []byte{9, 9}),
				chunk("fmt ", make([]byte, 16)),
			),
			want: []byte{9, 9},
		},
		{
			name: "奇数长度子块后跳过 1 字节 padding",
			raw: riff(
				append(chunk("junk", []byte{7}), 0), // 手动补 padding 字节，walk 须跳过才能落到 data
				chunk("data", []byte{1, 2}),
			),
			want: []byte{1, 2},
		},
		{
			name: "data size 超实际长度截断",
			raw: func() []byte {
				b := riff(chunk("data", []byte{1, 2, 3}))
				binary.LittleEndian.PutUint32(b[16:], 999) // data size 改成远超文件
				return b
			}(),
			want: []byte{1, 2, 3},
		},
		{
			name:    "非 RIFF 头",
			raw:     []byte("not a wav file at all"),
			wantErr: "RIFF/WAVE",
		},
		{
			name:    "文件过短",
			raw:     []byte("RIFF"),
			wantErr: "RIFF/WAVE",
		},
		{
			name:    "无 data chunk",
			raw:     riff(chunk("fmt ", make([]byte, 16))),
			wantErr: "data chunk",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := audiosrc.WAVData(tt.raw)
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

func TestFrames(t *testing.T) {
	tests := []struct {
		name      string
		src       []byte
		frameSize int
		want      [][]byte
	}{
		{name: "空流零帧", src: nil, frameSize: 4},
		{name: "整一帧", src: []byte{1, 2, 3, 4}, frameSize: 4, want: [][]byte{{1, 2, 3, 4}}},
		{
			name:      "两帧半：尾部不足帧丢弃",
			src:       []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
			frameSize: 4,
			want:      [][]byte{{1, 2, 3, 4}, {5, 6, 7, 8}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got [][]byte
			require.NoError(t, audiosrc.Frames(bytes.NewReader(tt.src), tt.frameSize, func(f []byte) { got = append(got, f) }))
			require.Equal(t, tt.want, got)
		})
	}
}

// errReader 恒定返回错误，驱动 Frames 的读失败分支。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestFrames_ReadError(t *testing.T) {
	err := audiosrc.Frames(errReader{}, 4, func([]byte) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "读 PCM 帧")
	require.Contains(t, err.Error(), "boom")
}

// 回调拿到的切片不得与内部缓冲别名：帧会被异步发送（outbox 排队），
// 若不 Clone，后续读取覆写缓冲将污染已上行内容。
func TestFrames_FrameNotAliased(t *testing.T) {
	src := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	var got [][]byte
	require.NoError(t, audiosrc.Frames(bytes.NewReader(src), 4, func(f []byte) { got = append(got, f) }))
	require.Equal(t, [][]byte{{0, 1, 2, 3}, {4, 5, 6, 7}, {8, 9, 10, 11}}, got)
}

// 帧尺寸非法须报错而非 panic。
func TestFrames_InvalidFrameSize(t *testing.T) {
	err := audiosrc.Frames(strings.NewReader("x"), 0, func([]byte) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "frameSize")
}
