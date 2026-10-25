package device

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestExactMatchBeforeNormalizedAndFallbackCount(t *testing.T) {
	b := NewUtteranceBuffer()
	_ = b.Add("normalized", "hello!", []byte{1, 2}, time.Now())
	_ = b.Add("exact", "hello", []byte{3, 4}, time.Now())
	if id, _, _ := b.Bind("hello"); id != "exact" {
		t.Fatalf("bound %s", id)
	}
	b.OnFallback = func(string) {
		if b.FallbackCount() != 1 {
			t.Error("missing fallback count")
		}
	}
	b.Bind("other")
	if err := b.Add("oversized", "", make([]byte, b.limit+2), time.Now()); err == nil {
		t.Fatal("accepted oversized utterance")
	}
}

func TestBindUploadsOriginalPCMAndLatestSTT(t *testing.T) {
	b := NewUtteranceBuffer()
	pcm := []byte{1, 2, 3, 4}
	_ = b.Add("first", "", pcm, time.Now())
	if !b.SetLatestTranscript("first") {
		t.Fatal("STT not attached")
	}
	_ = b.Add("second", "second", []byte{5, 6}, time.Now())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, pcm) {
			t.Errorf("uploaded %v", got)
		}
	}))
	defer srv.Close()
	if err := b.BindAndUpload(context.Background(), &AudioUploader{Endpoint: srv.URL, MAC: "test"}, "m1", "first"); err != nil {
		t.Fatal(err)
	}
}

func TestUploadCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "retry", 502) }))
	defer srv.Close()
	u := AudioUploader{Endpoint: srv.URL, MAC: "test", OnRetry: func(int, error) { cancel() }}
	started := time.Now()
	if err := u.Upload(ctx, "m1", []byte{1, 2}); err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("cancellation waited for retry")
	}
}

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
