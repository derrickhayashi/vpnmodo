package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/credentialinput"
	"io"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
)

func operationCommand(base context.Context, registry string, args []string, out io.Writer) error {
	if len(args) < 3 {
		return errors.New("usage: vpnmodo connection {ensure|disconnect|status} ALIAS; vpnmodo operation {status|cancel|watch} ID --host HOST")
	}
	group, command, name := args[0], args[1], args[2]
	flags := flag.NewFlagSet("operation", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := flags.String("host", "", "")
	machine := flags.Bool("json", false, "")
	challenge := flags.String("challenge", "", "")
	codeStdin := flags.Bool("code-stdin", false, "")
	wait := flags.Bool("wait", false, "")
	if flags.Parse(args[3:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid operation options")
	}
	var method protocol.Method
	var params any
	if group == "connection" {
		if *target != "" {
			return errors.New("connection commands use a registered profile alias")
		}
		reference, err := host.FindReference(host.ReferencePath(registry), name)
		if err != nil {
			return err
		}
		*target = reference.Host
		params = protocol.ProfileParams{Profile: reference.Profile}
		switch command {
		case "ensure":
			method = protocol.ConnectionEnsure
		case "disconnect":
			method = protocol.ConnectionDisconnect
		case "status":
			method = protocol.ConnectionStatus
		default:
			return errors.New("unknown connection command")
		}
	} else {
		if !operation.ValidID(name) || *target == "" || *wait {
			return errors.New("operation ID and --host are required; watch is a separate command")
		}
		params = protocol.OperationParams{OperationID: name}
		switch command {
		case "status", "watch":
			method = protocol.OperationStatus
		case "cancel":
			method = protocol.OperationCancel
		case "submit-mfa":
			method = protocol.OperationSubmitMFA
		default:
			return errors.New("unknown operation command")
		}
	}
	if method != protocol.OperationSubmitMFA && (*challenge != "" || *codeStdin) {
		return errors.New("MFA input options require operation submit-mfa")
	}
	if *wait && method == protocol.ConnectionStatus {
		return errors.New("--wait requires ensure or disconnect")
	}
	h, err := host.Find(registry, *target)
	if err != nil {
		return err
	}
	duration := 30 * time.Second
	if *wait || command == "watch" {
		duration = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(base, duration)
	defer cancel()
	client, err := host.Connect(ctx, h)
	if err != nil {
		return err
	}
	defer client.Close()
	emit := func(value any) error {
		encoder := json.NewEncoder(out)
		if !*machine {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(value)
	}
	if method == protocol.ConnectionStatus {
		var connection operation.Connection
		if err = client.Call(ctx, method, params, &connection); err != nil {
			return err
		}
		return emit(connection)
	}
	if method == protocol.OperationSubmitMFA {
		if !operation.ValidChallengeID(*challenge) {
			return errors.New("current --challenge is required")
		}
		code, e := credentialinput.ReadCode(ctx, *codeStdin)
		if e != nil {
			return e
		}
		payload, e := (protocol.MFASubmitParams{OperationID: name, ChallengeID: *challenge, Code: code}).EncodeForTransport()
		if e != nil {
			return e
		}
		defer clear(payload)
		params = json.RawMessage(payload)
	}
	var snapshot protocol.Operation
	if err = client.Call(ctx, method, params, &snapshot); err != nil {
		return err
	}
	if *wait || command == "watch" {
		return watchOperationPrompt(ctx, client, snapshot.ID, emit, !*machine)
	}
	return emit(snapshot)
}
func watchOperation(ctx context.Context, client *rpcio.Client, id string, emit func(any) error) error {
	return watchOperationPrompt(ctx, client, id, emit, false)
}
func watchOperationPrompt(ctx context.Context, client *rpcio.Client, id string, emit func(any) error, interactive bool) error {
	prompted := ""
	var snapshot protocol.Operation
	if err := client.Call(ctx, protocol.OperationSubscribe, protocol.OperationParams{OperationID: id}, &snapshot); err != nil {
		return err
	}
	publish := func(value protocol.Operation) (bool, error) {
		if value.Validate() != nil {
			return false, rpcio.ErrRemote
		}
		if err := emit(value); err != nil {
			return false, err
		}
		if interactive && value.State == operation.AwaitingMFA && value.Challenge.ID != prompted {
			prompted = value.Challenge.ID
			promptCtx, cancel := context.WithDeadline(ctx, value.Challenge.ExpiresAt)
			code, err := credentialinput.ReadCode(promptCtx, false)
			cancel()
			if err != nil {
				return true, err
			}
			payload, err := (protocol.MFASubmitParams{OperationID: id, ChallengeID: prompted, Code: code}).EncodeForTransport()
			if err != nil {
				return true, err
			}
			var result protocol.Operation
			err = client.Call(ctx, protocol.OperationSubmitMFA, json.RawMessage(payload), &result)
			clear(payload)
			if err != nil {
				return true, err
			}
		}
		if value.State == operation.Failed {
			return true, value.Failure
		}
		if value.State == operation.Cancelled {
			return true, errors.New("operation cancelled")
		}
		return value.State.Terminal(), nil
	}
	done, err := publish(snapshot)
	if done || err != nil {
		return err
	}
	// A snapshot poll also detects dropped SSH sessions and recovers missed bounded
	// notifications. Disconnecting the watcher never cancels the VM operation.
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		var next protocol.Operation
		select {
		case <-ctx.Done():
			return errors.New("operation watch ended; reconnect with operation status or watch")
		case next = <-client.Updates():
			if next.ID != id || next.Revision <= snapshot.Revision {
				continue
			}
		case <-ticker.C:
			if err := client.Call(ctx, protocol.OperationStatus, protocol.OperationParams{OperationID: id}, &next); err != nil {
				return err
			}
			if next.Revision <= snapshot.Revision {
				continue
			}
		}
		snapshot = next
		done, err = publish(snapshot)
		if done || err != nil {
			return err
		}
	}
}
