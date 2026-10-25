// playback.go 是 AudioBuffer 的消费端（02-linx §2）：播放泵按序取出 TTS
// PCM 帧写入外部播放后端。与生产者（WS 读循环 Push）经 AudioBuffer 的
// mutex+cond 解耦，泵自身单 goroutine 运行。

package linx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Sink 是一段 TTS 的播放后端输入流：Write 喂 PCM 帧；Close 优雅收口
// （后端播完已缓冲音频后自退）；Kill 立即打断进程。两方法均可重复调用。
type Sink interface {
	io.WriteCloser
	Kill()
}

// SinkStarter 启动外部播放后端；每段 TTS 调用一次。ctx 取消时后端进程
// 应随之终止（如 exec.CommandContext）。
type SinkStarter func(context.Context) (Sink, error)

// Player 是 TTS 下行播放泵。Run 阻塞运行至 ctx 取消、缓冲关闭或后端故障。
//
// 并发安全设计：sink/draining 仅泵 goroutine 读写，Stop 只置原子标志、
// 不触碰后端，打断动作全部串行回泵内处理——无共享可变状态、无锁竞态。
// 代价是空闲等待期 Stop 最迟 idle 后生效（无帧可播时无打断对象）。
// 播放中 Stop 一帧内生效：当前在途帧（≤20ms）可能仍写入旧后端。
type Player struct {
	buf   *AudioBuffer
	start SinkStarter
	idle  time.Duration

	stopReq atomic.Bool
}

// NewPlayer 构造播放泵；buf/start 必填，idle 为句间空闲收口阈值（>0）。
func NewPlayer(buf *AudioBuffer, start SinkStarter, idle time.Duration) (*Player, error) {
	if buf == nil {
		return nil, errors.New("linx: audio buffer is required")
	}
	if start == nil {
		return nil, errors.New("linx: sink starter is required")
	}
	if idle <= 0 {
		return nil, errors.New("linx: idle timeout must be positive")
	}
	return &Player{buf: buf, start: start, idle: idle}, nil
}

// Stop 打断当前播放：作废已缓冲残帧并立即终止后端，泵继续服务后续 TTS。
// 可在任意 goroutine 调用，泵空闲期调用同样有效。残留竞态边界：泵处理完
// 打断标志之后才到达的帧会被视为新一段 TTS 正常播放（帧归属无法判别，
// 由平台在抢话时停止下发旧句音频来兜底）。
func (p *Player) Stop() { p.stopReq.Store(true) }

// Run 阻塞运行播放循环；返回 nil 表示 ctx 取消或缓冲关闭，其余为后端故障。
func (p *Player) Run(ctx context.Context) error {
	var sink Sink     // 正在写入的后端
	var draining Sink // 已优雅收口、仍在播尾音的后端
	defer func() {
		if sink != nil {
			sink.Kill()
		}
	}()
	for {
		if p.stopReq.Swap(false) {
			if sink != nil {
				sink.Kill()
				sink = nil
			}
			if draining != nil {
				draining.Kill()
				draining = nil
			}
			p.buf.Clear()
		}

		waitCtx, cancel := context.WithTimeout(ctx, p.idle)
		frame, err := p.buf.Pop(waitCtx)
		cancel()
		if err != nil {
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				// 句间空闲：优雅收口，后端播完尾音自退；保留引用供
				// 新一段 TTS 到达时 Kill（避免新旧音频重叠）。
				if sink != nil {
					_ = sink.Close()
					draining = sink
					sink = nil
				}
				if ctx.Err() != nil {
					return nil
				}
				continue
			case ctx.Err() != nil, errors.Is(err, ErrClosed):
				return nil
			default:
				return fmt.Errorf("linx: pop audio frame: %w", err)
			}
		}
		if sink == nil {
			if draining != nil {
				draining.Kill()
				draining = nil
			}
			sink, err = p.start(ctx)
			if err != nil {
				return fmt.Errorf("linx: start playback sink: %w", err)
			}
		}
		if _, err := sink.Write(frame); err != nil {
			sink.Kill()
			return fmt.Errorf("linx: write audio frame: %w", err)
		}
	}
}
