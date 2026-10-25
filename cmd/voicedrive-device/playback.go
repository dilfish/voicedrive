// playback.go 实现 TTS 下行音频的播放后端（02-linx §4）：ffplay 从 stdin
// 流式播放灵犀下发的裸 PCM（16k/单声道/s16le，对齐 hello 声明的 audio_params）。
// 一个 ffplay 进程即一段 TTS：Close 关 stdin（EOF）让 ffplay 播完尾音自退，
// Kill 立即杀进程打断；两句重叠的收口由 Player 泵统一编排。

package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
)

// ffplayArgs 构造 ffplay 流式播放参数：stdin 读 16k 单声道 s16le 裸 PCM，
// 无界面、EOF 播完自退、只输出错误日志。声道用 -ch_layout 声明：
// ffmpeg 7 起移除了 -ac（channels 选项删除），-ch_layout 自 5.1 引入，
// 要求 ffmpeg ≥5.1（2022-08 发布）。
func ffplayArgs() []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-nodisp", "-autoexit",
		"-f", "s16le", "-ar", "16000", "-ch_layout", "mono",
		"-i", "-",
	}
}

// ffplaySink 实现 linx.Sink：Write 喂 PCM，Close/Kill 收口。两者可重复调用、
// 任意顺序组合（mu 保护状态位，重复调用幂等）。进程由构造时启动的收割
// goroutine Wait（waitCh 缓冲 1，不读也不泄漏），杜绝长跑设备累积僵尸进程。
type ffplaySink struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu     sync.Mutex
	closed bool
	killed bool

	waitCh chan error
}

// newFFPlaySink 启动 ffplay 后端并返回 sink；启动失败（如未安装 ffplay）直接报错。
func newFFPlaySink(ctx context.Context, bin string, stderr io.Writer) (linx.Sink, error) {
	cmd := exec.CommandContext(ctx, bin, ffplayArgs()...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 ffplay 输入管道: %w", err)
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ffplay（需安装 ffmpeg 套件）: %w", err)
	}
	sink := &ffplaySink{cmd: cmd, stdin: stdin, waitCh: make(chan error, 1)}
	go func() { sink.waitCh <- cmd.Wait() }()
	return sink, nil
}

// Write 写一帧 PCM 到 ffplay stdin。
func (s *ffplaySink) Write(p []byte) (int, error) {
	return s.stdin.Write(p)
}

// Close 优雅收口：EOF 通知 ffplay 播完尾音自退；已 Kill 或重复调用返回 nil。
func (s *ffplaySink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.killed {
		return nil
	}
	s.closed = true
	return s.stdin.Close()
}

// Kill 立即打断：杀掉 ffplay 进程；重复调用幂等。
func (s *ffplaySink) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.killed {
		return
	}
	s.killed = true
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}
