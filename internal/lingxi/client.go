// Package lingxi implements the Lingxi device WebSocket protocol, including PCM
// audio and MCP envelopes. It performs no OTA discovery or automatic reconnects.
package lingxi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const ioTimeout = 10 * time.Second

// Config supplies the connection credentials and optional synchronous callbacks.
// Callbacks run on the receive loop, must return promptly, and must not call
// blocking client methods. OnMCP must honor its context; nil means no response
// (in particular for notifications). Callbacks may retain their input buffers.
type Config struct {
	URL     string
	MAC     string
	Token   string
	OnMCP   func(context.Context, json.RawMessage) json.RawMessage
	OnEvent func(json.RawMessage)
	OnAudio func([]byte)
}

// Client owns one connection lifecycle. Construct a new Client to reconnect.
// Its methods are safe for concurrent use; callbacks are invoked serially.
type Client struct {
	cfg      Config
	clientID string
	mu       sync.Mutex
	started  bool
	session  string
	err      error
	ready    chan struct{}
	done     chan struct{}
	writes   chan writeRequest
	pumpDone chan struct{}
}

type frame struct {
	kind int
	data []byte
}
type writeRequest struct {
	ctx    context.Context
	frames []frame
	result chan error
}

// New validates configuration without connecting. Errors never include URLs,
// credentials, or raw server error messages. Authentication is header-only.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u == nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("lingxi: invalid WebSocket URL")
	}
	if strings.TrimSpace(cfg.MAC) == "" || strings.TrimSpace(cfg.Token) == "" || strings.ContainsAny(cfg.MAC+cfg.Token, "\r\n") {
		return nil, errors.New("lingxi: invalid device credentials")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, errors.New("lingxi: cannot generate client ID")
	}
	id[6] = id[6]&15 | 64
	id[8] = id[8]&63 | 128
	return &Client{cfg: cfg, clientID: fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), ready: make(chan struct{}), done: make(chan struct{}), writes: make(chan writeRequest), pumpDone: make(chan struct{})}, nil
}

