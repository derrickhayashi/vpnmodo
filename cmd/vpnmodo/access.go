package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/access"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/controller"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
)

type accessCheck struct {
	Layer string `json:"layer"`
	Code  string `json:"code"`
	OK    bool   `json:"ok"`
}
type accessReport struct {
	Connection         *operation.Connection `json:"connection,omitempty"`
	ControllerVersion  string                `json:"controller_version,omitempty"`
	ProfileRevision    uint64                `json:"profile_revision,omitempty"`
	CredentialsPresent bool                  `json:"credentials_present"`
	Type               string                `json:"type"`
	Alias              string                `json:"alias"`
	Outcome            string                `json:"outcome"`
	VPNReady           bool                  `json:"vpn_ready"`
	Ready              bool                  `json:"ready"`
	Checks             []accessCheck         `json:"checks"`
	Services           []access.Result       `json:"services"`
	OperationID        string                `json:"operation_id,omitempty"`
	Error              string                `json:"error,omitempty"`
}

var errAccess = errors.New("service access incomplete; see reported checks")

func accessCommand(base context.Context, registry string, args []string, out io.Writer) error {
	command := args[0]
	group := ""
	if command == "service" {
		if len(args) < 3 {
			return errors.New("usage: vpnmodo service {configure|start|stop|status} ALIAS")
		}
		group = command
		command = args[1]
		args = args[1:]
	}
	if len(args) < 2 {
		return errors.New("registered profile alias is required")
	}
	alias := args[1]
	flags := flag.NewFlagSet("access", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	machine := flags.Bool("json", false, "")
	file := flags.String("file", "", "")
	selected := flags.String("service", "", "")
	r := accessReport{Type: "access", Alias: alias, Checks: []accessCheck{}, Services: []access.Result{}, Outcome: "failed"}
	emit := func() error {
		if *machine {
			return json.NewEncoder(out).Encode(r)
		}
		fmt.Fprintf(out, "%s: %s\n", r.Alias, r.Outcome)
		if r.ControllerVersion != "" {
			fmt.Fprintf(out, "  controller version: %s\n", r.ControllerVersion)
		}
		if r.Connection != nil {
			fmt.Fprintf(out, "  desired: %s; observed: %s\n", r.Connection.Desired, r.Connection.Observed)
		}
		for _, c := range r.Checks {
			verdict := "ok"
			if !c.OK {
				verdict = "failed"
			}
			fmt.Fprintf(out, "  %s: %s (%s)\n", c.Layer, verdict, c.Code)
		}
		for _, s := range r.Services {
			fmt.Fprintf(out, "  %s (127.0.0.1:%d): %s\n", s.Name, s.LocalPort, s.Code)
		}
		if r.Error != "" {
			fmt.Fprintln(out, "  "+r.Error)
		}
		return nil
	}
	fail := func(code string) error {
		if r.Outcome == "failed" {
			for _, s := range r.Services {
				if s.Code == "ready" || s.Code == "stopped" {
					r.Outcome = "partial"
					break
				}
			}
		}
		r.Error = code
		if err := emit(); err != nil {
			return err
		}
		return errAccess
	}
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 {
		for _, arg := range args[2:] {
			if arg == "--json" {
				*machine = true
			}
		}
		return fail("invalid_options")
	}
	if (group == "service" && command != "configure" && command != "start" && command != "stop" && command != "status") || (group == "" && command != "up" && command != "down" && command != "status" && command != "doctor") {
		return fail("invalid_command")
	}
	if (*file != "" && (group != "service" || command != "configure")) || (*selected != "" && !(group == "service" && (command == "start" || command == "stop") || command == "up")) {
		return fail("invalid_options")
	}
	duration := 2 * time.Minute
	if command == "up" {
		duration = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(base, duration)
	defer cancel()
	if privatefile.Dir(registry+".access") != nil {
		return fail("private_storage_unavailable")
	}
	if command == "configure" || command == "start" || command == "stop" || command == "up" || command == "down" {
		// Serialize composed intent on this alias; other profiles remain independent.
		// Validate alias before constructing any path.
		if (host.Reference{Name: alias, Host: "valid", Profile: "valid"}).Validate() != nil {
			return fail("invalid_alias")
		}
		unlock, err := privatefile.Lock(ctx, filepath.Join(registry+".access", alias+".action.lock"))
		if err != nil {
			return fail("private_storage_unavailable")
		}
		defer unlock()
	}
	if command == "stop" || command == "down" {
		results, err := access.Stop(ctx, registry, alias, *selected)
		r.Services = results
		if err != nil {
			r.Error = "local_stop_failed"
		}
		for _, s := range results {
			if s.Code != "stopped" {
				r.Error = "local_stop_failed"
			}
		}
		if command == "stop" {
			if r.Error != "" {
				return fail(r.Error)
			}
			r.Outcome = "stopped"
			return emit()
		}
	}
	ref, err := host.FindReference(host.ReferencePath(registry), alias)
	if err != nil {
		return fail("profile_reference_unavailable")
	}
	h, err := host.Find(registry, ref.Host)
	if err != nil {
		return fail("vm_reference_unavailable")
	}
	if command == "configure" {
		d, err := access.Load(*file)
		if err != nil {
			return fail("invalid_service_definition")
		}
		if err = access.Configure(ctx, registry, alias, d); err != nil {
			return fail("service_configuration_unavailable_stop_services_first")
		}
		r.Outcome = "configured"
		return emit()
	}
	if command == "status" || command == "doctor" {
		results, err := access.Status(ctx, registry, alias)
		if err != nil {
			return fail("local_state_unavailable")
		}
		r.Services = results
	}
	if *selected != "" && (command == "up" || command == "start") {
		definition, e := access.Read(registry, alias)
		found := false
		if e == nil {
			for _, s := range definition.Services {
				if s.Name == *selected {
					found = true
					break
				}
			}
		}
		if !found {
			return fail("named_service_not_configured")
		}
	}
	probe, cancelProbe := context.WithTimeout(ctx, 15*time.Second)
	err = host.Probe(probe, h)
	cancelProbe()
	if err != nil {
		r.Checks = append(r.Checks, accessCheck{"vm_ssh", "vm_ssh_unavailable", false})
		return fail("vm_ssh_unavailable")
	}
	r.Checks = append(r.Checks, accessCheck{"vm_ssh", "ready", true})
	client, err := host.Connect(ctx, h)
	if err != nil {
		r.Checks = append(r.Checks, accessCheck{"controller", "controller_unavailable", false})
		return fail("controller_unavailable")
	}
	defer client.Close()
	var system controller.Status
	if err = client.Call(ctx, protocol.SystemStatus, struct{}{}, &system); err != nil {
		return fail("controller_unavailable")
	}
	if !system.StateReady || !system.DockerReady || !system.RuntimeEnabled {
		r.Checks = append(r.Checks, accessCheck{"controller", "controller_prerequisites_unavailable", false})
		return fail("controller_prerequisites_unavailable")
	}
	r.Checks = append(r.Checks, accessCheck{"controller", "ready", true})
	r.ControllerVersion = system.Version
	if command == "up" || command == "down" {
		method := protocol.ConnectionEnsure
		if command == "down" {
			method = protocol.ConnectionDisconnect
		}
		var op protocol.Operation
		if err = client.Call(ctx, method, protocol.ProfileParams{Profile: ref.Profile}, &op); err != nil {
			return fail("connection_operation_failed")
		}
		r.OperationID = op.ID
		// Machine up/down emit tagged operation events followed by an access report.
		notify := func(v any) error {
			if *machine {
				return json.NewEncoder(out).Encode(struct {
					Type      string `json:"type"`
					Operation any    `json:"operation"`
				}{"operation", v})
			}
			if op, ok := v.(protocol.Operation); ok {
				_, err := fmt.Fprintf(out, "VPN: %s\n", op.State)
				return err
			}
			return nil
		}
		if err = watchOperationPrompt(ctx, client, op.ID, notify, !*machine); err != nil {
			return fail("connection_operation_incomplete")
		}
		if command == "down" {
			if r.Error != "" {
				r.Outcome = "partial"
				return fail(r.Error)
			}
			r.Outcome = "disconnected"
			return emit()
		}
	}
	var view struct {
		Controller controller.Status     `json:"controller"`
		Runtime    operation.RuntimeView `json:"runtime"`
	}
	inspectionDeadline := time.Now().Add(5 * time.Second)
	for {
		err = client.Call(ctx, protocol.Doctor, protocol.ProfileParams{Profile: ref.Profile}, &view)
		var rpc *protocol.RPCError
		if !errors.As(err, &rpc) || rpc.Code != protocol.CodeBusy {
			break
		}
		var connection operation.Connection
		if e := client.Call(ctx, protocol.ConnectionStatus, protocol.ProfileParams{Profile: ref.Profile}, &connection); e != nil {
			break
		}
		r.Connection = &connection
		if connection.ActiveOperation != "" {
			r.OperationID = connection.ActiveOperation
			return fail("operation_in_progress")
		}
		// A background health probe briefly owns the runtime gate; wait for it, but
		// do not make doctor sit through an active authentication/connection attempt.
		if time.Now().After(inspectionDeadline) {
			break
		}
		select {
		case <-ctx.Done():
			return fail("inspection_unavailable")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		return fail("inspection_unavailable_operation_may_be_active")
	}
	r.Connection = &view.Runtime.Connection
	r.ProfileRevision = view.Runtime.ProfileRevision
	r.CredentialsPresent = view.Runtime.CredentialsPresent
	r.Checks = append(r.Checks, accessCheck{"credentials", "credentials_present", r.CredentialsPresent}, accessCheck{"runtime_prerequisites", "images_and_compose", view.Runtime.PrerequisitesReady}, accessCheck{"deployment", string(view.Runtime.Health), view.Runtime.Health != operation.HealthOwnerConflict})
	health := view.Runtime.Health
	tunnelOK := view.Runtime.TunnelReady
	serviceOK := view.Runtime.PrivateEndpointReady
	appOK := health == operation.HealthHealthy && view.Runtime.ApplicationID != ""
	code := string(health)
	r.Checks = append(r.Checks, accessCheck{"vpn", code, tunnelOK}, accessCheck{"private_health_endpoint", code, serviceOK}, accessCheck{"application", code, appOK})
	r.VPNReady = tunnelOK && serviceOK && appOK
	if !r.VPNReady {
		if len(r.Services) > 0 {
			r.Outcome = "partial"
		}
		return fail("vpn_access_unavailable")
	}
	if command == "up" || command == "start" {
		results, err := access.Start(ctx, registry, alias, h, ref, *selected, view.Runtime.ApplicationID)
		r.Services = results
		if err != nil {
			r.Outcome = "partial"
			return fail("local_service_start_incomplete")
		}
	}
	for i := range r.Services {
		if r.Services[i].Code == "ready" && r.Services[i].ApplicationID != view.Runtime.ApplicationID {
			r.Services[i].Code = "service_binding_changed_stop_first"
			r.Services[i].Endpoint = ""
		}
	}
	r.Ready = len(r.Services) > 0 && view.Runtime.PrerequisitesReady && r.CredentialsPresent
	r.Outcome = "vpn_ready"
	if !view.Runtime.PrerequisitesReady || !r.CredentialsPresent {
		r.Outcome = "partial"
	}
	for _, s := range r.Services {
		if s.Code != "ready" {
			r.Ready = false
			r.Outcome = "partial"
		}
	}
	if r.Ready {
		r.Outcome = "ready"
	}
	if r.Outcome == "partial" {
		return fail("service_access_incomplete")
	}
	return emit()
}
func proxyCommand(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return access.ErrConfig
	}
	d, err := access.ReadDescriptor(args[1])
	if err != nil {
		return err
	}
	return host.Proxy(ctx, d.Host, d.Request, os.Stdin, os.Stdout)
}
