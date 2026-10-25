package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Study-of-slag-weng/voicedrive1/internal/model"
)

func TestSilenceFallback(t *testing.T) {
	data, duration, err := (SilenceFallback{}).Generate(context.Background(), model.Message{})
	if err != nil || len(data) != 32000 || duration != 1000 {
		t.Fatalf("fallback = %d bytes, %d ms, %v", len(data), duration, err)
	}
	for _, sample := range data {
		if sample != 0 {
			t.Fatal("fallback is not silent")
		}
	}
}

func TestPCMToWAV(t *testing.T) {
	wav := pcmWAV([]byte{1, 2, 3, 4})
	if len(wav) != 48 || string(wav[:4]) != "RIFF" || string(wav[36:40]) != "data" {
		t.Fatalf("invalid WAV: %q", wav)
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.wav")
	if err := writeAtomic(path, []byte("audio")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "audio" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "audio.wav" {
		t.Fatalf("temporary files remain: %+v", entries)
	}
}
