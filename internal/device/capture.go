package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"time"
)

const PCMFrameBytes = 640

// StreamPCM sends 20ms frames. The final short frame is zero-padded.
// Source owners must close blocking readers when ctx is cancelled.
func StreamPCM(ctx context.Context, source io.Reader, send func(context.Context, []byte) error) error {
	if send == nil {
		return errors.New("PCM frame handler is required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame := make([]byte, PCMFrameBytes)
		n, err := io.ReadFull(source, frame)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("read PCM: %w", err)
		}
		if n%2 != 0 {
			return errors.New("PCM ends with an incomplete sample")
		}
		if n > 0 {
			if err := send(ctx, frame); err != nil {
				return err
			}
		}
		if err != nil {
			return nil
		}
	}
}

// CaptureMicrophone streams raw PCM from ffmpeg until cancelled. CommandContext
// terminates capture and Wait reaps the process on every exit path.
func CaptureMicrophone(ctx context.Context, input string, send func(context.Context, []byte) error) error {
	if input == "" {
		return errors.New("microphone input is required")
	}
	backend := "alsa"
	if runtime.GOOS == "darwin" {
		backend = "avfoundation"
		input = ":" + input
	}
	if runtime.GOOS == "windows" {
		backend = "dshow"
		input = "audio=" + input
	}
	return captureCommand(ctx, send, "ffmpeg", "-nostdin", "-loglevel", "error", "-f", backend, "-i", input, "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
}

func captureCommand(ctx context.Context, send func(context.Context, []byte) error, executable string, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start capture: %w", err)
	}
	streamErr := StreamPCM(ctx, stdout, send)
	cancelled := ctx.Err()
	if streamErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if cancelled != nil {
		return cancelled
	}
	if streamErr != nil {
		return streamErr
	}
	if waitErr != nil {
		return fmt.Errorf("capture process: %w", waitErr)
	}
	return nil
}
