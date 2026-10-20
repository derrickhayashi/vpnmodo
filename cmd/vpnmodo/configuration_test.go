package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

func combinedProfile(t *testing.T, backend string) (string, map[string]any) {
	t.Helper()
	fixture := "example-l2tp.json"
	if backend == "ipsec-xauth" {
		fixture = "example-xauth.json"
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "profiles", "v1", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		t.Fatal("invalid fixture")
	}
	input["credentials"] = map[string]string{"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}
	path := filepath.Join(t.TempDir(), "vpn.json")
	writeCombined(t, path, input)
	return path, input
}
func writeCombined(t *testing.T, path string, input map[string]any) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil || os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("write fixture")
	}
}
func noFileSecrets(t *testing.T, output []byte) {
	t.Helper()
	for _, v := range []string{"synthetic-user", "synthetic-password", "synthetic-shared"} {
		if bytes.Contains(output, []byte(v)) {
			t.Fatal("credential appeared in output or reference")
		}
	}
}

func TestCombinedFileCreateUpdateAndRotateBothBackends(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nexec \"$VPNMODO_TEST_BINARY\" -test.run=^TestCombinedFileRPCPeer$\n"
	if os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700) != nil {
		t.Fatal("write fake SSH")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VPNMODO_TEST_BINARY", executable)
	t.Setenv("VPNMODO_TEST_RPC_PEER", "yes")
	for _, backend := range []string{"l2tp-ipsec", "ipsec-xauth"} {
		t.Run(backend, func(t *testing.T) {
			file, _ := combinedProfile(t, backend)
			t.Setenv("VPNMODO_TEST_PROFILE_FILE", file)
			registry := filepath.Join(t.TempDir(), "private", "hosts.json")
			if err := host.Add(registry, host.Host{Name: host.ManagedName, SSH: "192.0.2.250"}); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"profile", "create", "example", "--file", file, "--json"},
				{"profile", "update", "example", "--file", file, "--revision", "1", "--json"},
				{"profile", "rotate-credentials", "example", "--file", file, "--revision", "2", "--json"},
			} {
				var out bytes.Buffer
				ctx, cancel := context.WithCancel(context.Background())
				err := run(ctx, append([]string{"--config", registry}, args...), &out)
				cancel()
				if err != nil {
					t.Fatal("combined file command failed:", args[1], err)
				}
				noFileSecrets(t, out.Bytes())
				var result profile.Snapshot
				if json.Unmarshal(out.Bytes(), &result) != nil || !result.CredentialsPresent || string(result.Profile.Backend.Type) != backend {
					t.Fatal("invalid profile result")
				}
			}
			raw, err := os.ReadFile(host.ReferencePath(registry))
			if err != nil {
				t.Fatal(err)
			}
			noFileSecrets(t, raw)
		})
	}
}

// This subprocess stands in for SSH transport only. Production codecs validate
// both RPC fields; profile.DecodeProfile rejects credential/registry fields.
func TestCombinedFileRPCPeer(t *testing.T) {
	if os.Getenv("VPNMODO_TEST_RPC_PEER") != "yes" {
		return
	}
	input, err := readProfileFile(os.Getenv("VPNMODO_TEST_PROFILE_FILE"))
	if err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		var result any
		if req.Method == "system.hello" {
			result = protocol.HelloResult{API: protocol.CurrentAPI(), ControllerVersion: "synthetic", Backends: []profile.BackendType{profile.L2TP, profile.XAuth}}
		} else {
			var params map[string]json.RawMessage
			if json.Unmarshal(req.Params, &params) != nil {
				os.Exit(2)
			}
			snapshot := profile.Snapshot{Profile: input.configuration(), Revision: 1, CredentialsPresent: true}
			switch req.Method {
			case "profile.create", "profile.update":
				parsed, e := profile.DecodeProfile(params["profile"])
				if e != nil || parsed.Backend.Type != input.Backend.Type {
					os.Exit(2)
				}
				if req.Method == "profile.update" {
					if _, ok := params["credentials"]; ok {
						os.Exit(2)
					}
				}
			case string(protocol.CredentialRotate):
			default:
				os.Exit(2)
			}
			if req.Method != "profile.update" {
				var credentials map[string]string
				if json.Unmarshal(params["credentials"], &credentials) != nil || len(credentials) != 3 || credentials["username"] != "synthetic-user" || credentials["password"] != "synthetic-password" || credentials["psk"] != "synthetic-shared" {
					os.Exit(2)
				}
			}
			if raw, ok := params["expected_revision"]; ok {
				if json.Unmarshal(raw, &snapshot.Revision) != nil {
					os.Exit(2)
				}
				snapshot.Revision++
			}
			result = snapshot
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func TestCombinedFileValidationBeforeSSH(t *testing.T) {
	file, input := combinedProfile(t, "ipsec-xauth")
	original, _ := os.ReadFile(file)
	for _, bad := range []string{
		strings.Replace(string(original), `"credentials":{`, `"credentials":null,"unused":{`, 1),
		strings.Replace(string(original), `"psk":"synthetic-shared"`, `"psk":""`, 1),
		strings.Replace(string(original), `"password":"synthetic-password"`, `"password":"synthetic-password","password":"duplicate"`, 1),
	} {
		if os.WriteFile(file, []byte(bad), 0600) != nil {
			t.Fatal("write invalid fixture")
		}
		_, err := readProfileFile(file)
		if err == nil {
			t.Fatal("invalid combined input accepted")
		}
		noFileSecrets(t, []byte(err.Error()))
	}
	delete(input, "credentials")
	writeCombined(t, file, input)
	var out bytes.Buffer
	err := run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "hosts.json"), "profile", "create", "example", "--file", file}, &out)
	if err == nil || !strings.Contains(err.Error(), "JSON must include credentials") || out.Len() != 0 {
		t.Fatal("missing credentials did not fail before SSH/prompt")
	}
	if os.Chmod(file, 0644) != nil {
		t.Fatal("chmod fixture")
	}
	input["credentials"] = map[string]string{"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}
	writeCombined(t, file, input)
	if _, err := readProfileFile(file); err != nil {
		t.Fatalf("ordinary input permissions rejected: %v", err)
	}
}
