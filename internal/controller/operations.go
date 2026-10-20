package controller

import (
	"context"
	"errors"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/derrickhayashi/vpnmodo/internal/lifecycle"
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/profilestore"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

type subscription struct {
	cancel  context.CancelFunc
	release func()
}

func (d *dispatcher) closeSubscriptions() {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for _, s := range d.subscriptions {
		s.cancel()
		s.release()
	}
	d.subscriptions = map[string]*subscription{}
}
func (d *dispatcher) lifecycle(ctx context.Context, req *jrpc2.Request, raw []byte, method protocol.Method) (any, error) {
	switch method {
	case protocol.ConnectionEnsure, protocol.ConnectionDisconnect, protocol.ConnectionStatus:
		params, err := protocol.DecodeProfileParams(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		if method == protocol.ConnectionStatus {
			var result operation.Connection
			err = d.profiles.WithProfile(ctx, params.Profile, func(profile.Snapshot) error {
				var err error
				result, err = d.operations.Connection(params.Profile)
				return err
			})
			if err != nil {
				return nil, lifecycleFault(err)
			}
			return result, nil
		}
		var record operation.Record
		if method == protocol.ConnectionEnsure {
			record, err = d.operations.Ensure(ctx, params.Profile)
		} else {
			record, err = d.operations.Disconnect(ctx, params.Profile)
		}
		return protocol.OperationFromRecord(record), lifecycleFault(err)
	case protocol.OperationSubmitMFA:
		params, err := protocol.DecodeMFASubmit(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		if err = d.operations.SubmitMFA(params.OperationID, params.ChallengeID, params.Code); err != nil {
			return nil, lifecycleFault(err)
		}
		record, err := d.operations.Status(params.OperationID)
		return protocol.OperationFromRecord(record), lifecycleFault(err)
	default:
		params, err := protocol.DecodeOperationParams(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		if method == protocol.OperationCancel {
			record, err := d.operations.Cancel(params.OperationID)
			return protocol.OperationFromRecord(record), lifecycleFault(err)
		}
		if method == protocol.OperationSubscribe {
			return d.subscribe(ctx, req, params.OperationID)
		}
		record, err := d.operations.Status(params.OperationID)
		return protocol.OperationFromRecord(record), lifecycleFault(err)
	}
}
func (d *dispatcher) subscribe(requestCtx context.Context, req *jrpc2.Request, id string) (any, error) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	if previous := d.subscriptions[id]; previous != nil {
		previous.cancel()
		previous.release()
		delete(d.subscriptions, id)
	}
	if len(d.subscriptions) >= 16 {
		return nil, fault(protocol.CodeBusy)
	}
	record, updates, release, err := d.operations.Subscribe(id)
	if err != nil {
		return nil, lifecycleFault(err)
	}
	if record.State.Terminal() {
		release()
		return protocol.OperationFromRecord(record), nil
	}
	ctx, cancel := context.WithCancel(d.connectionCtx)
	s := &subscription{cancel: cancel, release: release}
	d.subscriptions[id] = s
	server := jrpc2.ServerFromContext(requestCtx)
	d.channel.AfterResponse([]byte(req.ID()), func() { d.pump(ctx, server, id, s, updates) })
	return protocol.OperationFromRecord(record), nil
}

func (d *dispatcher) pump(ctx context.Context, server *jrpc2.Server, id string, s *subscription, updates <-chan operation.Record) {
	defer func() {
		s.cancel()
		s.release()
		d.subMu.Lock()
		if d.subscriptions[id] == s {
			delete(d.subscriptions, id)
		}
		d.subMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case record, ok := <-updates:
			if !ok {
				return
			}
			sendCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := server.Notify(sendCtx, string(protocol.OperationChanged), struct {
				Operation protocol.Operation `json:"operation"`
			}{protocol.OperationFromRecord(record)})
			cancel()
			if err != nil || record.State.Terminal() {
				return
			}
		}
	}
}
func lifecycleFault(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, lifecycle.ErrPersistence):
		return fault(protocol.CodePersistence)
	case errors.Is(err, lifecycle.ErrNotFound), errors.Is(err, profilestore.ErrNotFound):
		return fault(protocol.CodeNotFound)
	case errors.Is(err, lifecycle.ErrCredentials):
		return fault(protocol.CodeCredentials)
	case errors.Is(err, lifecycle.ErrBusy):
		return fault(protocol.CodeBusy)
	case errors.Is(err, lifecycle.ErrExpired):
		return fault(protocol.CodeMFAExpired)
	case errors.Is(err, lifecycle.ErrChallenge), errors.Is(err, operation.ErrOperation):
		return fault(protocol.CodeInvalidParams)
	case errors.Is(err, lifecycle.ErrClosed):
		return fault(protocol.CodeInterrupted)
	default:
		return fault(protocol.CodeBackendUnavailable)
	}
}
