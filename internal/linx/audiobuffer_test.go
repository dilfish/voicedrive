package linx

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAudioBuffer_PopBlocksUntilPush(t *testing.T) {
	b := NewAudioBuffer()
	go func() {
		time.Sleep(20 * time.Millisecond)
		b.Push([]byte("frame-1"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := b.Pop(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("frame-1"), got)
	require.True(t, b.HasData() == false)
}

func TestAudioBuffer_FIFO(t *testing.T) {
	b := NewAudioBuffer()
	b.Push([]byte("a"))
	b.Push([]byte("b"))
	ctx := context.Background()
	got, err := b.Pop(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("a"), got)
	got, err = b.Pop(ctx)
	require.NoError(t, err)
	require.Equal(t, []byte("b"), got)
}

func TestAudioBuffer_HasData(t *testing.T) {
	b := NewAudioBuffer()
	require.False(t, b.HasData())
	b.Push([]byte("a"))
	require.True(t, b.HasData())
	got, err := b.Pop(context.Background())
	require.NoError(t, err)
	require.Equal(t, []byte("a"), got)
	require.False(t, b.HasData())
}

func TestAudioBuffer_PopAfterClose(t *testing.T) {
	b := NewAudioBuffer()
	require.NoError(t, b.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := b.Pop(ctx)
	require.ErrorIs(t, err, ErrClosed)
	// 已关闭后 Push 不 panic、不入队
	b.Push([]byte("x"))
	require.False(t, b.HasData())
}

func TestAudioBuffer_CloseWakesBlockedPop(t *testing.T) {
	b := NewAudioBuffer()
	popped := make(chan error, 1)
	go func() {
		_, err := b.Pop(context.Background())
		popped <- err
	}()
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, b.Close())
	select {
	case err := <-popped:
		require.ErrorIs(t, err, ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("Close 未唤醒阻塞的 Pop")
	}
}

func TestAudioBuffer_PopCtxCanceled(t *testing.T) {
	b := NewAudioBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.Pop(ctx)
	require.ErrorIs(t, err, ctx.Err())
}

func TestAudioBuffer_Clear(t *testing.T) {
	b := NewAudioBuffer()
	b.Push([]byte("a"))
	b.Push([]byte("b"))
	b.Clear()
	require.False(t, b.HasData())
	// 清空后队列空：Pop 阻塞等待新帧，短超时 ctx 应以 ctx.Err 返回
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := b.Pop(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestAudioBuffer_Concurrent(t *testing.T) {
	b := NewAudioBuffer()
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Push([]byte{byte(id), byte(i)})
			}
		}(p)
	}
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = b.Pop(context.Background())
			}
		}()
	}
	wg.Wait()
	require.NoError(t, b.Close())
}
