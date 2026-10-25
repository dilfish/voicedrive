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
	if strings.TrimSpace(text) == "" {
		return errors.New("input requires recognized text")
	}
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
	return DecodeWAV(data)
}

// DecodeWAV reads PCM chunks, including files with metadata or extended fmt chunks.
func DecodeWAV(data []byte) ([]byte, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("invalid WAV header")
	}
	end := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
	if end != uint64(len(data)) {
		return nil, errors.New("invalid WAV RIFF size")
	}
	var pcm []byte
	format := false
	for offset := 12; offset < len(data); {
		if len(data)-offset < 8 {
			return nil, errors.New("truncated WAV chunk")
		}
		size := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		if size > uint64(len(data)-start) {
			return nil, errors.New("truncated WAV chunk data")
		}
		chunk := data[start : start+int(size)]
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if len(chunk) < 16 || binary.LittleEndian.Uint16(chunk[:2]) != 1 || binary.LittleEndian.Uint16(chunk[2:4]) != 1 || binary.LittleEndian.Uint32(chunk[4:8]) != 16000 || binary.LittleEndian.Uint32(chunk[8:12]) != 32000 || binary.LittleEndian.Uint16(chunk[12:14]) != 2 || binary.LittleEndian.Uint16(chunk[14:16]) != 16 {
				return nil, errors.New("WAV must be PCM 16kHz mono 16-bit")
			}
			format = true
		case "data":
			pcm = append(pcm, chunk...)
		}
		offset = start + int(size) + int(size%2)
		if offset > len(data) {
			return nil, errors.New("missing WAV chunk padding")
		}
	}
	if !format || len(pcm) == 0 || len(pcm)%2 != 0 {
		return nil, errors.New("WAV requires fmt and non-empty aligned data")
	}
	return pcm, nil
}

// RunREPL reads one text command per line and sends it as a message.
func (r *InputRunner) RunREPL(ctx context.Context, input io.Reader, output io.Writer) error {
	if r.Sender == nil {
		return errors.New("input sender is required")
	}
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
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
