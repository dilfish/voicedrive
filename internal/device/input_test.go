package device

import (
	"context"
	"strings"
	"testing"
)

type fakeSender struct{ messages []string }

func (s *fakeSender) Send(_ context.Context, text string) error {
	s.messages = append(s.messages, text)
	return nil
}

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name    string
		config  InputConfig
		wantErr bool
	}{
		{"say inject", InputConfig{Say: "x", Inject: "x.wav"}, true},
		{"repl say", InputConfig{REPL: true, Say: "x"}, true},
		{"say", InputConfig{Say: "x"}, false},
		{"inject", InputConfig{Inject: "x.wav"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if (ValidateInput(tt.config) != nil) != tt.wantErr {
				t.Fatal("unexpected validation result")
			}
		})
	}
}

func TestInputRunnerSayAndREPL(t *testing.T) {
	sender := &fakeSender{}
	runner := &InputRunner{Buffer: NewUtteranceBuffer(), Sender: sender}
	if err := runner.Run(context.Background(), InputConfig{Say: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunREPL(context.Background(), strings.NewReader("one\n\ntwo\n"), nil); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 3 || sender.messages[0] != "hello" || sender.messages[2] != "two" {
		t.Fatalf("messages = %#v", sender.messages)
	}
}
