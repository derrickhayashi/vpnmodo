package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/controller"
	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

func vmFixture(t *testing.T, status string) (string, host.Host, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	registry := filepath.Join(root, "private", "hosts.json")
	dir := filepath.Join(filepath.Dir(registry), "setup", host.ManagedName)
	for _, path := range []string{bin, dir} {
		if err := privatefile.Dir(path); err != nil {
			t.Fatal(err)
		}
	}
	h := host.Host{Name: host.ManagedName, SSH: "lima-" + host.ManagedName, SSHConfig: filepath.Join(root, "ssh.config"), Sudo: true}
	if err := host.Add(registry, h); err != nil {
		t.Fatal(err)
	}
	if err := host.AddReference(context.Background(), host.ReferencePath(registry), host.Reference{Name: "example", Host: h.Name, Profile: "example"}); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		filepath.Join(dir, "lima.yaml"): "# synthetic setup marker\n",
		filepath.Join(root, "state"):    status,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, script := range map[string]string{
		"limactl": `#!/bin/sh
set -eu
case "$*" in
  'list --json')
    [ "${VM_TEST_LIST_FAIL:-}" != yes ] || exit 1
    [ -f "$VM_TEST_STATE" ] || exit 0
    printf '{"name":"vpn-vm","sshConfigFile":"%s","status":"%s"}\n' "$VM_TEST_SSH_CONFIG" "$(cat "$VM_TEST_STATE")"
    ;;
  'start --tty=false vpn-vm')
    echo start >> "$VM_TEST_LOG"
    echo synthetic-private-boot-diagnostic
    echo synthetic-private-boot-diagnostic >&2
    [ "${VM_TEST_START_FAIL:-}" != yes ] || exit 1
    echo Running > "$VM_TEST_STATE"
    ;;
  *) exit 1 ;;
esac
`,
		"ssh": "#!/bin/sh\nexec \"$VM_TEST_EXEC\" -test.run=^TestVMSSHProcess$ -- \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "commands.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VM_TEST_EXEC", executable)
	t.Setenv("VM_TEST_STATE", filepath.Join(root, "state"))
	t.Setenv("VM_TEST_SSH_CONFIG", h.SSHConfig)
	t.Setenv("VM_TEST_LOG", log)
	t.Setenv("VM_TEST_READY", filepath.Join(root, "controller-ready"))
	return registry, h, log
}

// TestVMSSHProcess supplies a framed controller over the fake SSH process. It
// refuses SSH before boot and briefly reports an unready controller after boot.
func TestVMSSHProcess(t *testing.T) {
	if os.Getenv("VM_TEST_EXEC") == "" {
		return
	}
	state, _ := os.ReadFile(os.Getenv("VM_TEST_STATE"))
	if strings.TrimSpace(string(state)) != "Running" {
		os.Exit(1)
	}
	log, err := os.OpenFile(os.Getenv("VM_TEST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(1)
	}
	defer log.Close()
	if !strings.Contains(os.Args[len(os.Args)-1], " bridge") {
		log.WriteString("probe\n")
		os.Exit(0)
	}
	reader, writer := protocol.NewFrameReader(os.Stdin), protocol.NewFrameWriter(os.Stdout)
	for {
		data, err := reader.Read()
		if err != nil {
			os.Exit(0)
		}
		req, err := protocol.DecodeRequest(data)
		if err != nil {
			os.Exit(1)
		}
		log.WriteString(string(req.Method) + "\n")
		status := controller.Status{Version: "synthetic-release", API: protocol.CurrentAPI(), StateReady: true, DockerReady: true, RuntimeEnabled: true}
		var result any
		switch req.Method {
		case protocol.Hello:
			result = protocol.HelloResult{API: protocol.CurrentAPI(), Backends: []profile.BackendType{profile.L2TP}}
		case protocol.SystemStatus:
			if os.Getenv("VM_TEST_DELAY_CONTROLLER") == "yes" {
				if _, err := os.Stat(os.Getenv("VM_TEST_READY")); os.IsNotExist(err) {
					status.DockerReady = false
					os.WriteFile(os.Getenv("VM_TEST_READY"), nil, 0600)
				}
			}
			result = status
		case protocol.ConnectionEnsure, protocol.OperationSubscribe:
			result = protocol.Operation{ID: "op-" + strings.Repeat("1", 32), Profile: "example", Kind: operation.EnsureConnection, State: operation.Succeeded, Revision: 1}
		case protocol.Doctor:
			result = struct {
				Controller controller.Status     `json:"controller"`
				Runtime    operation.RuntimeView `json:"runtime"`
			}{status, operation.RuntimeView{Health: operation.HealthHealthy, ApplicationID: strings.Repeat("a", 64), CredentialsPresent: true, PrerequisitesReady: true, TunnelReady: true}}
		default:
			os.Exit(1)
		}
		reply, err := protocol.EncodeResult(req.ID, result)
		if err != nil || writer.Write(reply) != nil {
			os.Exit(1)
		}
	}
}

func TestUpStartsStoppedVMAndWaitsForController(t *testing.T) {
	for _, status := range []string{"Stopped", "Running"} {
		t.Run(status, func(t *testing.T) {
			registry, _, log := vmFixture(t, status)
			if status == "Stopped" {
				t.Setenv("VM_TEST_DELAY_CONTROLLER", "yes")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var out bytes.Buffer
			if err := run(ctx, []string{"--config", registry, "up", "example", "--json"}, &out); err != nil {
				data, _ := os.ReadFile(log)
				t.Fatalf("up failed: %v; %s; commands: %s", err, out.String(), data)
			}
			decoder := json.NewDecoder(&out)
			var report accessReport
			for {
				var event accessReport
				if err := decoder.Decode(&event); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal("non-JSON startup output", err)
				}
				if event.Type == "access" {
					report = event
				}
			}
			if !report.VPNReady || report.Outcome != "vpn_ready" {
				t.Fatalf("VPN did not connect: %+v", report)
			}
			data, _ := os.ReadFile(log)
			commands := string(data)
			if status == "Stopped" {
				if !strings.HasPrefix(commands, "start\nprobe\n") || strings.Count(commands, "system.status\n") != 3 {
					t.Fatalf("boot/readiness order: %s", commands)
				}
			} else if strings.Contains(commands, "start\n") {
				t.Fatal("restarted a running VM")
			}
			if !strings.Contains(commands, "connection.ensure\n") {
				t.Fatal("did not ensure the VPN after VM startup")
			}
		})
	}
}

func TestUpVMFailuresDoNotConnect(t *testing.T) {
	for _, name := range []string{"missing", "unowned", "conflict", "list-failure", "start-failure", "broken", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			registry, _, log := vmFixture(t, "Stopped")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ""
			switch name {
			case "missing":
				os.Remove(os.Getenv("VM_TEST_STATE"))
				want = "vm_missing_run_setup"
			case "unowned":
				os.Remove(filepath.Join(filepath.Dir(registry), "setup", host.ManagedName, "lima.yaml"))
				want = "vm_setup_unavailable_run_setup"
			case "conflict":
				t.Setenv("VM_TEST_SSH_CONFIG", filepath.Join(t.TempDir(), "unrelated.config"))
				want = "vm_reference_conflict_run_setup"
			case "list-failure":
				t.Setenv("VM_TEST_LIST_FAIL", "yes")
				want = "vm_lima_unavailable_run_setup"
			case "start-failure":
				t.Setenv("VM_TEST_START_FAIL", "yes")
				want = "vm_start_failed"
			case "broken":
				os.WriteFile(os.Getenv("VM_TEST_STATE"), []byte("Broken"), 0600)
				want = "vm_state_unavailable"
			case "cancelled":
				cancel()
			}
			var out bytes.Buffer
			if err := run(ctx, []string{"--config", registry, "up", "example", "--json"}, &out); err == nil {
				t.Fatal("up succeeded without an available VM")
			}
			var report accessReport
			if json.Unmarshal(out.Bytes(), &report) != nil || report.Error == "" || want != "" && report.Error != want {
				t.Fatalf("unexpected failure report: %s", out.String())
			}
			data, _ := os.ReadFile(log)
			if strings.Contains(string(data), "probe") || name != "start-failure" && len(data) != 0 {
				t.Fatal("attempted SSH or unexpected VM startup after failure")
			}
		})
	}
}

func TestOtherAccessCommandsDoNotStartVM(t *testing.T) {
	for _, command := range []string{"status", "doctor", "down", "service"} {
		t.Run(command, func(t *testing.T) {
			registry, _, log := vmFixture(t, "Stopped")
			args := []string{"--config", registry, command}
			if command == "service" {
				args = append(args, "start")
			}
			args = append(args, "example", "--json")
			run(context.Background(), args, &bytes.Buffer{})
			if data, _ := os.ReadFile(log); len(data) != 0 {
				t.Fatal("started VM for", command)
			}
		})
	}
}

func TestConcurrentVMStartsResumeOnce(t *testing.T) {
	registry, h, log := vmFixture(t, "Stopped")
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := startManagedVM(context.Background(), registry, h, io.Discard); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	data, _ := os.ReadFile(log)
	if string(data) != "start\n" {
		t.Fatalf("concurrent callers did not share the VM start: %s", data)
	}
}
