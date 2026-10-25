package device

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/dilfish/voicedrive/internal/model"
	"github.com/gorilla/websocket"
)

const (
	controlWriteWait            = time.Second
	controlPing                 = 10 * time.Second
	controlPongWait             = 30 * time.Second
	controlMaxBackoff           = 30 * time.Second
	controlMaxHeartbeatFailures = 3
)

// MessageHandler receives a message delivered to the device's active group.
type MessageHandler func(context.Context, model.DeviceMessage) error

// GroupChangedHandler receives a focus-group change notification.
type GroupChangedHandler func(context.Context, model.Group) error

// BindMessageHandler receives a request to upload the original audio for a message.
type BindMessageHandler func(context.Context, string, string) error

// Client maintains the device control-plane WebSocket connection.
type Client struct {
	Endpoint           string
	MAC                string
	HTTPClient         *http.Client
	OnMessage          MessageHandler
	OnGroupChanged     GroupChangedHandler
	OnBindMessage      BindMessageHandler
	Token              string
	OnConnect          func()
	OnDisconnect       func(error)
	OnRetry            func(time.Duration, error)
	OnHeartbeatFailure func(int, error)

	mu          sync.Mutex
	lastAcked   map[string]int64
	activeGroup string
	conn        *websocket.Conn
	writeMu     sync.Mutex
}

type frame struct {
	Type         string               `json:"type"`
	Message      *model.DeviceMessage `json:"message,omitempty"`
	MessageID    string               `json:"message_id,omitempty"`
	Text         string               `json:"text,omitempty"`
	Group        *model.Group         `json:"group,omitempty"`
	GroupID      string               `json:"group_id,omitempty"`
	Seq          int64                `json:"seq,omitempty"`
	LastAckedSeq int64                `json:"last_acked_seq,omitempty"`
}

// NewClient constructs a control-plane client for a registered device.
func NewClient(endpoint, mac string) (*Client, error) {
	if endpoint == "" {
		return nil, errors.New("control endpoint is required")
	}
	if mac == "" {
		return nil, errors.New("device MAC is required")
	}
	return &Client{Endpoint: endpoint, MAC: mac, HTTPClient: http.DefaultClient, lastAcked: make(map[string]int64)}, nil
}

// Run connects and reconnects until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if c.OnDisconnect != nil {
			c.OnDisconnect(err)
		}
		if c.OnRetry != nil {
			c.OnRetry(backoff, err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < controlMaxBackoff {
			backoff *= 2
			if backoff > controlMaxBackoff {
				backoff = controlMaxBackoff
			}
		}
	}
}

func (c *Client) connectAndServe(ctx context.Context) error {
	parsed, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("parse control endpoint: %w", err)
	}
	header := http.Header{"X-VD-MAC": []string{c.MAC}}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, parsed.String(), header)
	if err != nil {
		return fmt.Errorf("connect control websocket: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		_ = conn.Close()
	}()
	if c.OnConnect != nil {
		c.OnConnect()
	}
	if err := c.writeHeartbeat(); err != nil {
		return fmt.Errorf("send reconnect heartbeat: %w", err)
	}
	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-readDone:
		}
	}()
	defer close(readDone)
	_ = conn.SetReadDeadline(time.Now().Add(controlPongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(controlPongWait)) })
	go c.pingLoop(ctx, conn)
	for {
		var incoming frame
		if err := conn.ReadJSON(&incoming); err != nil {
			return fmt.Errorf("read control frame: %w", err)
		}
		switch incoming.Type {
		case "message":
			if incoming.Message == nil {
				return errors.New("message frame has no message")
			}
		case "group_changed":
			if incoming.Group == nil {
				return errors.New("group_changed frame has no group")
			}
		case "bind_message":
			if incoming.MessageID == "" {
				return errors.New("bind_message frame has no message ID")
			}
		default:
			continue
		}
		if err := c.handleFrame(ctx, incoming); err != nil {
			return err
		}
	}
}

func (c *Client) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(controlPing)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := c.writeHeartbeat()
			if err == nil {
				failures = 0
				continue
			}
			failures++
			if c.OnHeartbeatFailure != nil {
				c.OnHeartbeatFailure(failures, err)
			}
			if failures >= controlMaxHeartbeatFailures {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *Client) handleFrame(ctx context.Context, incoming frame) error {
	switch incoming.Type {
	case "message":
		c.mu.Lock()
		if c.activeGroup == "" {
			c.activeGroup = incoming.Message.GroupID
		}
		c.mu.Unlock()
		if c.OnMessage != nil {
			if err := c.OnMessage(ctx, *incoming.Message); err != nil {
				return fmt.Errorf("handle message: %w", err)
			}
		}
	case "group_changed":
		c.mu.Lock()
		c.activeGroup = incoming.Group.ID
		c.mu.Unlock()
		if err := c.writeHeartbeat(); err != nil {
			return fmt.Errorf("send initial heartbeat: %w", err)
		}
		if c.OnGroupChanged != nil {
			if err := c.OnGroupChanged(ctx, *incoming.Group); err != nil {
				return fmt.Errorf("handle group change: %w", err)
			}
		}
	case "bind_message":
		if c.OnBindMessage != nil {
			if err := c.OnBindMessage(ctx, incoming.MessageID, incoming.Text); err != nil {
				return fmt.Errorf("handle bind message: %w", err)
			}
		}
	}
	return nil
}

// Ack reports that a message has finished playing.
func (c *Client) Ack(groupID string, seq int64) error {
	if groupID == "" {
		return errors.New("group ID is required")
	}
	if seq < 0 {
		return errors.New("sequence must not be negative")
	}
	c.mu.Lock()
	if seq > c.lastAcked[groupID] {
		c.lastAcked[groupID] = seq
	}
	c.mu.Unlock()
	return c.write(frame{Type: "played", GroupID: groupID, Seq: seq})
}

func (c *Client) write(outgoing frame) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("control websocket is not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(controlWriteWait)); err != nil {
		return fmt.Errorf("set control write deadline: %w", err)
	}
	if err := conn.WriteJSON(outgoing); err != nil {
		return fmt.Errorf("write control frame: %w", err)
	}
	return nil
}

func (c *Client) writeHeartbeat() error {
	groupID := c.activeGroupID()
	if groupID == "" {
		return nil
	}
	return c.write(frame{Type: "heartbeat", GroupID: groupID, LastAckedSeq: c.activeAck()})
}

func (c *Client) activeGroupID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeGroup
}

func (c *Client) activeAck() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAcked[c.activeGroup]
}
