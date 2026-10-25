# Lingxi client

`New(Config) (*Client, error)` validates configuration without dialing. Pass the WebSocket URL, device MAC and bearer token in `Config`; optional `OnMCP`, `OnEvent` and `OnAudio` callbacks receive MCP payloads, other JSON text events (including hello), and binary audio respectively. No credentials are logged, and transport errors omit URLs and server error text.

- `Run(ctx) error` runs exactly one connection lifecycle. It sends hello and receives messages until cancellation or failure. It never reconnects and cannot be called a second time; create a new client to reconnect.
- `WaitReady(ctx) error` waits for a hello containing a nonempty `session_id`. Calls may wait before `Run` starts.
- `Command(ctx, text) error` waits for readiness, then sends `listen/start/manual` and `listen/detect/text` using that session ID. The pair is one serialized write batch.
- `SendAudio(ctx, pcm) error` waits for readiness and sends exactly one 640-byte frame. PCM is 16 kHz, mono, signed 16-bit little-endian, 20 ms per frame. The hello uses the MVP protocol's `frame_size: 320` samples. The caller controls pacing and starts listening with `Command` before streaming.

All writes, including pong control frames and MCP replies, pass through one writer. Handshakes and individual write batches have a 10-second timeout; operation deadlines can shorten writes. Canceling `Run` closes idle reads and writes and joins internal goroutines. Canceling an in-flight write closes the connection because a partial write cannot safely be resumed. Idle reads have no timeout; use a deadline on `Run` or `WaitReady` when required.

Callbacks run synchronously on the receive loop, must return promptly, and must not call blocking client methods. `OnMCP` receives the run context and must honor cancellation. Its result is a complete JSON-RPC payload, wrapped by the client in `{"type":"mcp","payload":...}`; return nil for notifications to suppress replies. Retaining callback input buffers is supported. No goroutines are created for callbacks that might ignore cancellation.

## Test record

Tests use real local `httptest` WebSocket servers only. No external services or dependency downloads are needed.

1. Before implementation, `GOPROXY=off GOSUMDB=off go test ./internal/lingxi` failed to compile with undefined `Config`, `Client` and `New` as expected.
2. After implementation, `GOPROXY=off GOSUMDB=off go test -race ./internal/lingxi -count=10` passed.

Coverage includes handshake headers, exact hello configuration, session readiness, command frame ordering, MCP response envelopes and nil notification replies, upstream/downstream audio, concurrent writes, cancellation before readiness and within a callback, pong serialization, single-use lifecycle, and sanitization of URL, handshake and server-close errors.
