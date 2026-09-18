package dshstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// serveMux upgrades one fenced request and serves its logical streams. The
// upgrade happens only after the trust fence, so a foreign Origin or a
// non-loopback peer gets an ordinary 403 and never a socket.
func (h *Handler) serveMux(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProtocolError(w, http.StatusMethodNotAllowed, nil, codeBadRequest, "the stream mux requires GET")
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		writeProtocolError(w, http.StatusBadRequest, nil, codeBadRequest, "the stream mux requires a WebSocket upgrade")
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response; there is no socket.
		return
	}
	h.serveConnection(r.Context(), conn)
}

// serveConnection owns one physical WebSocket generation: the read loop, the
// heartbeat, every logical stream it opened, and their clean shutdown.
func (h *Handler) serveConnection(parent context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(parent)
	active := &muxConnection{
		handler: h,
		conn:    conn,
		ctx:     ctx,
		cancel:  cancel,
		streams: make(map[string]context.CancelFunc),
	}
	active.run()
}

// muxConnection is one live WebSocket and the logical streams opened on it.
type muxConnection struct {
	handler *Handler
	conn    *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc

	writeMu sync.Mutex

	streamsMu sync.Mutex
	streams   map[string]context.CancelFunc
	wg        sync.WaitGroup
}

// run reads client frames until the connection ends, then cancels every
// logical stream and waits for its goroutine. The wait is what makes a client
// that disconnects mid-stream leave nothing behind.
func (c *muxConnection) run() {
	pingDone := make(chan struct{})
	defer func() {
		// LIFO: close(pingDone) stops the heartbeat, then cancel stops the
		// streams, then the wait proves they returned, then the socket closes.
		c.cancel()
		c.wg.Wait()
		_ = c.conn.Close()
	}()
	defer close(pingDone)

	heartbeat := c.handler.cfg.HeartbeatInterval
	pongWait := c.handler.cfg.pongWait()
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	go c.heartbeat(pingDone, heartbeat)

	for {
		messageType, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			c.closeWith(websocket.CloseUnsupportedData, "text messages required")
			return
		}
		message, problem := parseClientMessage(data)
		if problem != nil {
			// Upstream closes the physical generation on a message it cannot
			// parse (stream-server.ts receive(): close 1008), because the
			// streamId a reply would name is not trustworthy.
			c.closeWith(websocket.ClosePolicyViolation, "invalid Remote stream request")
			return
		}
		switch message.messageType {
		case "cancel":
			c.cancelStream(message.streamID)
		case "open":
			if !c.openStream(message) {
				c.closeWith(websocket.ClosePolicyViolation, "invalid Remote stream request")
				return
			}
		}
	}
}

// heartbeat sends one Ping per interval until the connection ends. WriteControl
// may be called concurrently with every other write, so it needs no lock.
func (c *muxConnection) heartbeat(done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			deadline := time.Now().Add(c.handler.cfg.WriteTimeout)
			if err := c.conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				return
			}
		}
	}
}

// openStream starts one logical stream under a child context. It returns false
// when the streamId is already open, which upstream treats as a protocol
// failure of the whole generation (stream-server.ts receive()).
func (c *muxConnection) openStream(message clientMessage) bool {
	c.streamsMu.Lock()
	if _, exists := c.streams[message.streamID]; exists {
		c.streamsMu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.streams[message.streamID] = cancel
	c.streamsMu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.streamsMu.Lock()
			delete(c.streams, message.streamID)
			c.streamsMu.Unlock()
			cancel()
		}()
		c.pump(ctx, message)
	}()
	return true
}

// cancelStream cancels one logical stream without ending the physical socket.
func (c *muxConnection) cancelStream(streamID string) {
	c.streamsMu.Lock()
	cancel, ok := c.streams[streamID]
	c.streamsMu.Unlock()
	if ok {
		cancel()
	}
}

// pump runs one stream to completion and writes its terminal frame. A stream
// whose context was cancelled writes nothing more: the client asked for it, so
// an end or error frame would be noise.
func (c *muxConnection) pump(ctx context.Context, message clientMessage) {
	err := c.handler.runStream(ctx, message.endpoint, message.payload, func(value any) error {
		return c.send(serverFrame{Type: "item", StreamID: message.streamID, Value: value})
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		var failure *streamError
		if errors.As(err, &failure) {
			_ = c.send(serverFrame{Type: "error", StreamID: message.streamID, Error: failure.wire()})
			return
		}
		_ = c.send(serverFrame{
			Type:     "error",
			StreamID: message.streamID,
			Error:    &wireError{Code: codeInternal, Message: err.Error(), Details: map[string]any{}},
		})
		return
	}
	_ = c.send(serverFrame{Type: "end", StreamID: message.streamID})
}

// send writes one text frame. Writes are serialized because gorilla permits
// only one concurrent writer, and a write deadline bounds a dead peer so a
// stream goroutine cannot be pinned by the kernel send buffer.
func (c *muxConnection) send(frame serverFrame) error {
	encoded, err := marshalFrame(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.handler.cfg.WriteTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, encoded)
}

// closeWith sends a close control frame and lets run() finish the shutdown.
func (c *muxConnection) closeWith(code int, reason string) {
	deadline := time.Now().Add(c.handler.cfg.WriteTimeout)
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), deadline)
}

// clientMessage is one validated browser-to-host mux frame. Upstream's
// parseRemoteStreamClientMessage accepts only:
//
//	{type:"open",   streamId, endpoint, payload}   (exact keys)
//	{type:"cancel", streamId}                      (exact keys)
//
// with a non-empty streamId and, for open, a non-empty endpoint.
type clientMessage struct {
	messageType string
	streamID    string
	endpoint    string
	payload     json.RawMessage
}

// parseClientMessage decodes and validates one text message. A non-nil problem
// means the frame is not a legal client message, which closes the physical
// generation.
func parseClientMessage(data []byte) (clientMessage, error) {
	object, err := decodeJSONObject(data)
	if err != nil {
		return clientMessage{}, err
	}
	rawType, ok := object["type"]
	if !ok {
		return clientMessage{}, errors.New("missing type")
	}
	var messageType string
	if err := json.Unmarshal(rawType, &messageType); err != nil {
		return clientMessage{}, errors.New("type must be a string")
	}
	switch messageType {
	case "cancel":
		if len(object) != 2 {
			return clientMessage{}, errors.New("cancel accepts only type and streamId")
		}
		streamID, err := nonEmptyString(object, "streamId")
		if err != nil {
			return clientMessage{}, err
		}
		return clientMessage{messageType: "cancel", streamID: streamID}, nil
	case "open":
		if len(object) != 4 {
			return clientMessage{}, errors.New("open accepts only type, streamId, endpoint, and payload")
		}
		streamID, err := nonEmptyString(object, "streamId")
		if err != nil {
			return clientMessage{}, err
		}
		endpoint, err := nonEmptyString(object, "endpoint")
		if err != nil {
			return clientMessage{}, err
		}
		rawPayload, ok := object["payload"]
		if !ok {
			return clientMessage{}, errors.New("open is missing payload")
		}
		return clientMessage{messageType: "open", streamID: streamID, endpoint: endpoint, payload: rawPayload}, nil
	default:
		return clientMessage{}, errors.New("unknown message type")
	}
}

// nonEmptyString reads a required, non-empty string field.
func nonEmptyString(object map[string]json.RawMessage, key string) (string, error) {
	raw, ok := object[key]
	if !ok {
		return "", errors.New("missing " + key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", errors.New(key + " must be a non-empty string")
	}
	return value, nil
}
