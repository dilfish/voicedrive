package command

import (
	"context"
	"testing"
	"time"
)

func TestRouter(t *testing.T) {
	for _, tt := range []struct {
		input, wake string
		want        int
	}{{"建个群吧", "小牛", 0}, {" 小牛，建个群", "小牛", 1}, {"HeyBot LIST", "heybot", 1}, {"半句小牛建群", "小牛", 0}} {
		t.Run(tt.input, func(t *testing.T) {
			calls := 0
			r := New(tt.wake, func(context.Context, string) error { calls++; return nil }, nil, nil)
			if err := r.Input(context.Background(), tt.input); err != nil {
				t.Fatal(err)
			}
			if calls != tt.want {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestConfirmation(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		advance     time.Duration
		want        int
	}{{"match", "第一个", 0, 1}, {"reject", "不要", 0, 0}, {"miss", "乱说", 0, 0}, {"deadline", "第一个", 10 * time.Second, 0}, {"expired", "第一个", 11 * time.Second, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			calls := 0
			r := New("小牛", nil, func(context.Context, string, map[string]any) error { calls++; return nil }, func() time.Time { return now })
			if err := r.Ask("group.switch", map[string]any{"name": "夜班"}, "name", []string{"夜班组", "夜班群"}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(tt.advance)
			if err := r.Input(context.Background(), tt.input); err != nil {
				t.Fatal(err)
			}
			if calls != tt.want {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestExpireAndReplacement(t *testing.T) {
	now := time.Unix(100, 0)
	sent := ""
	r := New("小牛", func(_ context.Context, s string) error { sent = s; return nil }, nil, func() time.Time { return now })
	if err := r.Ask("group.switch", map[string]any{"name": "夜班"}, "name", []string{"夜班组"}); err != nil {
		t.Fatal(err)
	}
	if r.Expire() {
		t.Fatal("expired before deadline")
	}
	now = now.Add(10 * time.Second)
	if !r.Expire() || r.Expire() {
		t.Fatal("expiry must fire once")
	}
	if err := r.Input(context.Background(), "小牛查群"); err != nil {
		t.Fatal(err)
	}
	if sent != "查群" {
		t.Fatalf("sent=%q", sent)
	}
	if err := r.Ask("group.switch", nil, "name", []string{"a", "b", "c", "d"}); err == nil {
		t.Fatal("accepted too many options")
	}
}