// Run connects once, sends hello, and receives until cancellation or an error.
// It may be called only once and never reconnects. Cancel ctx to close idle
// reads and pending writes. Handshake and individual writes time out after 10s;
// an idle read has no timeout. Run joins all internal goroutines before returning.
func (c *Client) Run(ctx context.Context) (result error) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("lingxi: Run already called")
	}
	c.started = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.err = result; close(c.done); c.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.cfg.Token)
	h.Set("Protocol-Version", "1")
	h.Set("Device-Id", c.cfg.MAC)
	h.Set("Client-Id", c.clientID)
	h.Set("User-Agent", "voicedrive-emu/0.1.0")
	dialer := websocket.Dialer{HandshakeTimeout: ioTimeout}
	conn, resp, err := dialer.DialContext(ctx, c.cfg.URL, h)
	if err != nil {
		if resp != nil && resp.Body != nil {
			closeBestEffort(resp.Body)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("lingxi: WebSocket handshake failed")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-runCtx.Done(); closeBestEffort(conn) }()
	go func() { defer wg.Done(); c.writePump(runCtx, conn) }()
	defer func() { cancel(); closeBestEffort(conn); wg.Wait() }()
	conn.SetReadLimit(4 << 20)
	// Override Gorilla's default control handlers: even pong frames use the pump.
	conn.SetPingHandler(func(data string) error { return c.send(runCtx, []frame{{websocket.PongMessage, []byte(data)}}) })
	conn.SetCloseHandler(func(int, string) error { return nil })
	hello := json.RawMessage(`{"type":"hello","version":1,"transport":"websocket","features":{"mcp":true},"audio_params":{"format":"pcm","sample_rate":16000,"channels":1,"bit_depth":16,"endianness":"little","frame_duration":20,"frame_size":320,"sample_format":"signed_int16","play_buffer_duration":300}}`)
	if err := c.send(runCtx, []frame{{websocket.TextMessage, hello}}); err != nil {
		return err
	}
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("lingxi: connection closed or read failed")
		}
		if kind == websocket.BinaryMessage {
			if c.cfg.OnAudio != nil {
				c.cfg.OnAudio(data)
			}
			continue
		}
		if kind != websocket.TextMessage {
			continue
		}
		var msg struct {
			Type      string          `json:"type"`
			SessionID string          `json:"session_id"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "hello":
			if msg.SessionID != "" {
				c.mu.Lock()
				if c.session == "" {
					c.session = msg.SessionID
					close(c.ready)
				}
				c.mu.Unlock()
			}
		case "mcp":
			if c.cfg.OnMCP != nil {
				payload := c.cfg.OnMCP(runCtx, msg.Payload)
				if payload != nil {
					raw, err := json.Marshal(struct {
						Type    string          `json:"type"`
						Payload json.RawMessage `json:"payload"`
					}{"mcp", payload})
					if err != nil {
						return errors.New("lingxi: invalid MCP response")
					}
					if err = c.send(runCtx, []frame{{websocket.TextMessage, raw}}); err != nil {
						return err
					}
				}
			}
			continue
		}
		if c.cfg.OnEvent != nil {
			c.cfg.OnEvent(data)
		}
	}
}

func (c *Client) writePump(ctx context.Context, conn *websocket.Conn) {
	defer close(c.pumpDone)
	defer closeBestEffort(conn)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-c.writes:
			if err := req.ctx.Err(); err != nil {
				req.result <- err
				continue
			}
			// Cancellation interrupts an in-flight write by closing this lifecycle.
			interrupted := make(chan struct{})
			stop := context.AfterFunc(req.ctx, func() { closeBestEffort(conn); close(interrupted) })
			deadline := time.Now().Add(ioTimeout)
			if d, ok := req.ctx.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
			var err error
			for _, f := range req.frames {
				if err = conn.SetWriteDeadline(deadline); err != nil {
					break
				}
				if err = conn.WriteMessage(f.kind, f.data); err != nil {
					break
				}
			}
			if !stop() {
				<-interrupted
			}
			if err != nil {
				err = errors.New("lingxi: write failed")
			}
			if req.ctx.Err() != nil {
				err = req.ctx.Err()
			}
			req.result <- err
			if err != nil {
				return
			}
		}
	}
}

func closeBestEffort(closer io.Closer) {
	if err := closer.Close(); err != nil {
		// Best effort: cancellation and cleanup can close the same connection
		// concurrently. Preserve the lifecycle error and never log raw network
		// errors, which may contain sensitive remote information.
		return
	}
}

func (c *Client) send(ctx context.Context, frames []frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req := writeRequest{ctx: ctx, frames: frames, result: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedError()
	case <-c.pumpDone:
		return errors.New("lingxi: writer stopped")
	case c.writes <- req:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedError()
	case <-c.pumpDone:
		return errors.New("lingxi: writer stopped")
	case err := <-req.result:
		return err
	}
}

func (c *Client) closedError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("lingxi: connection closed")
}

// WaitReady waits for a server hello containing a nonempty session_id. It can
// be called before Run; ctx bounds that wait. A closed connection is not ready.
func (c *Client) WaitReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return c.closedError()
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedError()
	case <-c.ready:
		select {
		case <-c.done:
			return c.closedError()
		default:
			return nil
		}
	}
}

// Command waits for readiness and sends listen start/manual followed by listen
// detect/text with the server session ID. The two frames form one write batch.
func (c *Client) Command(ctx context.Context, text string) error {
	if err := c.WaitReady(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	sid := c.session
	c.mu.Unlock()
	start, _ := json.Marshal(map[string]any{"type": "listen", "state": "start", "mode": "manual", "session_id": sid})
	detect, _ := json.Marshal(map[string]any{"type": "listen", "state": "detect", "text": text, "session_id": sid})
	return c.send(ctx, []frame{{websocket.TextMessage, start}, {websocket.TextMessage, detect}})
}

// SendAudio sends one 640-byte PCM frame (16kHz mono signed 16-bit little-endian,
// 20ms). The caller controls pacing and must start listening via Command first.
// It waits for readiness and copies data before submitting it to the write pump.
func (c *Client) SendAudio(ctx context.Context, data []byte) error {
	if len(data) != 640 {
		return errors.New("lingxi: PCM frame must contain 640 bytes")
	}
	if err := c.WaitReady(ctx); err != nil {
		return err
	}
	return c.send(ctx, []frame{{websocket.BinaryMessage, append([]byte(nil), data...)}})
}
