package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/dilfish/voicedrive/internal/model"
	"github.com/dilfish/voicedrive/internal/store"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = time.Second
	pingPeriod     = 10 * time.Second
	connectionWait = 30 * time.Second
	maxPending     = 50
	messageBuffer  = 32
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

type deviceFrame struct {
	Type         string               `json:"type"`
	Message      *model.DeviceMessage `json:"message,omitempty"`
	MessageID    string               `json:"message_id,omitempty"`
	Text         string               `json:"text,omitempty"`
	Group        *model.Group         `json:"group,omitempty"`
	GroupID      string               `json:"group_id,omitempty"`
	Seq          int64                `json:"seq,omitempty"`
	LastAckedSeq int64                `json:"last_acked_seq,omitempty"`
}

func (h *deliveryHub) notifyBind(ctx context.Context, message model.Message) error {
	h.mu.RLock()
	client := h.clients[message.FromDeviceID]
	h.mu.RUnlock()
	if client == nil {
		return nil
	}
	if !h.enqueue(client, deviceFrame{Type: "bind_message", MessageID: message.ID, Text: message.Text}) {
		h.remove(client)
	}
	return nil
}

type deviceClient struct {
	deviceID string
	conn     *websocket.Conn
	send     chan deviceFrame
	done     chan struct{}
	closeOne sync.Once
}

func (c *deviceClient) close() {
	c.closeOne.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

type deliveryHub struct {
	store   *store.Postgres
	mu      sync.RWMutex
	clients map[string]*deviceClient
}

func newDeliveryHub(s *store.Postgres) *deliveryHub {
	return &deliveryHub{store: s, clients: make(map[string]*deviceClient)}
}

func (h *deliveryHub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	mac := r.Header.Get("X-VD-MAC")
	if mac == "" {
		mac = r.URL.Query().Get("mac")
	}
	if mac == "" {
		http.Error(w, "X-VD-MAC is required", http.StatusBadRequest)
		return
	}
	device, err := h.store.DeviceByMAC(r.Context(), mac)
	if err != nil {
		fail(w, err)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &deviceClient{deviceID: device.ID, conn: conn, send: make(chan deviceFrame, messageBuffer), done: make(chan struct{})}
	h.mu.Lock()
	previous := h.clients[client.deviceID]
	h.clients[client.deviceID] = client
	activeGroup, activeGroupErr := h.store.ActiveGroup(r.Context(), device.ID)
	if activeGroupErr != nil && !errors.Is(activeGroupErr, store.ErrNotFound) {
		delete(h.clients, client.deviceID)
		h.mu.Unlock()
		client.close()
		return
	}
	if activeGroupErr == nil && !h.enqueue(client, deviceFrame{Type: "group_changed", Group: &activeGroup}) {
		delete(h.clients, client.deviceID)
		h.mu.Unlock()
		client.close()
		return
	}
	pending, err := h.store.PendingMessages(r.Context(), device.ID, maxPending)
	if err != nil {
		if h.clients[client.deviceID] == client {
			delete(h.clients, client.deviceID)
		}
		h.mu.Unlock()
		client.close()
		return
	}
	for _, message := range pending {
		if !h.enqueue(client, deviceFrame{Type: "message", Message: &message}) {
			if h.clients[client.deviceID] == client {
				delete(h.clients, client.deviceID)
			}
			h.mu.Unlock()
			client.close()
			return
		}
	}
	h.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	go h.writePump(client)
	go h.readPump(client)
}

func (h *deliveryHub) remove(client *deviceClient) {
	h.mu.Lock()
	if h.clients[client.deviceID] == client {
		delete(h.clients, client.deviceID)
	}
	h.mu.Unlock()
	client.close()
}

func (h *deliveryHub) enqueue(client *deviceClient, frame deviceFrame) bool {
	select {
	case client.send <- frame:
		return true
	case <-client.done:
		return false
	default:
		return false
	}
}

func (h *deliveryHub) broadcast(ctx context.Context, message model.Message) error {
	memberIDs, err := h.store.MemberDeviceIDs(ctx, message.GroupID)
	if err != nil {
		return fmt.Errorf("find message recipients: %w", err)
	}
	deviceMessage := model.DeviceMessage{Message: message}
	h.mu.RLock()
	clients := make([]*deviceClient, 0, len(memberIDs))
	for _, deviceID := range memberIDs {
		if client, ok := h.clients[deviceID]; ok && client.deviceID != message.FromDeviceID {
			clients = append(clients, client)
		}
	}
	h.mu.RUnlock()
	for _, client := range clients {
		if !h.enqueue(client, deviceFrame{Type: "message", Message: &deviceMessage}) {
			h.remove(client)
		}
	}
	return nil
}

func (h *deliveryHub) notifyGroupChanged(ctx context.Context, mac string, group model.Group) error {
	device, err := h.store.DeviceByMAC(ctx, mac)
	if err != nil {
		return fmt.Errorf("find switched device: %w", err)
	}
	h.mu.RLock()
	client := h.clients[device.ID]
	h.mu.RUnlock()
	if client == nil {
		return nil
	}
	if !h.enqueue(client, deviceFrame{Type: "group_changed", Group: &group}) {
		h.remove(client)
	}
	return nil
}

func (h *deliveryHub) readPump(client *deviceClient) {
	defer h.remove(client)
	client.conn.SetReadLimit(1024)
	_ = client.conn.SetReadDeadline(time.Now().Add(connectionWait))
	client.conn.SetPongHandler(func(string) error {
		return client.conn.SetReadDeadline(time.Now().Add(connectionWait))
	})
	for {
		var frame deviceFrame
		if err := client.conn.ReadJSON(&frame); err != nil {
			return
		}
		switch frame.Type {
		case "played":
			if err := h.store.AckDevice(context.Background(), client.deviceID, frame.GroupID, frame.Seq); err != nil && !errors.Is(err, store.ErrNotFound) {
				return
			}
		case "heartbeat":
			if frame.GroupID == "" {
				return
			}
			if err := h.store.AckDevice(context.Background(), client.deviceID, frame.GroupID, frame.LastAckedSeq); err != nil && !errors.Is(err, store.ErrNotFound) {
				return
			}
		default:
			return
		}
	}
}

func (h *deliveryHub) writePump(client *deviceClient) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		h.remove(client)
	}()
	for {
		select {
		case frame := <-client.send:
			if err := client.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := client.conn.WriteJSON(frame); err != nil {
				return
			}
		case <-ticker.C:
			if err := client.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-client.done:
			return
		}
	}
}

func encodeFrame(frame deviceFrame) ([]byte, error) {
	data, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("encode device frame: %w", err)
	}
	return data, nil
}
