// Package audiosrc 提供设备侧音频源工具：从 WAV 提取裸 PCM、把字节流按
// 固定尺寸切帧，供 --inject/--mic 调试上行共用（02-linx §3）。
package audiosrc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// WAVData 从最小结构 WAV 文件字节中提取 data chunk 的裸 PCM。
// 只做 RIFF/WAVE 头校验与子块遍历（奇数长度子块跳 padding），不解释 fmt 参数。
func WAVData(raw []byte) ([]byte, error) {
	if len(raw) < 12 || !bytes.Equal(raw[0:4], []byte("RIFF")) || !bytes.Equal(raw[8:12], []byte("WAVE")) {
		return nil, fmt.Errorf("不是 WAV 文件（缺 RIFF/WAVE 头）: %d 字节", len(raw))
	}
	off := 12
	for off+8 <= len(raw) {
		id := string(raw[off : off+4])
		size := int(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		body := off + 8
		if id == "data" {
			end := body + size
			if end > len(raw) { // 声明长度超出实际文件：截断到文件尾
				end = len(raw)
			}
			return raw[body:end], nil
		}
		off = body + size + size%2 // 子块按 2 字节对齐，奇数长度后跟 1 字节 padding
	}
	return nil, fmt.Errorf("WAV 没有 data chunk")
}

// Frames 把 r 中的字节按 frameSize 切帧，逐帧回调 onFrame；尾部不足一帧丢弃。
// 回调拿到的切片是独立拷贝：帧通常被异步发送（入队后延迟写出），
// 复用内部缓冲会污染已排队的帧内容。
func Frames(r io.Reader, frameSize int, onFrame func(frame []byte)) error {
	if frameSize <= 0 {
		return fmt.Errorf("frameSize 必须为正: %d", frameSize)
	}
	buf := make([]byte, frameSize)
	for {
		_, err := io.ReadFull(r, buf)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // 尾部不足帧丢弃
			}
			return fmt.Errorf("读 PCM 帧: %w", err)
		}
		onFrame(bytes.Clone(buf))
	}
}
