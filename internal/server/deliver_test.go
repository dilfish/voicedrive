package server

import (
	"encoding/json"
	"testing"

	"github.com/dilfish/voicedrive/internal/model"
)

func TestEncodeFrame(t *testing.T) {
	tests := []struct {
		name  string
		frame deviceFrame
		want  string
	}{
		{
			name:  "played",
			frame: deviceFrame{Type: "played", GroupID: "g1", Seq: 7},
			want:  `{"type":"played","group_id":"g1","seq":7}`,
		},
		{
			name:  "group changed",
			frame: deviceFrame{Type: "group_changed", Group: &model.Group{ID: "g1", Name: "家庭群"}},
			want:  `{"type":"group_changed","group":{"id":"g1","name":"家庭群","owner_device_id":"","invite_code":"","next_seq":0,"created_at":"0001-01-01T00:00:00Z"}}`,
		},
		{
			name:  "message",
			frame: deviceFrame{Type: "message", Message: &model.DeviceMessage{Message: model.Message{ID: "m1", Seq: 3}, GroupName: "夜班组"}},
			want:  `{"type":"message","message":{"id":"m1","group_id":"","from_device_id":"","text":"","audio_url":"","duration_ms":0,"seq":3,"created_at":"0001-01-01T00:00:00Z","group_name":"夜班组"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encodeFrame(tt.frame)
			if err != nil {
				t.Fatalf("encodeFrame() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("encodeFrame() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDeviceFrameJSONRoundTrip(t *testing.T) {
	frame := deviceFrame{Type: "heartbeat", LastAckedSeq: 11}
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	var got deviceFrame
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if got != frame {
		t.Fatalf("round trip = %#v, want %#v", got, frame)
	}
}
