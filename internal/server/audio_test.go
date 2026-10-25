package server

import "testing"

func TestPCM事Duration(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ms   int
		ok   bool
	}{
		{name: "one second pcm", data: make([]byte, 32000), ms: 1000, ok: true},
		{name: "odd pcm", data: make([]byte, 3), ok: false},
		{name: "empty", data: nil, ok: false},
		{name: "wav wrong rate", data: wavHeader(8000, 1, 16, 16000), ok: false},
		{name: "wav one second", data: wavHeader(16000, 1, 16, 32000), ms: 1000, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMS, gotOK := pcmDuration(tt.data)
			if gotOK != tt.ok || gotMS != tt.ms {
				t.Fatalf("pcmDuration() = (%d, %v), want (%d, %v)", gotMS, gotOK, tt.ms, tt.ok)
			}
		})
	}
}

func wavHeader(rate, channels, bits, dataSize int) []byte {
	data := make([]byte, 44+dataSize)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	data[22] = byte(channels)
	data[24] = byte(rate)
	data[25] = byte(rate >> 8)
	data[26] = byte(rate >> 16)
	data[27] = byte(rate >> 24)
	data[34] = byte(bits)
	data[35] = byte(bits >> 8)
	return data
}
