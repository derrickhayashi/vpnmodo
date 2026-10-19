// Package rpcio adapts bounded protocol frames to the JSON-RPC library.
package rpcio

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
)

type Channel struct {
	stream  io.ReadWriteCloser
	reader  *protocol.FrameReader
	writer  *protocol.FrameWriter
	server  bool
	mu      sync.Mutex
	pending map[string]bool
	after   map[string]func()
	drained chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func New(stream io.ReadWriteCloser, server bool) *Channel {
	drained := make(chan struct{})
	close(drained)
	return &Channel{stream: stream, reader: protocol.NewFrameReader(stream), writer: protocol.NewFrameWriter(stream), server: server, pending: make(map[string]bool), after: map[string]func(){}, drained: drained, closed: make(chan struct{})}
}
func idKey(raw []byte) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return ""
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
func (c *Channel) Recv() ([]byte, error) {
	for {
		if conn, ok := c.stream.(net.Conn); ok {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		}
		data, err := c.reader.Read()
		if err != nil {
			if err == io.EOF && c.server {
				// Do not let the library cancel queued calls when a bridge half-closes
				// stdin. Drain their responses first; shutdown and stalled calls are bounded.
				c.mu.Lock()
				drained := c.drained
				c.mu.Unlock()
				timer := time.NewTimer(10 * time.Second)
				select {
				case <-drained:
				case <-c.closed:
				case <-timer.C:
				}
				timer.Stop()
			}
			return nil, err
		}
		if !c.server {
			return data, nil
		}
		req, err := protocol.DecodeRequest(data)
		if err != nil {
			code := protocol.CodeInvalidRequest
			if fault, ok := err.(*protocol.RPCError); ok {
				code = fault.Code
			}
			reply, _ := protocol.EncodeError(nil, code)
			if err := c.write(reply); err != nil {
				return nil, err
			}
			continue
		}
		if req.Notification() {
			continue
		}
		key := idKey(req.ID)
		c.mu.Lock()
		if len(c.pending) >= 64 {
			c.mu.Unlock()
			return nil, protocol.ErrFrame
		}
		if c.pending[key] {
			c.mu.Unlock()
			reply, _ := protocol.EncodeError(req.ID, protocol.CodeInvalidRequest)
			if err := c.write(reply); err != nil {
				return nil, err
			}
			continue
		}
		if len(c.pending) == 0 {
			c.drained = make(chan struct{})
		}
		c.pending[key] = true
		c.mu.Unlock()
		return data, nil
	}
}
func (c *Channel) write(data []byte) error {
	if conn, ok := c.stream.(net.Conn); ok {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	}
	return c.writer.Write(data)
}
func (c *Channel) Send(data []byte) error {
	if c.server {
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data, &envelope) == nil && len(envelope.ID) > 0 {
			key := idKey(envelope.ID)
			defer func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.pending[key] {
					delete(c.pending, key)
					if len(c.pending) == 0 {
						close(c.drained)
					}
				}
			}()
		}
	}
	err := c.write(data)
	if err == nil && c.server {
		var envelope struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			key := idKey(envelope.ID)
			c.mu.Lock()
			callback := c.after[key]
			delete(c.after, key)
			c.mu.Unlock()
			if callback != nil {
				go callback()
			}
		}
	}
	return err
}
func (c *Channel) Close() error {
	var err error
	c.once.Do(func() { close(c.closed); err = c.stream.Close() })
	return err
}

// AfterResponse runs only after this response frame has been written. It lets
// subscription notifications follow their initial snapshot on the wire.
func (c *Channel) AfterResponse(id []byte, callback func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.after[idKey(id)] = callback
}
