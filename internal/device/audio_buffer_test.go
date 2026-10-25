package device

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestUtteranceBufferMatchesAndFallsBack(t *testing.T) {
	buffer := NewUtteranceBuffer()
	pcm := make([]byte, 320)
	if err := buffer.Add("first", "你好，收费站！", pcm, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Add("second", "第二句", pcm, time.Now()); err != nil {
		t.Fatal(err)
	}
	id, got, ok := buffer.Bind("你好收费站")
	if !ok || id != "first" || len(got) != len(pcm) {
		t.Fatalf("matched = %q, %v, %d", id, ok, len(got))
	}
	id, _, ok = buffer.Bind("unknown")
	if !ok || id != "second" {
		t.Fatalf("fallback = %q, %v", id, ok)
	}
}

func TestUtteranceBufferBindsFirstOfTwo(t *testing.T) {
	buffer := NewUtteranceBuffer()
	pcm1 := []byte{1, 2, 3, 4}
	pcm2 := []byte{5, 6, 7, 8}
	_ = buffer.Add("first", "first text", pcm1, time.Now())
	_ = buffer.Add("second", "second text", pcm2, time.Now().Add(5*time.Second))
	id, got, ok := buffer.Bind("first text")
	if !ok || id != "first" || string(got) != string(pcm1) {
		t.Fatalf("got %q %v %v", id, ok, got)
	}
	id, got, ok = buffer.Bind("second text")
	if !ok || id != "second" || string(got) != string(pcm2) {
		t.Fatalf("got %q %v %v", id, ok, got)
	}
}

func TestUtteranceBufferEvictsOldest(t *testing.T) {
	buffer := NewUtteranceBuffer()
	if err := buffer.Add("old", "old", make([]byte, buffer.limit), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Add("new", "new", make([]byte, 2), time.Now()); err != nil {
		t.Fatal(err)
	}
	if id, _, ok := buffer.Bind("old"); !ok || id != "new" {
		t.Fatalf("old utterance remained: %q, %v", id, ok)
	}
}

func TestAudioUploaderRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-VD-MAC") != "AA" {
			t.Errorf("missing MAC header")
		}
		if calls.Add(1) < 3 {
			http.Error(w, "retry", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	uploader := &AudioUploader{Endpoint: server.URL, MAC: "AA", Sleeper: func(time.Duration) {}}
	if err := uploader.Upload(context.Background(), "m1", make([]byte, 320)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}
