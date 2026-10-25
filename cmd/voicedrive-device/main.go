package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/dilfish/voicedrive/internal/device"
	"github.com/dilfish/voicedrive/internal/model"
)

func main() {
	endpoint := flag.String("server", "ws://localhost:8080/v1/device", "voicedrive control WebSocket endpoint")
	httpEndpoint := flag.String("http-server", "http://localhost:8080", "voicedrive HTTP endpoint")
	mac := flag.String("mac", "", "device MAC address")
	token := flag.String("token", "", "optional bearer token")
	groupID := flag.String("group", "", "group ID for text/audio input")
	say := flag.String("say", "", "send one text message and exit")
	inject := flag.String("inject", "", "inject a WAV file and send it")
	transcript := flag.String("transcript", "", "recognized text for -inject")
	repl := flag.Bool("repl", false, "read text messages from stdin")
	flag.Parse()
	if *mac == "" {
		log.Fatal("-mac is required")
	}
	client, err := device.NewClient(*endpoint, *mac)
	if err != nil {
		log.Fatal(err)
	}
	client.Token = *token
	inputConfig := device.InputConfig{Say: *say, Inject: *inject, Transcript: *transcript, REPL: *repl}
	if err := device.ValidateInput(inputConfig); err != nil {
		log.Fatal(err)
	}
	if (*say != "" || *inject != "" || *repl) && *groupID == "" {
		log.Fatal("-group is required for input")
	}
	player := &device.AFPlayer{}
	queue, err := device.NewPlaybackQueue(player, client.Ack)
	if err != nil {
		log.Fatal(err)
	}
	defer queue.Close()
	queue.OnOverflow = func(count int) {
		log.Printf("[%s] skipped %d queued messages", *mac, count)
	}
	buffer := device.NewUtteranceBuffer()
	uploader := &device.AudioUploader{Endpoint: *httpEndpoint, MAC: *mac}
	sender := &device.HTTPMessageSender{Endpoint: *httpEndpoint, GroupID: *groupID, MAC: *mac}
	connected := make(chan struct{})
	var connectedOnce sync.Once
	buffer.OnFallback = func(transcript string) { log.Printf("[%s] audio pairing fallback for %q", *mac, transcript) }
	uploader.OnRetry = func(attempt int, err error) { log.Printf("[%s] audio upload retry %d: %v", *mac, attempt, err) }
	client.OnBindMessage = func(ctx context.Context, messageID, text string) error {
		return buffer.BindAndUpload(ctx, uploader, messageID, text)
	}
	client.OnMessage = func(_ context.Context, message model.DeviceMessage) error {
		queue.Enqueue(message)
		return nil
	}
	client.OnGroupChanged = func(_ context.Context, group model.Group) error {
		queue.SetActiveGroup(group.ID)
		return nil
	}
	client.OnConnect = func() { log.Printf("[%s] control connected", *mac); connectedOnce.Do(func() { close(connected) }) }
	client.OnDisconnect = func(err error) { log.Printf("[%s] control disconnected: %v", *mac, err) }
	client.OnRetry = func(delay time.Duration, err error) {
		log.Printf("[%s] reconnect in %s after: %v", *mac, delay.String(), err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *say != "" || *inject != "" || *repl {
		go func() {
			select {
			case <-connected:
			case <-ctx.Done():
				return
			}
			runner := &device.InputRunner{Buffer: buffer, Sender: sender}
			var err error
			if *repl {
				err = runner.RunREPL(ctx, os.Stdin, os.Stdout)
			} else {
				err = runner.Run(ctx, inputConfig)
			}
			if err != nil {
				log.Printf("[%s] input: %v", *mac, err)
			}
		}()
	}
	if err := client.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
