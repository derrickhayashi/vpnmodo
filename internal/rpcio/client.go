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

type Client struct{ client *jrpc2.Client }

func Connect(ctx context.Context, stream io.ReadWriteCloser) (*Client, error) {
	client := &Client{client: jrpc2.NewClient(New(stream, false), nil)}
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
