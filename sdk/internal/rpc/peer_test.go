package rpc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestNestedCallsAndConcurrentResponses(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	left := New(a, a, "h:", func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]string{"value": "from host"}, nil
	})
	var right *Peer
	right = New(b, b, "p:", func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		result, err := right.Call(ctx, "host.context.get", nil, 0)
		return json.RawMessage(result), err
	})
	defer left.Close(io.EOF)
	defer right.Close(io.EOF)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := left.Call(ctx, "query", nil, 0)
			if err != nil || string(result) != `{"value":"from host"}` {
				t.Errorf("nested RPC: %s %v", result, err)
			}
		}()
	}
	workers.Wait()
}

func TestCancellationAndLateResponseDoNotPoisonNextCall(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	entered := make(chan struct{})
	right := New(b, b, "p:", func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "slow" {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return "healthy", nil
	})
	left := New(a, a, "h:", nil)
	defer left.Close(io.EOF)
	defer right.Close(io.EOF)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := left.Call(ctx, "slow", nil, 300*time.Millisecond); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := left.Call(ctx, "ping", nil, 0)
	if err != nil || string(result) != `"healthy"` {
		t.Fatalf("next RPC: %s %v", result, err)
	}
}

func TestClosingSessionCancelsPendingHostService(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	entered := make(chan struct{})
	exited := make(chan struct{})
	right := New(b, b, "p:", func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	})
	left := New(a, a, "h:", nil)
	done := make(chan error, 1)
	go func() { _, err := left.Call(context.Background(), "service", nil, 0); done <- err }()
	<-entered
	left.Close(io.EOF)
	right.Close(io.EOF)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("pending request did not stop")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("service context survived session exit")
	}
}

func TestOversizedFrameRejectedBeforeAllocation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	peer := New(a, a, "h:", nil)
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxMessageBytes+1)
	if _, err := b.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if err := peer.Wait(); err == nil {
		t.Fatal("oversized frame accepted")
	}
}
