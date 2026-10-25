// Package linx 实现灵犀 WS 客户端及其构件（02-linx §2/§4）：
// AudioBuffer 是 TTS 下行生产者-消费者缓冲，语义 1:1 对齐 linx-linux
// 的 queue+mutex+condition_variable+atomic；state.go 是会话状态机。
package linx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrClosed 表示 AudioBuffer 已关闭：Push 丢弃、Pop 立即返回。
var ErrClosed = errors.New("linx: audio buffer closed")

// AudioBuffer 是 TTS 帧队列：生产者（WS 读循环 Push 二进制帧）与
// 消费者（播放泵 Pop）并发安全；Close 唤醒所有阻塞的 Pop。
type AudioBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames [][]byte
	closed bool
	has    atomic.Bool
}

// NewAudioBuffer 构造空缓冲。
func NewAudioBuffer() *AudioBuffer {
	b := &AudioBuffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Push 入队一帧（TTS 二进制帧原样字节）；已关闭时丢弃。
// has 原子标记供播放泵无锁快速探测（对齐 linx-linux has_data）。
func (b *AudioBuffer) Push(frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.frames = append(b.frames, frame)
	b.has.Store(true)
	b.cond.Signal()
}

// Pop 阻塞取队头帧；队列空则等待 Push 或 Close；
// Close 后返回 ErrClosed，ctx 先到期返回 ctx.Err()。
func (b *AudioBuffer) Pop(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		if len(b.frames) > 0 {
			frame := b.frames[0]
			b.frames = b.frames[1:]
			if len(b.frames) == 0 {
				b.has.Store(false)
			}
			return frame, nil
		}
		if b.closed {
			return nil, ErrClosed
		}
		// ctx 取消需唤醒 cond 等待：注册一次性广播再进入等待
		if ctx.Done() != nil {
			stop := context.AfterFunc(ctx, b.cond.Broadcast)
			b.cond.Wait()
			stop()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		b.cond.Wait()
	}
}

// HasData 原子报告是否有待播帧。
func (b *AudioBuffer) HasData() bool { return b.has.Load() }

// Clear 丢弃全部待播帧并复位 has（如切群/打断时清残音）。
func (b *AudioBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = nil
	b.has.Store(false)
	b.cond.Broadcast()
}

// Close 关闭缓冲：唤醒全部阻塞 Pop（返回 ErrClosed），其后 Push 丢弃。
func (b *AudioBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.cond.Broadcast()
	return nil
}
