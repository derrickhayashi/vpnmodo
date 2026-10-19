package access

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
)

var syscallECONNREFUSED = syscall.ECONNREFUSED

func destination(ctx context.Context, s Service) (host.ProxyRequest, error) {
	// ssh -G resolves the user's normal key/user/host configuration without network
	// access. Values enter JSON stdin, never the VM's remote shell command.
	args := []string{"-G", "-o", "ProxyCommand=none", "-o", "CanonicalizeHostname=no", "-o", "PermitLocalCommand=no"}
	if s.SSHConfig != "" {
		args = append(args, "-F", s.SSHConfig)
	}
	args = append(args, "--", s.SSHTarget)
	b, err := boundedRun(ctx, args, nil)
	if err != nil {
		return host.ProxyRequest{}, ErrConfig
	}
	var p host.ProxyRequest
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "hostname":
			p.Host = value
		case "port":
			p.Port, _ = strconv.Atoi(value)
		}
	}
	if !Endpoint(p.Host) || p.Port < 1 || p.Port > 65535 {
		return p, ErrConfig
	}
	return p, nil
}

const serviceProbe = `import json,socket,sys
try:
 p=json.load(sys.stdin)
 s=socket.create_connection((p["host"],p["port"]),timeout=3)
 s.close()
 print('{"reachable":true}')
except Exception:
 print('{"reachable":false}')
`

func inspectOne(ctx context.Context, s state) Result {
	r := Result{Name: s.Service.Name, LocalPort: s.Service.LocalPort, Code: "destination_ssh_unavailable"}
	if checkDir(s.Directory) != nil || control(ctx, s, "check") != nil {
		return r
	}
	r.DestinationSSHReady = true
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Service.LocalPort)), time.Second)
	if err != nil {
		r.Code = "local_listener_unavailable"
		return r
	}
	c.Close()
	r.ListenerReady = true
	args := []string{"-T", "-F", "/dev/null", "-S", filepath.Join(s.Directory, "s"), "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ProxyCommand=false", "-o", "ConnectTimeout=3", "--", s.Service.SSHTarget, "python3 -c " + shellQuote(serviceProbe)}
	input, _ := json.Marshal(struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}{s.Service.RemoteHost, s.Service.RemotePort})
	b, err := boundedRun(ctx, args, input)
	if err != nil {
		r.Code = "remote_service_unverified"
		return r
	}
	var reply struct {
		Reachable bool `json:"reachable"`
	}
	if jsonstrict.Decode(b, &reply, 4096) != nil {
		r.Code = "remote_service_unverified"
		return r
	}
	if !reply.Reachable {
		r.Code = "remote_service_unavailable"
		return r
	}
	r.RemoteServiceReady = true
	r.Code = "ready"
	return r
}
