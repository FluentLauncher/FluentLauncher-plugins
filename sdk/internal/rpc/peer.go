// Package rpc implements bounded, bidirectional JSON-RPC over length frames.
// The SDK maintains an independent standard-library copy of this wire peer.
package rpc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const MaxMessageBytes = 4 << 20
const maxPending = 32
const maxHandlers = 8

var ErrUnresponsive = errors.New("request cancellation was not acknowledged")

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}
type reply struct {
	result json.RawMessage
	err    error
}
type Handler func(context.Context, string, json.RawMessage) (any, error)

type Peer struct {
	input    io.Reader
	output   io.Writer
	prefix   string
	handle   Handler
	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan reply
	active   map[string]context.CancelFunc
	sequence uint64
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	err      error
	workers  sync.WaitGroup
	slots    chan struct{}
}

func New(input io.Reader, output io.Writer, prefix string, handle Handler) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Peer{input: input, output: output, prefix: prefix, handle: handle, pending: map[string]chan reply{}, active: map[string]context.CancelFunc{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), slots: make(chan struct{}, maxHandlers)}
	go p.readLoop()
	return p
}

func (p *Peer) Call(ctx context.Context, method string, params any, grace time.Duration) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.err != nil {
		err := p.err
		p.mu.Unlock()
		return nil, err
	}
	if len(p.pending) >= maxPending {
		p.mu.Unlock()
		return nil, &Error{Code: -32000, Message: "too many pending requests"}
	}
	p.sequence++
	id, _ := json.Marshal(fmt.Sprintf("%s%d", p.prefix, p.sequence))
	key := string(id)
	ch := make(chan reply, 1)
	p.pending[key] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, key); p.mu.Unlock() }()
	if err := p.write(message{JSONRPC: "2.0", ID: id, Method: method, Params: data}); err != nil {
		return nil, err
	}
	select {
	case result := <-ch:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return result.result, result.err
	case <-ctx.Done():
		cancel, _ := json.Marshal(struct {
			ID json.RawMessage `json:"id"`
		}{id})
		if err := p.write(message{JSONRPC: "2.0", Method: "$/cancelRequest", Params: cancel}); err != nil {
			return nil, err
		}
		if grace > 0 {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-ch:
			case <-p.done:
			case <-timer.C:
				return nil, ErrUnresponsive
			}
		}
		return nil, ctx.Err()
	case <-p.ctx.Done():
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		return nil, err
	}
}

func (p *Peer) readLoop() {
	defer close(p.done)
	for {
		payload, err := readFrame(p.input)
		if err != nil {
			p.Close(err)
			return
		}
		var msg message
		if err := json.Unmarshal(payload, &msg); err != nil {
			_ = p.respond(nil, nil, &Error{Code: -32700, Message: "invalid JSON"})
			continue
		}
		if msg.JSONRPC != "2.0" {
			p.Close(errors.New("invalid JSON-RPC version"))
			return
		}
		if msg.Method == "" {
			p.mu.Lock()
			ch := p.pending[string(msg.ID)]
			p.mu.Unlock()
			if ch != nil {
				var err error
				if msg.Error != nil {
					err = msg.Error
				}
				select {
				case ch <- reply{msg.Result, err}:
				default:
				}
			}
			continue
		}
		if msg.Method == "$/cancelRequest" {
			var params struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(msg.Params, &params) == nil {
				p.mu.Lock()
				cancel := p.active[string(params.ID)]
				p.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
			continue
		}
		if len(msg.ID) == 0 {
			continue
		} // No unsolicited methods in protocol v1.
		select {
		case p.slots <- struct{}{}:
		default:
			_ = p.respond(msg.ID, nil, &Error{Code: -32000, Message: "too many concurrent requests"})
			continue
		}
		ctx, cancel := context.WithCancel(p.ctx)
		key := string(msg.ID)
		p.mu.Lock()
		if _, exists := p.active[key]; exists {
			p.mu.Unlock()
			cancel()
			<-p.slots
			_ = p.respond(msg.ID, nil, &Error{Code: -32600, Message: "duplicate request ID"})
			continue
		}
		p.active[key] = cancel
		p.mu.Unlock()
		p.workers.Add(1)
		go func(msg message, key string) {
			defer p.workers.Done()
			defer cancel()
			defer func() { p.mu.Lock(); delete(p.active, key); p.mu.Unlock(); <-p.slots }()
			var result any
			var err error
			if p.handle == nil {
				err = &Error{Code: -32601, Message: "method not supported"}
			} else {
				result, err = p.handle(ctx, msg.Method, msg.Params)
			}
			if p.ctx.Err() != nil {
				return
			}
			_ = p.respond(msg.ID, result, err)
		}(msg, key)
	}
}

func (p *Peer) respond(id json.RawMessage, result any, err error) error {
	msg := message{JSONRPC: "2.0", ID: id}
	if err != nil {
		var rpcErr *Error
		if errors.As(err, &rpcErr) {
			msg.Error = rpcErr
		} else {
			msg.Error = &Error{Code: -32603, Message: err.Error()}
		}
	} else {
		msg.Result, _ = json.Marshal(result)
	}
	return p.write(msg)
}

func (p *Peer) write(msg message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(payload) > MaxMessageBytes {
		return errors.New("RPC frame exceeds 4 MiB")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.ctx.Err() != nil {
		return io.EOF
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := p.output.Write(header[:]); err != nil {
		return err
	}
	_, err = p.output.Write(payload)
	return err
}

// Close cancels this session immediately; the owner closes its pipes/process.
func (p *Peer) Close(err error) {
	if err == nil {
		err = io.EOF
	}
	p.mu.Lock()
	if p.err == nil {
		p.err = err
		p.cancel()
		for _, cancel := range p.active {
			cancel()
		}
	}
	p.mu.Unlock()
}

func (p *Peer) Wait() error {
	<-p.done
	p.workers.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if errors.Is(p.err, io.EOF) {
		return nil
	}
	return p.err
}

func readFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxMessageBytes {
		return nil, errors.New("invalid RPC frame size")
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(reader, payload)
	return payload, err
}
