package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
)

func TestFFPlayArgs(t *testing.T) {
	require.Equal(t, []string{
		"-hide_banner", "-loglevel", "error",
		"-nodisp", "-autoexit",
		"-f", "s16le", "-ar", "16000", "-ch_layout", "mono",
		"-i", "-",
	}, ffplayArgs())
}

func TestNewFFPlaySinkMissingBinary(t *testing.T) {
	_, err := newFFPlaySink(context.Background(), filepath.Join(t.TempDir(), "no-such-ffplay"), io.Discard)
	require.Error(t, err)
}

// writeHelper 落一个可执行 shell 脚本当假 ffplay，返回其路径。脚本经 exec
// 内置命令让子进程与脚本共享 PID，Kill 时不会留下孤儿进程。
func writeHelper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fakeffplay.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return path
}

// helperPID 从脚本旁的 .pid 标记文件读出脚本 PID（脚本启动时写）。
func helperPID(t *testing.T, path string) int {
	t.Helper()
	pidFile := path + ".pid"
	require.Eventually(t, func() bool {
		_, err := os.Stat(pidFile)
		return err == nil
	}, 2*time.Second, 2*time.Millisecond)
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	var pid int
	_, err = fmt.Sscanf(string(raw), "%d", &pid)
	require.NoError(t, err)
	return pid
}

func TestFFPlaySinkKillTerminatesProcess(t *testing.T) {
	path := writeHelper(t, "echo $$ > \"$0.pid\"\nexec sleep 60\n")
	sink, err := newFFPlaySink(context.Background(), path, io.Discard)
	require.NoError(t, err)
	var _ linx.Sink = sink
	ff := sink.(*ffplaySink)
	pid := helperPID(t, path)

	ff.Kill()
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 2*time.Millisecond)

	// 后台收割 goroutine 已 Wait：信号杀死的退出错误非 nil。
	select {
	case err := <-ff.waitCh:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("进程未被收割")
	}
	// 幂等：重复 Kill 不 panic、不重复 Wait。
	ff.Kill()
}

func TestFFPlaySinkCloseDrainsAndReaps(t *testing.T) {
	path := writeHelper(t, "exec cat > /dev/null\n")
	sink, err := newFFPlaySink(context.Background(), path, io.Discard)
	require.NoError(t, err)
	ff := sink.(*ffplaySink)

	_, err = sink.Write([]byte("pcm-frame"))
	require.NoError(t, err)
	require.NoError(t, sink.Close())

	// EOF 后假 ffplay 播完尾音自退：退出码 0。
	select {
	case err := <-ff.waitCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("假 ffplay 未在 EOF 后退出")
	}
	// 幂等：重复 Close 返回 nil。
	require.NoError(t, sink.Close())
}

func TestFFPlaySinkKillAfterCloseTerminatesProcess(t *testing.T) {
	path := writeHelper(t, "echo $$ > \"$0.pid\"\nwhile read -r line; do :; done\nexec sleep 60\n")
	sink, err := newFFPlaySink(context.Background(), path, io.Discard)
	require.NoError(t, err)
	ff := sink.(*ffplaySink)
	pid := helperPID(t, path)

	_, err = sink.Write([]byte("pcm-frame"))
	require.NoError(t, err)
	// 优雅收口：EOF 后脚本进入"播尾音"长眠，Close 不得杀进程。
	require.NoError(t, sink.Close())
	require.Never(t, func() bool { return syscall.Kill(pid, 0) != nil }, 100*time.Millisecond, 5*time.Millisecond)

	// Close 之后的 Kill 仍必须立即打断在播尾音的后端。
	sink.Kill()
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 2*time.Millisecond)
	select {
	case err := <-ff.waitCh:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("进程未被收割")
	}
	// Kill 后的 Close 幂等返回 nil。
	require.NoError(t, sink.Close())
}
