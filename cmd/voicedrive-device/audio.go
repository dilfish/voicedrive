// audio.go 实现设备模拟器的真音频上行（02-linx §3，对齐 linx-emu 方案 A）：
// --inject 从 WAV 文件读裸 PCM、--mic 经 ffmpeg 采集（macOS avfoundation），
// 统一按 20ms 帧上行灵犀 WS。收口分两种：--inject 走 manual（发完补 listen
// stop），--mic 走 auto（平台端点检测自收口，设备不补 stop）。

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/Study-of-slag-weng/voicedrive1/internal/audiosrc"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

// pcmFrameBytes 是上行 PCM 帧字节数：320 采样 × 2 字节 = 20ms @ 16k s16le。
const pcmFrameBytes = 640

// framePace 是文件回放帧间隔（对齐真实开口节奏，避免瞬间灌满平台缓冲）。
// 麦克风采集天然实时，不需要额外配速。
const framePace = 20 * time.Millisecond

// waitSession 等待平台 hello 分配 session_id（音频上行与 listen 帧都依赖它）。
// 等不到时每 warnEvery 打一条提示，区分"在等平台"与"进程卡死"。
func waitSession(ctx context.Context, c *linx.Client, warnEvery time.Duration, logger logx.Logger) error {
	lastWarn := time.Now()
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	for {
		if c.SessionID() != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待会话被取消: %w", ctx.Err())
		case <-tk.C:
			if time.Since(lastWarn) >= warnEvery {
				lastWarn = time.Now()
				logger.Warnf("仍在等待平台会话（连接灵犀并收到 hello 后自动就绪）…")
			}
		}
	}
}

// streamUplink 把 r 中的 PCM 按 20ms 帧上行：Wake 注入本地唤醒、pace>0 时按帧
// 间隔配速；stopAtEOF 为 true 时正常发完补 listen stop 收口（manual 模式），
// false 时不补（auto 模式由平台端点检测收口，设备补 stop 会打断连续对话）。
// ctx 中途取消一律不补 stop（平台已收口或进程退出，重复 stop 无意义）。
func streamUplink(ctx context.Context, c *linx.Client, r io.Reader, pace time.Duration, stopAtEOF bool) error {
	c.Wake()
	var tk *time.Ticker
	if pace > 0 {
		tk = time.NewTicker(pace)
		defer tk.Stop()
	}
	err := audiosrc.Frames(r, pcmFrameBytes, func(frame []byte) {
		if tk != nil {
			<-tk.C
		}
		if ctx.Err() != nil {
			return
		}
		c.SendAudio(frame)
	})
	if err != nil {
		return fmt.Errorf("帧化 PCM: %w", err)
	}
	if ctx.Err() != nil {
		return nil
	}
	if stopAtEOF {
		c.SendListenStop()
	}
	return nil
}

// uplinkWAV 读取 WAV 文件并按真实节奏上行其 data chunk（--inject，等同一次开口）。
func uplinkWAV(ctx context.Context, c *linx.Client, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读 WAV: %w", err)
	}
	pcm, err := audiosrc.WAVData(raw)
	if err != nil {
		return fmt.Errorf("解析 WAV: %w", err)
	}
	return streamUplink(ctx, c, bytes.NewReader(pcm), framePace, true)
}

// uplinkMic 经 ffmpeg 采集麦克风 PCM 并实时上行（--mic，设备序号对 avfoundation
// 而言形如 ":0"；需安装 ffmpeg，macOS 还需给终端麦克风权限）。dur>0 为定长
// 采集兜底；dur=0 持续采集，说完由平台 listen stop 判定收口。
func uplinkMic(ctx context.Context, c *linx.Client, device string, dur time.Duration, stderr io.Writer, logger logx.Logger) error {
	cctx, cancel := micContext(ctx, dur)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "avfoundation", "-i", ":"+device,
		"-ar", "16000", "-ac", "1", "-f", "s16le", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 ffmpeg 管道: %w", err)
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg（需安装 ffmpeg，macOS 还需给终端麦克风权限）: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	if dur > 0 {
		logger.Infof("麦克风定长采集 %v（avfoundation 设备 :%s）", dur, device)
	} else {
		logger.Infof("麦克风持续采集（avfoundation 设备 :%s），说完由平台判定收口，Ctrl-C 退出", device)
	}
	return streamUplink(ctx, c, stdout, 0, false)
}

// micContext 构造采集 ctx：dur=0 持续（无截止），dur>0 加 dur+2s 截止兜底
// （余量留给 ffmpeg 收尾，避免截掉最后半句）。
func micContext(ctx context.Context, dur time.Duration) (context.Context, context.CancelFunc) {
	if dur <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, dur+2*time.Second)
}
