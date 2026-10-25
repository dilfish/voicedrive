package linx_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx/linxtest"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

// fakeSink 记录播放后端收到的字节与关闭/打断状态，可注入写入失败模拟后端死亡。
type fakeSink struct {
	mu         sync.Mutex
	data       []byte
	closed     bool
	killed     bool
	failWrites bool
}

func (f *fakeSink) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWrites {
		return 0, errors.New("fake: write failed")
	}
	f.data = append(f.data, p...)
	return len(p), nil
}

func (f *fakeSink) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSink) Kill() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.killed = true
}

func (f *fakeSink) dataSnapshot() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.data)
}

func (f *fakeSink) closedSnapshot() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeSink) killedSnapshot() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// sinkRecorder 每次启动后端生成一个新 fakeSink 并记录，模拟"一段 TTS 一个进程"。
type sinkRecorder struct {
	mu    sync.Mutex
	sinks []*fakeSink
}

func (r *sinkRecorder) start(context.Context) (linx.Sink, error) {
	s := &fakeSink{}
	r.mu.Lock()
	r.sinks = append(r.sinks, s)
	r.mu.Unlock()
	return s, nil
}

func (r *sinkRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sinks)
}

func (r *sinkRecorder) last() *fakeSink {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sinks) == 0 {
		return nil
	}
	return r.sinks[len(r.sinks)-1]
}

// lastData/lastClosed/lastKilled 空后端时返回零值，供 Eventually 谓词安全轮询。
func (r *sinkRecorder) lastData() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sinks) == 0 {
		return ""
	}
	return r.sinks[len(r.sinks)-1].dataSnapshot()
}

func (r *sinkRecorder) lastClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sinks) > 0 && r.sinks[len(r.sinks)-1].closedSnapshot()
}

func (r *sinkRecorder) lastKilled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sinks) > 0 && r.sinks[len(r.sinks)-1].killedSnapshot()
}

func (r *sinkRecorder) allData() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := ""
	for _, s := range r.sinks {
		out += s.dataSnapshot()
	}
	return out
}

// runPlayer 启动播放泵并把 Run 结果送入错误通道；t.Cleanup 取消 ctx。
func runPlayer(t *testing.T, buf *linx.AudioBuffer, start linx.SinkStarter, idle time.Duration) (*linx.Player, chan error) {
	t.Helper()
	p, err := linx.NewPlayer(buf, start, idle)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- p.Run(ctx) }()
	return p, errCh
}

// pumpAlive 断言泵未退出（错误通道无结果）。
func pumpAlive(t *testing.T, errCh chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		t.Fatalf("播放泵意外退出: %v", err)
	default:
	}
}

func TestPlayer_NewPlayerRejectsInvalidArgs(t *testing.T) {
	tests := []struct {
		name  string
		buf   *linx.AudioBuffer
		start linx.SinkStarter
		idle  time.Duration
	}{
		{name: "nil buffer", buf: nil, start: (&sinkRecorder{}).start, idle: time.Millisecond},
		{name: "nil starter", buf: linx.NewAudioBuffer(), start: nil, idle: time.Millisecond},
		{name: "zero idle", buf: linx.NewAudioBuffer(), start: (&sinkRecorder{}).start, idle: 0},
		{name: "negative idle", buf: linx.NewAudioBuffer(), start: (&sinkRecorder{}).start, idle: -time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := linx.NewPlayer(tt.buf, tt.start, tt.idle)
			require.Error(t, err)
		})
	}
}

func TestPlayer_StreamsFramesInOrder(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	_, errCh := runPlayer(t, buf, rec.start, 50*time.Millisecond)

	buf.Push([]byte("f1"))
	buf.Push([]byte("f2"))
	buf.Push([]byte("f3"))
	require.Eventually(t, func() bool { return rec.lastData() == "f1f2f3" }, 2*time.Second, 2*time.Millisecond)
	pumpAlive(t, errCh)
}

func TestPlayer_IdleTimeoutClosesSinkGracefully(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	_, errCh := runPlayer(t, buf, rec.start, 20*time.Millisecond)

	buf.Push([]byte("f1"))
	require.Eventually(t, func() bool { return rec.lastData() == "f1" }, 2*time.Second, 2*time.Millisecond)
	// 空闲超时优雅收口：Close 而非 Kill（后端播完缓冲自退）。
	require.Eventually(t, func() bool {
		return rec.lastClosed() && !rec.lastKilled()
	}, 2*time.Second, 2*time.Millisecond)

	// 下一段 TTS 开启新后端，同时 Kill 还在播尾音的前一段。
	buf.Push([]byte("f2"))
	require.Eventually(t, func() bool {
		return rec.count() == 2 && rec.lastData() == "f2"
	}, 2*time.Second, 2*time.Millisecond)
	require.Eventually(t, func() bool { return rec.sinks[0].killedSnapshot() }, 2*time.Second, 2*time.Millisecond)
	pumpAlive(t, errCh)
}

