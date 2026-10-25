package device

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// InputConfig controls one device input source.
type InputConfig struct {
	Say        string
	Inject     string
	Transcript string
	REPL       bool
}

// ValidateInput rejects incompatible input modes.
func ValidateInput(config InputConfig) error {
	if config.Say != "" && config.Inject != "" {
		return errors.New("-say and -inject are mutually exclusive")
	}
	if config.REPL && (config.Say != "" || config.Inject != "") {
		return errors.New("interactive mode cannot be combined with -say or -inject")
	}
	return nil
}

// MessageSender submits a text message to the active group.
type MessageSender interface {
	Send(context.Context, string) error
}

// InputRunner turns text or injected audio into buffered utterances and messages.
type InputRunner struct {
	Buffer *UtteranceBuffer
	Sender MessageSender
	Now    func() time.Time
	ID     func() string
}

// Run executes the configured non-interactive input source.
func (r *InputRunner) Run(ctx context.Context, config InputConfig) error {
	if err := ValidateInput(config); err != nil {
		return err
	}
	if r.Buffer == nil || r.Sender == nil {
		return errors.New("input buffer and sender are required")
	}
	if config.Say != "" {
		return r.submit(ctx, config.Say, nil)
	}
	if config.Inject != "" {
		pcm, err := ReadWAV(config.Inject)
		if err != nil {
			return err
		}
		return r.submit(ctx, config.Transcript, pcm)
	}
	return nil
}

func (r *InputRunner) submit(ctx context.Context, text string, pcm []byte) error {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	id := fmt.Sprintf("utterance-%d", now().UnixNano())
	if r.ID != nil {
		id = r.ID()
	}
	if pcm != nil {
		if err := r.Buffer.Add(id, text, pcm, now()); err != nil {
			return err
		}
	}
	if text == "" {
		return errors.New("injected audio requires recognized text")
	}
	if err := r.Sender.Send(ctx, text); err != nil {
		return fmt.Errorf("send input message: %w", err)
	}
	return nil
}

// ReadWAV loads a 16 kHz mono 16-bit PCM WAV file.
func ReadWAV(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read WAV: %w", err)
	}
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("invalid WAV header")
	}
	if string(data[12:16]) != "fmt " {
		return nil, errors.New("WAV fmt chunk is required")
	}
	channels := binary.LittleEndian.Uint16(data[22:24])
	rate := binary.LittleEndian.Uint32(data[24:28])
	bits := binary.LittleEndian.Uint16(data[34:36])
	if channels != 1 || rate != 16000 || bits != 16 {
		return nil, errors.New("WAV must be 16kHz mono 16-bit")
	}
	return append([]byte(nil), data[44:]...), nil
}

// RunREPL reads one text command per line and sends it as a message.
func (r *InputRunner) RunREPL(ctx context.Context, input io.Reader, output io.Writer) error {
	if r.Sender == nil {
		return errors.New("input sender is required")
	}
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if err := r.Sender.Send(ctx, text); err != nil {
			return err
		}
		if output != nil {
			_, _ = fmt.Fprintln(output, "sent")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	return nil
}
