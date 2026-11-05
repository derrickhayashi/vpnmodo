package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	managed := os.Getenv("PROBE_MANAGED") == "1"
	os.MkdirAll("/run/secrets", 0700)
	if err := os.WriteFile("/run/secrets/vpn_username", []byte("synthetic-user"), 0600); err != nil {
		return err
	}
	config := []byte("include /etc/strongswan.conf\ncharon-cmd {\n port = 15000\n port_nat_t = 15001\n}\n")
	if err := os.WriteFile("/tmp/probe-strongswan.conf", config, 0600); err != nil {
		return err
	}
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4500})
	if err != nil {
		return err
	}
	defer listener.Close()
	cmd := exec.Command("charon-cmd", "--debug", "1", "--host", "127.0.0.1", "--identity", "192.0.2.2", "--remote-identity", "%any", "--xauth-username", "synthetic-user", "--profile", "ikev1-xauth-psk-am", "--ike-proposal", "aes256-sha256-modp2048")
	cmd.Env = append(os.Environ(), "STRONGSWAN_CONF=/tmp/probe-strongswan.conf")
	if managed {
		cmd.Env = append(cmd.Env, "VPNMODO_MANAGED_XAUTH=1")
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	listener.SetReadDeadline(time.Now().Add(15 * time.Second))
	packet := make([]byte, 65536)
	n, _, err := listener.ReadFromUDP(packet)
	if err != nil {
		return fmt.Errorf("no synthetic IKE proposal received: %w", err)
	}
	packet = packet[:n]
	if len(packet) > 4 && binary.BigEndian.Uint32(packet[:4]) == 0 {
		packet = packet[4:]
	}
	if len(packet) < 40 || packet[16] != 1 || packet[18] != 4 {
		return fmt.Errorf("unexpected initial IKE payload")
	}
	sa := packet[28:]
	saLen := int(binary.BigEndian.Uint16(sa[2:4]))
	if saLen > len(sa) || saLen < 20 {
		return fmt.Errorf("invalid SA payload")
	}
	sa = sa[:saLen]
	proposal := sa[12:]
	pLen := int(binary.BigEndian.Uint16(proposal[2:4]))
	if pLen > len(proposal) || pLen < 16 {
		return fmt.Errorf("invalid proposal")
	}
	proposal = proposal[:pLen]
	spiSize := int(proposal[6])
	if 8+spiSize+8 > len(proposal) {
		return fmt.Errorf("invalid transform offset")
	}
	transform := proposal[8+spiSize:]
	tLen := int(binary.BigEndian.Uint16(transform[2:4]))
	if tLen > len(transform) || tLen < 8 {
		return fmt.Errorf("invalid transform")
	}
	attrs := transform[8:tLen]
	var lifetime uint64
	for len(attrs) >= 4 {
		kind := binary.BigEndian.Uint16(attrs[:2])
		value := binary.BigEndian.Uint16(attrs[2:4])
		attrs = attrs[4:]
		if kind&0x8000 != 0 {
			if kind&0x7fff == 12 {
				lifetime = uint64(value)
			}
			continue
		}
		if int(value) > len(attrs) {
			return fmt.Errorf("invalid attribute length")
		}
		if kind == 12 {
			if value > 8 {
				return fmt.Errorf("oversized lifetime")
			}
			for _, v := range attrs[:value] {
				lifetime = (lifetime << 8) | uint64(v)
			}
		}
		attrs = attrs[value:]
	}
	expected := uint64(36600)
	if os.Getenv("PROBE_EXPECT") != "" {
		expected, err = strconv.ParseUint(os.Getenv("PROBE_EXPECT"), 10, 64)
		if err != nil {
			return err
		}
	}
	if lifetime != expected {
		return fmt.Errorf("advertised lifetime %d, expected %d", lifetime, expected)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"managed": managed, "advertised_ike_lifetime_seconds": lifetime, "synthetic_loopback_only": true})
}
