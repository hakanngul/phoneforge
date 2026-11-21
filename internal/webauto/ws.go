package webauto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Request is a CDP command frame.
type Request struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// Response is a CDP response or event frame.
type Response struct {
	ID     int64           `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// RPCError is a CDP protocol error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message) }

// EncodeRequest marshals a CDP command.
func EncodeRequest(id int64, method string, params any) ([]byte, error) {
	return json.Marshal(Request{ID: id, Method: method, Params: params})
}

// DecodeResponse parses a CDP frame.
func DecodeResponse(data []byte) (*Response, error) {
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ErrClosed is returned for calls on a closed client.
var ErrClosed = errors.New("webauto: connection closed")

// Client is a minimal CDP client over a WebSocket.
type Client struct {
	conn    *websocket.Conn
	nextID  atomic.Int64
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan *Response
	closed  bool
	err     error
	done    chan struct{}

	// OnEvent, if set before use, receives protocol events.
	OnEvent func(method string, params json.RawMessage)
}

// Dial connects to a CDP websocket URL.
func Dial(ctx context.Context, url string) (*Client, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second, ReadBufferSize: 1 << 16, WriteBufferSize: 1 << 16}
	conn, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}
	conn.SetReadLimit(256 << 20) // screenshots / big DOM
	c := &Client{conn: conn, pending: map[int64]chan *Response{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.fail(err)
			return
		}
		r, err := DecodeResponse(data)
		if err != nil {
			continue
		}
		if r.ID == 0 && r.Method != "" {
			if c.OnEvent != nil {
				c.OnEvent(r.Method, r.Params)
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// Call sends a command and waits for its result.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	data, err := EncodeRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan *Response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()

	c.writeMu.Lock()
	err = c.conn.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if r.Error != nil {
			return nil, r.Error
		}
		return r.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Closed reports whether the connection has terminated.
func (c *Client) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Close shuts down the connection.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	c.writeMu.Lock()
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	c.writeMu.Unlock()
	err := c.conn.Close()
	<-c.done
	return err
}
