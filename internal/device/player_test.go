package device

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dilfish/voicedrive/internal/model"
)

type fakePlayer struct {
	mu           sync.Mutex
	played       []string
	started      chan struct{}
	continuePlay chan struct{}
}

func (p *fakePlayer) Play(_ context.Context, message model.DeviceMessage) error {
	p.mu.Lock()
	p.played = append(p.played, message.ID)
	p.mu.Unlock()
	if p.started != nil {
		p.started <- struct{}{}
	}
	if p.continuePlay != nil {
		<-p.continuePlay
	}
	return nil
}
func (p *fakePlayer) Stop() error { return nil }

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func TestPlaybackQueueOrdersAndAcks(t *testing.T) {
	player := &fakePlayer{}
	var mu sync.Mutex
	acks := []int64{}
	queue, err := NewPlaybackQueue(player, func(_ string, seq int64) error {
		mu.Lock()
		acks = append(acks, seq)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for i := int64(1); i <= 2; i++ {
		queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: string(rune('a' + i)), GroupID: "g1", Seq: i}})
	}
	waitFor(t, func() bool { player.mu.Lock(); defer player.mu.Unlock(); return len(player.played) == 2 })
	mu.Lock()
	defer mu.Unlock()
	if len(acks) != 2 || acks[0] != 1 || acks[1] != 2 {
		t.Fatalf("acks = %v", acks)
	}
}

func TestPlaybackQueueDropsOlderMessages(t *testing.T) {
	player := &fakePlayer{started: make(chan struct{}, 1), continuePlay: make(chan struct{})}
	queue, err := NewPlaybackQueue(player, func(string, int64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: "first", GroupID: "g1", Seq: 1}})
	<-player.started
	queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: "second", GroupID: "g1", Seq: 2}})
	queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: "third", GroupID: "g1", Seq: 3}})
	close(player.continuePlay)
	waitFor(t, func() bool { player.mu.Lock(); defer player.mu.Unlock(); return len(player.played) == 2 })
	player.mu.Lock()
	defer player.mu.Unlock()
	if player.played[1] != "third" {
		t.Fatalf("played = %v", player.played)
	}
}

func TestPlaybackQueueFocusAndTTSPause(t *testing.T) {
	player := &fakePlayer{}
	queue, err := NewPlaybackQueue(player, func(string, int64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	queue.SetActiveGroup("g1")
	queue.SetTTSSpeaking(true)
	queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: "old", GroupID: "g2", Seq: 1}})
	queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: "queued", GroupID: "g1", Seq: 2}})
	time.Sleep(20 * time.Millisecond)
	player.mu.Lock()
	if len(player.played) != 0 {
		t.Fatalf("played while TTS active: %v", player.played)
	}
	player.mu.Unlock()
	queue.SetTTSSpeaking(false)
	waitFor(t, func() bool { player.mu.Lock(); defer player.mu.Unlock(); return len(player.played) == 1 })
	queue.SetActiveGroup("g2")
	queue.Enqueue(model.DeviceMessage{Message: model.Message{ID: "new", GroupID: "g2", Seq: 1}})
	waitFor(t, func() bool { player.mu.Lock(); defer player.mu.Unlock(); return len(player.played) == 2 })
}

func TestPlaybackQueueValidation(t *testing.T) {
	if _, err := NewPlaybackQueue(nil, func(string, int64) error { return nil }); !errors.Is(err, errors.New("audio player is required")) {
		if err == nil {
			t.Fatal("expected player validation error")
		}
	}
	if _, err := NewPlaybackQueue(&fakePlayer{}, nil); err == nil {
		t.Fatal("expected ack validation error")
	}
}