func TestPlayer_StopKillsBackendClearsBufferAndContinues(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	p, errCh := runPlayer(t, buf, rec.start, 50*time.Millisecond)

	buf.Push([]byte("f1"))
	require.Eventually(t, func() bool { return rec.count() == 1 }, 2*time.Second, 2*time.Millisecond)
	// 灌入积压帧：Stop 时缓冲中的残帧必须全部丢弃，不得泄漏进下一段。
	for i := 0; i < 100; i++ {
		buf.Push([]byte("x"))
	}
	p.Stop()
	require.Eventually(t, func() bool {
		return rec.lastKilled() && !buf.HasData()
	}, 2*time.Second, 2*time.Millisecond)

	// 打断后泵继续服务：后续帧归属新一段 TTS，进入全新后端且不含残帧。
	buf.Push([]byte("f2"))
	require.Eventually(t, func() bool {
		return rec.count() == 2 && rec.lastData() == "f2"
	}, 2*time.Second, 2*time.Millisecond)
	pumpAlive(t, errCh)
}

func TestPlayer_StopKillsDrainingBackend(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	p, errCh := runPlayer(t, buf, rec.start, 20*time.Millisecond)

	// 第一段优雅收口后后端仍在播尾音（draining）：Stop 必须立即打断它。
	buf.Push([]byte("f1"))
	require.Eventually(t, func() bool {
		return rec.lastClosed() && !rec.lastKilled()
	}, 2*time.Second, 2*time.Millisecond)
	p.Stop()
	require.Eventually(t, func() bool { return rec.lastKilled() }, 2*time.Second, 2*time.Millisecond)

	// 打断后泵继续服务下一段 TTS。
	buf.Push([]byte("f2"))
	require.Eventually(t, func() bool {
		return rec.count() == 2 && rec.lastData() == "f2"
	}, 2*time.Second, 2*time.Millisecond)
	pumpAlive(t, errCh)
}

func TestPlayer_SinkStarterErrorReturns(t *testing.T) {
	buf := linx.NewAudioBuffer()
	boom := errors.New("backend unavailable")
	_, errCh := runPlayer(t, buf, func(context.Context) (linx.Sink, error) {
		return nil, boom
	}, 50*time.Millisecond)

	buf.Push([]byte("f1"))
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, boom)
	case <-time.After(2 * time.Second):
		t.Fatal("泵未因后端启动失败退出")
	}
}

func TestPlayer_WriteErrorReturns(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	_, errCh := runPlayer(t, buf, rec.start, 50*time.Millisecond)

	buf.Push([]byte("f1"))
	require.Eventually(t, func() bool { return rec.count() == 1 }, 2*time.Second, 2*time.Millisecond)
	rec.last().mu.Lock()
	rec.last().failWrites = true
	rec.last().mu.Unlock()
	buf.Push([]byte("f2"))
	select {
	case err := <-errCh:
		require.Error(t, err)
		require.Contains(t, err.Error(), "write")
	case <-time.After(2 * time.Second):
		t.Fatal("泵未因后端写入失败退出")
	}
}

func TestPlayer_ContextCancelExits(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := linx.NewPlayer(buf, rec.start, 50*time.Millisecond)
	require.NoError(t, err)
	errCh := make(chan error, 1)
	go func() { errCh <- p.Run(ctx) }()

	buf.Push([]byte("f1"))
	require.Eventually(t, func() bool { return rec.count() == 1 }, 2*time.Second, 2*time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后泵未退出")
	}
	require.True(t, rec.lastKilled(), "退出时应立即打断后端进程")
}

func TestPlayer_ClosedBufferExits(t *testing.T) {
	buf := linx.NewAudioBuffer()
	rec := &sinkRecorder{}
	_, errCh := runPlayer(t, buf, rec.start, 50*time.Millisecond)

	require.NoError(t, buf.Close())
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("缓冲关闭后泵未退出")
	}
}

// 集成用例：灵犀 WS 下行二进制帧 → client 缓冲 → 播放泵 → 后端，全链路顺序一致。
func TestPlayer_PlaysFramesFromClient(t *testing.T) {
	srv := linxtest.New(t)
	buf := linx.NewAudioBuffer()
	c := linx.NewClient(linx.Config{
		URL:         srv.URL(),
		BackoffBase: 5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		OutboxSize:  64,
		Token:       "tk-test",
		DeviceID:    "AA:BB:CC:DD:EE:FF",
		ClientID:    "cid-test",
		UserAgent:   "vd-test/0.1",
	}, buf, nil, logx.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()

	rec := &sinkRecorder{}
	_, errCh := runPlayer(t, buf, rec.start, 50*time.Millisecond)

	srv.WaitForTexts(t, 1)
	require.NoError(t, srv.PushBinary([]byte("pcm1")))
	require.NoError(t, srv.PushBinary([]byte("pcm2")))
	require.Eventually(t, func() bool { return rec.allData() == "pcm1pcm2" }, 2*time.Second, 2*time.Millisecond)
	pumpAlive(t, errCh)
}
