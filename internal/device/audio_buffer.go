package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	pcmBytesPerSecond = 16000 * 2
	bufferWindow      = 60 * time.Second
)

type utterance struct {
	ID         string
	At         time.Time
	PCM        []byte
	Transcript string
}

// UtteranceBuffer keeps a bounded set of recently completed speech segments.
type UtteranceBuffer struct {
	mu         sync.Mutex
	items      []utterance
	limit      int
	fallbacks  uint64
	OnFallback func(string)
}

// NewUtteranceBuffer creates a buffer with a 60 second PCM retention window.
func NewUtteranceBuffer() *UtteranceBuffer {
	return &UtteranceBuffer{limit: pcmBytesPerSecond * int(bufferWindow/time.Second)}
}

// Add stores a completed PCM utterance. The buffer evicts the oldest data over 60 seconds.
func (b *UtteranceBuffer) Add(id, transcript string, pcm []byte, at time.Time) error {
	if id == "" {
		return errors.New("utterance ID is required")
	}
	if len(pcm) == 0 || len(pcm)%2 != 0 {
		return errors.New("utterance PCM must be non-empty 16-bit samples")
	}
	if len(pcm) > b.limit {
		return errors.New("utterance exceeds 60 second buffer capacity")
	}
	copyPCM := append([]byte(nil), pcm...)
	b.mu.Lock()
	b.items = append(b.items, utterance{ID: id, At: at, PCM: copyPCM, Transcript: transcript})
	total := 0
	start := 0
	for i := len(b.items) - 1; i >= 0; i-- {
		total += len(b.items[i].PCM)
		if total > b.limit {
			start = i + 1
			break
		}
	}
	if start > 0 {
		b.items = append([]utterance(nil), b.items[start:]...)
	}
	b.mu.Unlock()
	return nil
}

// Bind finds the utterance matching transcript and removes it from the buffer.
// It retries after punctuation normalization and then falls back to the newest utterance.
func (b *UtteranceBuffer) Bind(transcript string) (string, []byte, bool) {
	b.mu.Lock()
	if len(b.items) == 0 {
		b.mu.Unlock()
		return "", nil, false
	}
	normalized := normalizeTranscript(transcript)
	index := -1
	for i := range b.items {
		if b.items[i].Transcript == transcript {
			index = i
			break
		}
	}
	if index < 0 && normalized != "" {
		for i := range b.items {
			if normalizeTranscript(b.items[i].Transcript) == normalized {
				index = i
				break
			}
		}
	}
	fallback := index < 0
	if index < 0 {
		index = len(b.items) - 1
		b.fallbacks++
	}
	item := b.items[index]
	copy(b.items[index:], b.items[index+1:])
	b.items[len(b.items)-1] = utterance{}
	b.items = b.items[:len(b.items)-1]
	b.mu.Unlock()
	if fallback && b.OnFallback != nil {
		b.OnFallback(transcript)
	}
	return item.ID, append([]byte(nil), item.PCM...), true
}

// FallbackCount returns the number of binds that used the newest utterance.
func (b *UtteranceBuffer) FallbackCount() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fallbacks
}

// SetLatestTranscript attaches an STT result to the latest completed utterance.
func (b *UtteranceBuffer) SetLatestTranscript(text string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return false
	}
	b.items[len(b.items)-1].Transcript = text
	return true
}

func normalizeTranscript(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// AudioUploader uploads raw 16 kHz mono s16le PCM to the message audio endpoint.
type AudioUploader struct {
	Endpoint   string
	MAC        string
	HTTPClient *http.Client
	Sleeper    func(time.Duration)
	OnRetry    func(int, error)
}

// Upload sends PCM and retries failures twice after 300ms and 1s.
func (u *AudioUploader) Upload(ctx context.Context, messageID string, pcm []byte) error {
	if u.Endpoint == "" || u.MAC == "" || messageID == "" {
		return errors.New("upload endpoint, MAC, and message ID are required")
	}
	if len(pcm) == 0 || len(pcm)%2 != 0 {
		return errors.New("audio must be non-empty 16-bit PCM")
	}
	client := u.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	sleep := u.Sleeper
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.Endpoint, "/")+"/v1/messages/"+messageID+"/audio", bytes.NewReader(pcm))
		if err != nil {
			return fmt.Errorf("create audio upload request: %w", err)
		}
		request.Header.Set("X-VD-MAC", u.MAC)
		request.Header.Set("Content-Type", "audio/pcm")
		response, err := client.Do(request)
		if err == nil {
			bodyErr := error(nil)
			if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
				if readErr != nil {
					bodyErr = fmt.Errorf("read upload error: %w", readErr)
				} else {
					bodyErr = fmt.Errorf("audio upload returned %s: %s", response.Status, strings.TrimSpace(string(body)))
				}
			}
			closeErr := response.Body.Close()
			if bodyErr == nil && closeErr == nil {
				return nil
			}
			if bodyErr != nil {
				lastErr = bodyErr
			} else {
				lastErr = fmt.Errorf("close audio upload response: %w", closeErr)
			}
		} else {
			lastErr = fmt.Errorf("upload audio: %w", err)
		}
		if attempt < 2 {
			if u.OnRetry != nil {
				u.OnRetry(attempt+1, lastErr)
			}
			delay := []time.Duration{300 * time.Millisecond, time.Second}[attempt]
			if sleep != nil {
				sleep(delay)
			} else {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				}
			}
		}
	}
	return lastErr
}

// BindAndUpload pairs a bind request with the buffered utterance and uploads it.
func (b *UtteranceBuffer) BindAndUpload(ctx context.Context, uploader *AudioUploader, messageID, transcript string) error {
	if uploader == nil {
		return errors.New("audio uploader is required")
	}
	_, pcm, ok := b.Bind(transcript)
	if !ok {
		return errors.New("no buffered utterance matches bind message")
	}
	if err := uploader.Upload(ctx, messageID, pcm); err != nil {
		return fmt.Errorf("bind and upload audio: %w", err)
	}
	return nil
}
