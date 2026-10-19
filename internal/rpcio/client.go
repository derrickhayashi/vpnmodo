package rpcio

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/creachadair/jrpc2"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
)

var ErrRemote = errors.New("controller request failed; check SSH authentication, host keys and Docker access")

type Client struct {
	client  *jrpc2.Client
	updates chan protocol.Operation
}

func Connect(ctx context.Context, stream io.ReadWriteCloser) (*Client, error) {
	client := &Client{updates: make(chan protocol.Operation, 32)}
	client.client = jrpc2.NewClient(New(stream, false), &jrpc2.ClientOptions{OnNotify: func(request *jrpc2.Request) {
		if request.Method() != string(protocol.OperationChanged) {
			return
		}
		var raw json.RawMessage
		var params struct {
			Operation protocol.Operation `json:"operation"`
		}
		if request.UnmarshalParams(&raw) != nil || jsonstrict.Decode(raw, &params, protocol.MaxMessageBytes) != nil || params.Operation.Validate() != nil {
			return
		}
		select {
		case client.updates <- params.Operation:
		default:
			select {
			case <-client.updates:
			default:
			}
			select {
			case client.updates <- params.Operation:
			default:
			}
		}
	}})
	var hello protocol.HelloResult
	if err := client.Call(ctx, protocol.Hello, protocol.HelloParams{API: protocol.CurrentAPI()}, &hello); err != nil {
		_ = client.Close()
		return nil, err
	}
	if !hello.API.Compatible(protocol.CurrentAPI()) {
		_ = client.Close()
		return nil, protocol.NewRPCError(protocol.CodeVersion)
	}
	return client, nil
}
func (c *Client) Call(ctx context.Context, method protocol.Method, params, result any) error {
	response, err := c.client.Call(ctx, string(method), params)
	if err != nil {
		var rpc *jrpc2.Error
		if errors.As(err, &rpc) {
			return protocol.NewRPCError(protocol.ErrorCode(rpc.Code))
		}
		return ErrRemote
	}
	var raw json.RawMessage
	if response.UnmarshalResult(&raw) != nil || jsonstrict.Decode(raw, result, protocol.MaxMessageBytes) != nil {
		return ErrRemote
	}
	return nil
}
func (c *Client) Close() error { return c.client.Close() }

// Updates is a bounded best-effort notification stream. Callers must use their
// own context and re-fetch a snapshot after reconnect; this channel is not closed.
func (c *Client) Updates() <-chan protocol.Operation { return c.updates }
