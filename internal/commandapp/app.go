// Package commandapp assembles the real local router, platform transport and HTTP tool bridge.
package commandapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dilfish/voicedrive/internal/command"
	"github.com/dilfish/voicedrive/internal/grouptools"
	"github.com/dilfish/voicedrive/internal/lingxi"
)

// Config injects both endpoints without changing the existing control-plane client.
type Config struct {
	URL, Server, MAC, Token, WakeWord string
	HTTPClient                        *http.Client
	Output                            io.Writer
}

// App owns a single command session and serialized local feedback.
type App struct {
	client *lingxi.Client
	router *command.Router
	mu     sync.Mutex
	output io.Writer
}

// New assembles dependencies before any goroutine is started.
func New(config Config) (*App, error) {
	if config.Output == nil {
		config.Output = io.Discard
	}
	a := &App{output: config.Output}
	d, err := grouptools.New(grouptools.Config{BaseURL: config.Server, MAC: config.MAC, HTTPClient: config.HTTPClient, OnResult: func(name string, args map[string]any, response grouptools.Response) {
		a.feedback(response.Message)
		if response.Status == "clarify" {
			// Current backend options are group names. Person/invite ambiguity needs a
			// typed disambiguation contract rather than guessing a field to mutate.
			field := "name"
			switch name {
			case "group.pat":
				field = "alias"
			case "group.query_member", "group.invite":
				a.feedback("消歧接口待对齐，操作未执行")
				return
			}
			if err := a.router.Ask(name, args, field, response.Options); err != nil {
				a.feedback("无法确认，操作未执行")
			}
		}
	}})
	if err != nil {
		return nil, fmt.Errorf("tool bridge: %w", err)
	}
	c, err := lingxi.New(lingxi.Config{URL: config.URL, MAC: config.MAC, Token: config.Token, OnMCP: d.Handle})
	if err != nil {
		return nil, fmt.Errorf("platform configuration: %w", err)
	}
	a.client = c
	a.router = command.New(config.WakeWord, c.Command, func(ctx context.Context, name string, args map[string]any) error {
		_, err := d.Call(ctx, name, args)
		if err != nil {
			return fmt.Errorf("confirmed call: %w", err)
		}
		return nil
	}, nil)
	return a, nil
}

func (a *App) feedback(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := fmt.Fprintln(a.output, text); err != nil {
		return
	}
}

// Run owns the transport lifecycle and the confirmation expiration ticker.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.router.Expire() {
					a.feedback("先不管了，需要再说")
				}
			}
		}
	}()
	err := a.client.Run(ctx)
	cancel()
	<-done
	if err != nil {
		return fmt.Errorf("platform session ended: %w", err)
	}
	return nil
}

// WaitReady waits for the platform hello/session handshake.
func (a *App) WaitReady(ctx context.Context) error {
	if err := a.client.WaitReady(ctx); err != nil {
		return fmt.Errorf("wait for platform: %w", err)
	}
	return nil
}

// Input routes text locally before sending any platform traffic.
func (a *App) Input(ctx context.Context, text string) error {
	if err := a.router.Input(ctx, text); err != nil {
		return fmt.Errorf("route input: %w", err)
	}
	return nil
}
