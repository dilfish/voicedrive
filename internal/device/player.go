package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"sync"

	"github.com/dilfish/voicedrive/internal/model"
)

var ErrPlaybackInterrupted = errors.New("playback interrupted")

// AudioPlayer plays one message audio stream at a time.
type AudioPlayer interface {
	Play(context.Context, model.DeviceMessage) error
	Stop() error
}

// PlaybackQueue serializes group message playback and reports completed messages.
type PlaybackQueue struct {
	player AudioPlayer
	ack    func(string, int64) error

	mu         sync.Mutex
	active     string
	paused     bool
	playing    bool
	items      []model.DeviceMessage
	dropped    int
	wake       chan struct{}
	stop       chan struct{}
	stopOnce   sync.Once
	done       chan struct{}
	OnOverflow func(int)
}

// NewPlaybackQueue creates a queue for the supplied player and ack callback.
func NewPlaybackQueue(player AudioPlayer, ack func(string, int64) error) (*PlaybackQueue, error) {
	if player == nil {
		return nil, errors.New("audio player is required")
	}
	if ack == nil {
		return nil, errors.New("ack callback is required")
	}
	queue := &PlaybackQueue{player: player, ack: ack, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go queue.run()
	return queue, nil
}

// Enqueue adds a message for the current focus group.
func (q *PlaybackQueue) Enqueue(message model.DeviceMessage) {
	q.mu.Lock()
	if q.active != "" && q.active != message.GroupID {
		q.mu.Unlock()
		return
	}
	if q.active == "" {
		q.active = message.GroupID
	}
	index := sort.Search(len(q.items), func(i int) bool { return q.items[i].Seq > message.Seq })
	q.items = append(q.items, model.DeviceMessage{})
	copy(q.items[index+1:], q.items[index:])
	q.items[index] = message
	dropped := 0
	if q.playing && len(q.items) >= 2 {
		dropped = len(q.items) - 1
		q.dropped += dropped
		q.items = q.items[len(q.items)-1:]
	} else if len(q.items) >= 3 {
		dropped = len(q.items) - 1
		q.dropped += dropped
		q.items = q.items[len(q.items)-1:]
	}
	q.mu.Unlock()
	if dropped > 0 && q.OnOverflow != nil {
		q.OnOverflow(dropped)
	}
	q.signal()
}

// SetActiveGroup switches the focus and discards queued messages from other groups.
func (q *PlaybackQueue) SetActiveGroup(groupID string) {
	q.mu.Lock()
	q.active = groupID
	q.items = nil
	q.dropped = 0
	q.mu.Unlock()
	q.signal()
}

// SetTTSSpeaking pauses new message playback until TTS ends.
func (q *PlaybackQueue) SetTTSSpeaking(speaking bool) {
	q.mu.Lock()
	q.paused = speaking
	q.mu.Unlock()
	if !speaking {
		q.signal()
	}
}

// Interrupt immediately stops local playback and keeps queued messages for later.
func (q *PlaybackQueue) Interrupt() error {
	if err := q.player.Stop(); err != nil {
		return fmt.Errorf("stop playback: %w", err)
	}
	return nil
}

// Close stops the queue worker and waits for it to exit.
func (q *PlaybackQueue) Close() error {
	if err := q.player.Stop(); err != nil {
		return fmt.Errorf("stop playback on close: %w", err)
	}
	q.stopOnce.Do(func() { close(q.stop) })
	<-q.done
	return nil
}

func (q *PlaybackQueue) run() {
	defer close(q.done)
	for {
		q.mu.Lock()
		if q.paused || q.playing || len(q.items) == 0 {
			q.mu.Unlock()
			select {
			case <-q.wake:
			case <-q.stop:
				return
			}
			continue
		}
		message := q.items[0]
		q.items = q.items[1:]
		q.playing = true
		q.mu.Unlock()

		err := q.player.Play(context.Background(), message)
		if err == nil {
			err = q.ack(message.GroupID, message.Seq)
		}
		q.mu.Lock()
		q.playing = false
		q.mu.Unlock()
		if err != nil && !errors.Is(err, ErrPlaybackInterrupted) {
			q.signal()
		}
	}
}

func (q *PlaybackQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// AFPlayer uses the host's default audio player for local demos.
type AFPlayer struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Play streams a WAV/AIFF-compatible local URL through the host player.
func (p *AFPlayer) Play(ctx context.Context, message model.DeviceMessage) error {
	if message.AudioURL == "" {
		return errors.New("message has no audio URL")
	}
	if runtime.GOOS != "darwin" {
		return errors.New("afplay is only available on macOS")
	}
	playCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
	}()
	audioPath := message.AudioURL
	cleanup := func() {}
	if parsedURL, err := url.Parse(message.AudioURL); err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") {
		response, err := http.Get(message.AudioURL)
		if err != nil {
			return fmt.Errorf("download audio: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("download audio: status %s", response.Status)
		}
		temporary, err := os.CreateTemp("", "voicedrive-audio-*")
		if err != nil {
			return fmt.Errorf("create audio temporary file: %w", err)
		}
		audioPath = temporary.Name()
		cleanup = func() { _ = os.Remove(audioPath) }
		defer cleanup()
		if _, err := io.Copy(temporary, response.Body); err != nil {
			_ = temporary.Close()
			return fmt.Errorf("save downloaded audio: %w", err)
		}
		if err := temporary.Close(); err != nil {
			return fmt.Errorf("close downloaded audio: %w", err)
		}
	}
	command := exec.CommandContext(playCtx, "afplay", audioPath)
	if err := command.Run(); err != nil {
		if playCtx.Err() != nil {
			return ErrPlaybackInterrupted
		}
		return fmt.Errorf("afplay: %w", err)
	}
	return nil
}

// Stop is intentionally unsupported by the stateless demo player.
func (p *AFPlayer) Stop() error {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
