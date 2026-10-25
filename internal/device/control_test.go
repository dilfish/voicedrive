package device

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dilfish/voicedrive/internal/model"
	"github.com/gorilla/websocket"
)

func TestNewClientValidation(t *testing.T) {
	tests := []struct {
		name, endpoint, mac string
		wantErr             bool
	}{
		{name: "missing endpoint", mac: "AA", wantErr: true},
		{name: "missing mac", endpoint: "ws://localhost/v1/device", wantErr: true},
		{name: "valid", endpoint: "ws://localhost/v1/device", mac: "AA", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(tt.endpoint, tt.mac)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewClient() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClientReceivesGroupChangeAndMessage(t *testing.T) {
	groupChanged := make(chan model.Group, 1)
	messageReceived := make(chan model.DeviceMessage, 1)
	played := make(chan frame, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-VD-MAC") != "AA" {
			http.Error(w, "bad mac", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(frame{Type: "group_changed", Group: &model.Group{ID: "g1", Name: "家庭群"}}); err != nil {
			return
		}
		if err := conn.WriteJSON(frame{Type: "message", Message: &model.DeviceMessage{Message: model.Message{GroupID: "g1", Seq: 3}}}); err != nil {
			return
		}
		for range 3 {
			var got frame
			if err := conn.ReadJSON(&got); err != nil {
				return
			}
			if got.Type == "played" {
				played <- got
			}
		}
	}))
	defer server.Close()

	endpoint := "ws" + server.URL[len("http"):]
	client, err := NewClient(endpoint, "AA")
	if err != nil {
		t.Fatal(err)
	}
	client.OnGroupChanged = func(_ context.Context, group model.Group) error {
		groupChanged <- group
		return nil
	}
	client.OnMessage = func(_ context.Context, message model.DeviceMessage) error {
		messageReceived <- message
		return client.Ack(message.GroupID, message.Seq)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	select {
	case group := <-groupChanged:
		if group.ID != "g1" || group.Name != "家庭群" {
			t.Fatalf("unexpected group: %#v", group)
		}
	case <-ctx.Done():
		t.Fatal("group_changed callback was not called")
	}
	select {
	case message := <-messageReceived:
		if message.GroupID != "g1" || message.Seq != 3 {
			t.Fatalf("unexpected message: %#v", message)
		}
	case <-ctx.Done():
		t.Fatal("message callback was not called")
	}
	select {
	case got := <-played:
		if got.GroupID != "g1" || got.Seq != 3 {
			t.Fatalf("unexpected played frame: %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("played frame was not received")
	}
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientHandlesBindMessage(t *testing.T) {
	bound := make(chan [2]string, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(frame{Type: "bind_message", MessageID: "m1", Text: "hello"})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	client, err := NewClient("ws"+server.URL[len("http"):], "AA")
	if err != nil {
		t.Fatal(err)
	}
	client.OnBindMessage = func(_ context.Context, id, text string) error {
		bound <- [2]string{id, text}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()
	select {
	case got := <-bound:
		if got != [2]string{"m1", "hello"} {
			t.Fatalf("unexpected bind message: %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("bind_message callback was not called")
	}
}
