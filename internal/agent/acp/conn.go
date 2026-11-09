package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Transport is the newline-delimited JSON-RPC channel used by ACP agents.
type Transport interface {
	ReadMessage(ctx context.Context) (json.RawMessage, error)
	WriteMessage(ctx context.Context, msg json.RawMessage) error
	Close() error
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

const (
	jsonRPCVersion        = "2.0"
	errCodeMethodNotFound = -32601
	errCodeInternal       = -32603
)

// conn correlates JSON-RPC requests with their responses and dispatches
// notifications and agent-initiated requests.
type conn struct {
	transport Transport
	onNotify  func(ctx context.Context, method string, params json.RawMessage)
	onRequest func(ctx context.Context, id json.RawMessage, method string, params json.RawMessage)

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage
	writeMu   sync.Mutex
	closed    chan struct{}
	closeOnce sync.Once
}

func newConn(transport Transport,
	onNotify func(ctx context.Context, method string, params json.RawMessage),
	onRequest func(ctx context.Context, id json.RawMessage, method string, params json.RawMessage),
) *conn {
	return &conn{
		transport: transport,
		onNotify:  onNotify,
		onRequest: onRequest,
		pending:   map[string]chan rpcMessage{},
		closed:    make(chan struct{}),
	}
}

func (c *conn) start() {
	go func() {
		defer c.close()
		for {
			raw, err := c.transport.ReadMessage(context.Background())
			if err != nil {
				c.failPending(err)
				return
			}
			var msg rpcMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			switch {
			case msg.Method != "" && msg.ID != nil:
				if c.onRequest != nil {
					c.onRequest(context.Background(), msg.ID, msg.Method, msg.Params)
					continue
				}
				_ = c.respondError(context.Background(), msg.ID, errCodeMethodNotFound,
					"unsupported request: "+msg.Method)
			case msg.Method != "":
				if c.onNotify != nil {
					c.onNotify(context.Background(), msg.Method, msg.Params)
				}
			default:
				c.deliver(msg)
			}
		}
	}()
}

func (c *conn) deliver(msg rpcMessage) {
	key := string(msg.ID)
	c.pendingMu.Lock()
	ch := c.pending[key]
	delete(c.pending, key)
	c.pendingMu.Unlock()
	if ch == nil {
		return
	}
	ch <- msg
	close(ch)
}

func (c *conn) call(ctx context.Context, method string, params, target any) error {
	id := c.nextID.Add(1)
	idRaw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	paramsRaw, err := marshalRaw(params)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(rpcMessage{JSONRPC: jsonRPCVersion, ID: idRaw, Method: method, Params: paramsRaw})
	if err != nil {
		return err
	}

	ch := make(chan rpcMessage, 1)
	c.pendingMu.Lock()
	c.pending[string(idRaw)] = ch
	c.pendingMu.Unlock()

	if err := c.write(ctx, payload); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, string(idRaw))
		c.pendingMu.Unlock()
		return err
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if target != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, target)
		}
		return nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, string(idRaw))
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.closed:
		return io.EOF
	}
}

func (c *conn) notify(ctx context.Context, method string, params any) error {
	paramsRaw, err := marshalRaw(params)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(rpcMessage{JSONRPC: jsonRPCVersion, Method: method, Params: paramsRaw})
	if err != nil {
		return err
	}
	return c.write(ctx, payload)
}

func (c *conn) respond(ctx context.Context, id json.RawMessage, result any) error {
	resultRaw, err := marshalRaw(result)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(rpcMessage{JSONRPC: jsonRPCVersion, ID: id, Result: resultRaw})
	if err != nil {
		return err
	}
	return c.write(ctx, payload)
}

func (c *conn) respondError(ctx context.Context, id json.RawMessage, code int64, message string) error {
	payload, err := json.Marshal(rpcMessage{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	return c.write(ctx, payload)
}

func (c *conn) write(ctx context.Context, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.transport.WriteMessage(ctx, payload)
}

func (c *conn) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for key, ch := range c.pending {
		delete(c.pending, key)
		ch <- rpcMessage{Error: &rpcError{Code: errCodeInternal, Message: err.Error()}}
		close(ch)
	}
}

func (c *conn) close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.transport.Close()
	})
	return err
}

func marshalRaw(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func decodeRaw(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("empty payload")
	}
	return json.Unmarshal(raw, target)
}
