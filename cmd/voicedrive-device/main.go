package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dilfish/voicedrive/internal/device"
	"github.com/dilfish/voicedrive/internal/model"
)

func main() {
	endpoint := flag.String("server", "ws://localhost:8080/v1/device", "voicedrive control WebSocket endpoint")
	mac := flag.String("mac", "", "device MAC address")
	token := flag.String("token", "", "optional bearer token")
	flag.Parse()
	if *mac == "" {
		log.Fatal("-mac is required")
	}
	client, err := device.NewClient(*endpoint, *mac)
	if err != nil {
		log.Fatal(err)
	}
	client.Token = *token
	player := &device.AFPlayer{}
	queue, err := device.NewPlaybackQueue(player, client.Ack)
	if err != nil {
		log.Fatal(err)
	}
	defer queue.Close()
	queue.OnOverflow = func(count int) {
		log.Printf("[%s] skipped %d queued messages", *mac, count)
	}
	client.OnMessage = func(_ context.Context, message model.DeviceMessage) error {
		queue.Enqueue(message)
		return nil
	}
	client.OnGroupChanged = func(_ context.Context, group model.Group) error {
		queue.SetActiveGroup(group.ID)
		return nil
	}
	client.OnConnect = func() { log.Printf("[%s] control connected", *mac) }
	client.OnDisconnect = func(err error) { log.Printf("[%s] control disconnected: %v", *mac, err) }
	client.OnRetry = func(delay time.Duration, err error) {
		log.Printf("[%s] reconnect in %s after: %v", *mac, delay.String(), err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := client.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
