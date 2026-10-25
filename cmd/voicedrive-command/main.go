// voicedrive-command is the V2 command-channel companion to voicedrive-device.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dilfish/voicedrive/internal/commandapp"
)

func run(ctx context.Context, args []string, input io.ReadCloser, output io.Writer) int {
	f := flag.NewFlagSet("voicedrive-command", flag.ContinueOnError)
	f.SetOutput(output)
	mac := f.String("mac", "", "bound device MAC (required)")
	server := f.String("server", "http://127.0.0.1:8080", "V2 voicedrive HTTP API")
	url := f.String("lingx-url", "wss://xrobo-io.qiniuapi.com/v1/ws/", "platform WS endpoint")
	wake := f.String("wake-word", "小牛", "local command prefix")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if *mac == "" || *wake == "" || f.NArg() != 0 {
		fmt.Fprintln(output, "-mac and nonempty -wake-word required")
		return 2
	}
	token := os.Getenv("LINX_TOKEN")
	if token == "" {
		fmt.Fprintln(output, "configure LINX_TOKEN in your local environment before connecting; do not print or commit it")
		return 2
	}
	a, err := commandapp.New(commandapp.Config{URL: *url, Server: *server, MAC: *mac, Token: token, WakeWord: *wake, Output: output})
	if err != nil {
		fmt.Fprintln(output, "invalid command configuration")
		return 2
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	readyCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	err = a.WaitReady(readyCtx)
	stop()
	if err != nil {
		cancel()
		<-done
		fmt.Fprintln(output, "platform handshake failed")
		return 1
	}
	lines := make(chan string)
	readDone := make(chan error, 1)
	readerStopped := make(chan struct{})
	go func() {
		defer close(readerStopped)
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				readDone <- nil
				return
			}
		}
		readDone <- scanner.Err()
	}()
	defer func() {
		cancel()
		if err := input.Close(); err != nil {
			fmt.Fprintln(output, "input close failed")
		}
		<-readerStopped
	}()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return 0
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				fmt.Fprintln(output, "platform session failed")
				return 1
			}
			return 0
		case err := <-readDone:
			cancel()
			<-done
			if err != nil {
				fmt.Fprintln(output, "input read failed")
				return 1
			}
			return 0
		case line := <-lines:
			if err := a.Input(ctx, line); err != nil {
				cancel()
				<-done
				fmt.Fprintln(output, "command failed")
				return 1
			}
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout)
	stop()
	os.Exit(code)
}
