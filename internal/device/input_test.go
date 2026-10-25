package device

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func TestDecodeWAVChunks(t *testing.T) {
	chunk := func(name string, payload []byte) []byte {
		data := make([]byte, 8+len(payload)+len(payload)%2)
		copy(data, name)
		binary.LittleEndian.PutUint32(data[4:8], uint32(len(payload)))
		copy(data[8:], payload)
		return data
	}
	format := make([]byte, 18)
	binary.LittleEndian.PutUint16(format, 1)
	binary.LittleEndian.PutUint16(format[2:], 1)
	binary.LittleEndian.PutUint32(format[4:], 16000)
	binary.LittleEndian.PutUint32(format[8:], 32000)
	binary.LittleEndian.PutUint16(format[12:], 2)
	binary.LittleEndian.PutUint16(format[14:], 16)
	pcm := []byte{1, 2, 3, 4}
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVE"), chunk("JUNK", []byte{1})...)
	wav = append(wav, chunk("fmt ", format)...)
	wav = append(wav, chunk("data", pcm)...)
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	got, err := DecodeWAV(wav)
	if err != nil || !bytes.Equal(got, pcm) {
		t.Fatalf("decoded %v: %v", got, err)
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }},
		{"float", func(b []byte) []byte { b[30] = 3; return b }},
		{"chunk size", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[16:20], 100000); return b }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeWAV(test.mutate(append([]byte(nil), wav...))); err == nil {
				t.Fatal("accepted malformed WAV")
			}
		})
	}
}

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
