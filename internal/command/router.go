// Package command owns local routing and confirmation; it never guesses an intent.
package command

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

type pending struct {
	tool, field string
	args        map[string]any
	options     []string
	deadline    time.Time
}

// Router serializes input and timeout decisions. Callbacks run outside its lock.
type Router struct {
	mu      sync.Mutex
	wake    string
	send    func(context.Context, string) error
	replay  func(context.Context, string, map[string]any) error
	now     func() time.Time
	pending *pending
}

// New injects command transport, tool replay and a deterministic clock.
func New(wake string, send func(context.Context, string) error, replay func(context.Context, string, map[string]any) error, now func() time.Time) *Router {
	if now == nil {
		now = time.Now
	}
	return &Router{wake: wake, send: send, replay: replay, now: now}
}

// Ask stores only explicit backend options; unknown disambiguation fields are rejected.
func (r *Router) Ask(tool string, args map[string]any, field string, options []string) error {
	if len(options) == 0 || len(options) > 3 {
		return fmt.Errorf("confirmation requires 1..3 options")
	}
	if field != "name" && field != "alias" && field != "group_name" {
		return fmt.Errorf("unsupported disambiguation field")
	}
	copyArgs := map[string]any{}
	for key, value := range args {
		copyArgs[key] = value
	}
	for _, option := range options {
		if strings.TrimSpace(option) == "" {
			return fmt.Errorf("empty confirmation option")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = &pending{tool: tool, args: copyArgs, field: field, options: append([]string(nil), options...), deadline: r.now().Add(10 * time.Second)}
	return nil
}

// Expire discards stale parameters exactly at the deadline, without replaying them.
func (r *Router) Expire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil && !r.now().Before(r.pending.deadline) {
		r.pending = nil
		return true
	}
	return false
}

// Input consumes valid confirmation replies before checking the wake-word prefix.
func (r *Router) Input(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	r.mu.Lock()
	p := r.pending
	r.pending = nil
	if p != nil && r.now().Before(p.deadline) {
		if text == "不要" || text == "取消" {
			r.mu.Unlock()
			return nil
		}
		selected := ""
		for i, option := range p.options {
			if text == option || text == []string{"第一个", "第二个", "第三个"}[i] || (len(p.options) == 1 && text == "要") {
				selected = option
				break
			}
		}
		if selected != "" {
			p.args[p.field] = selected
			r.mu.Unlock()
			if r.replay == nil {
				return fmt.Errorf("tool replay is not configured")
			}
			if err := r.replay(ctx, p.tool, p.args); err != nil {
				return fmt.Errorf("replay tool: %w", err)
			}
			return nil
		}
	}
	r.mu.Unlock()
	word := []rune(r.wake)
	input := []rune(text)
	if len(word) == 0 || len(input) < len(word) || !strings.EqualFold(string(input[:len(word)]), r.wake) {
		return nil
	}
	rest := strings.TrimLeftFunc(string(input[len(word):]), func(c rune) bool { return unicode.IsSpace(c) || unicode.IsPunct(c) })
	if rest == "" {
		return nil
	}
	if r.send == nil {
		return fmt.Errorf("command transport is not configured")
	}
	if err := r.send(ctx, rest); err != nil {
		return fmt.Errorf("send command: %w", err)
	}
	return nil
}
