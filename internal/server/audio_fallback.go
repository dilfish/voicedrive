package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Study-of-slag-weng/voicedrive1/internal/model"
	"github.com/Study-of-slag-weng/voicedrive1/internal/store"
)

const audioFallbackDelay = 3 * time.Second

// AudioFallback creates an audio object when the sender did not upload the
// original PCM in time. The implementation is injected so production can use
// a TTS service while local tests use SilenceFallback.
type AudioFallback interface {
	Generate(context.Context, model.Message) (data []byte, durationMS int, err error)
}

// SilenceFallback is a deterministic local TTS stand-in: one second of valid
// 16 kHz mono s16le silence.
type SilenceFallback struct{}

func (SilenceFallback) Generate(context.Context, model.Message) ([]byte, int, error) {
	return make([]byte, 32000), 1000, nil
}

type fallbackScheduler struct {
	store     *store.Postgres
	hub       *deliveryHub
	dir       string
	generator AudioFallback
	delay     time.Duration
	mu        sync.Mutex
	pending   map[string]context.CancelFunc
	audioMu   *sync.Mutex
}

func newFallbackScheduler(s *store.Postgres, hub *deliveryHub, dir string, generator AudioFallback, audioMu *sync.Mutex) *fallbackScheduler {
	if generator == nil {
		generator = SilenceFallback{}
	}
	if audioMu == nil {
		audioMu = &sync.Mutex{}
	}
	return &fallbackScheduler{store: s, hub: hub, dir: dir, generator: generator, delay: audioFallbackDelay, pending: make(map[string]context.CancelFunc), audioMu: audioMu}
}

func (f *fallbackScheduler) schedule(parent context.Context, message model.Message) {
	ctx, cancel := context.WithCancel(parent)
	f.mu.Lock()
	f.pending[message.ID] = cancel
	f.mu.Unlock()
	go func() {
		defer func() { f.mu.Lock(); delete(f.pending, message.ID); f.mu.Unlock() }()
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		f.audioMu.Lock()
		defer f.audioMu.Unlock()
		data, duration, err := f.generator.Generate(ctx, message)
		if err != nil || len(data) == 0 {
			_ = f.store.SetMessageFailed(context.Background(), message.ID)
			return
		}
		if err := os.MkdirAll(f.dir, 0755); err != nil {
			_ = f.store.SetMessageFailed(context.Background(), message.ID)
			return
		}
		path := filepath.Join(f.dir, filepath.Base(message.ID)+".wav")
		if err := writeAtomic(path, pcmWAV(data)); err != nil {
			_ = f.store.SetMessageFailed(context.Background(), message.ID)
			return
		}
		ready, err := f.store.SetMessageFallback(ctx, message.ID, "/v1/messages/"+message.ID+"/audio", duration)
		if err != nil {
			if !errors.Is(err, store.ErrConflict) {
				_ = os.Remove(path)
			}
			return
		}
		if err := f.hub.broadcast(ctx, ready); err != nil {
			return
		}
	}()
}

func pcmWAV(pcm []byte) []byte {
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+len(pcm)))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], 16000)
	binary.LittleEndian.PutUint32(header[28:32], 32000)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(len(pcm)))
	return append(header, pcm...)
}

func (f *fallbackScheduler) cancel(messageID string) {
	f.mu.Lock()
	cancel := f.pending[messageID]
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func writeAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tts-*")
	if err != nil {
		return fmt.Errorf("create fallback audio: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write fallback audio: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close fallback audio: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish fallback audio: %w", err)
	}
	return nil
}
