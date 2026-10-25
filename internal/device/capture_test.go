package device

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestStreamPCMFrames(t *testing.T) {
	pcm := bytes.Repeat([]byte{1, 2}, 350)
	var frames [][]byte
	err := StreamPCM(context.Background(), bytes.NewReader(pcm), func(_ context.Context, frame []byte) error { frames = append(frames, frame); return nil })
	if err != nil || len(frames) != 2 || len(frames[1]) != 640 || !bytes.Equal(frames[1][:60], pcm[640:]) || !bytes.Equal(frames[1][60:], make([]byte, 580)) {
		t.Fatalf("frames=%v, err=%v", len(frames), err)
	}
	if err := StreamPCM(context.Background(), bytes.NewReader([]byte{1}), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("accepted partial sample")
	}
}

func TestCaptureHelper(t *testing.T) {
	if os.Getenv("VD_CAPTURE_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.Write(make([]byte, 640))
	for {
		time.Sleep(time.Second)
	}
}

func TestCaptureCancellationReapsProcess(t *testing.T) {
	t.Setenv("VD_CAPTURE_HELPER", "1")
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- captureCommand(ctx, func(context.Context, []byte) error { cancel(); return ctx.Err() }, path, "-test.run=^TestCaptureHelper$")
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not reap child")
	}
}
