package managedvpn

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

// Network never changes routes, starts a login, or exposes destinations.
func (a *Adapter) Network(ctx context.Context, p profile.Profile) *operation.NetworkStatus {
	_, cs, err := a.owned(ctx, p)
	if err != nil {
		return a.networkStatus(ctx, p, container{})
	}
	return a.networkStatus(ctx, p, cs["vpn"])
}

func (a *Adapter) networkStatus(ctx context.Context, p profile.Profile, c container) *operation.NetworkStatus {
	if p.Network == nil {
		return nil
	}
	n := &operation.NetworkStatus{ConfiguredMTU: p.Network.MTU, DerivedMSS: p.Network.MSS(), Code: "network_settings_unverified"}
	mtu := p.Network.MTU
	device := ""
	if p.Backend.Type == profile.L2TP {
		device = "ppp0"
	}
	if p.Backend.Type == profile.GlobalProtect {
		device = "vpn0"
	}
	if device != "" {
		data, err := a.exec(ctx, c, "ip", "-j", "link", "show", "dev", device)
		var links []struct {
			MTU int `json:"mtu"`
		}
		if err != nil || json.Unmarshal(data, &links) != nil || len(links) != 1 || links[0].MTU < 576 || links[0].MTU > mtu {
			return n
		}
		// PPP/OpenConnect may select a lower MTU. PPP routes inherit it;
		// GlobalProtect explicitly applies its effective limit and derived MSS.
		mtu = links[0].MTU
	}
	args := []string{"ip", "-o", "-4", "route", "show", "table", "220"}
	if device != "" {
		args = []string{"ip", "-o", "-4", "route", "show", "table", "all", "dev", device}
	}
	data, err := a.exec(ctx, c, args...)
	if err != nil {
		return n
	}
	if !networkRoutesMatch(string(data), mtu, p.Backend.Type == profile.L2TP) {
		n.Code = "network_settings_mismatch"
		return n
	}
	n.EffectiveMTU, n.EffectiveMSS = mtu, mtu-40
	n.Verified, n.Code = true, "network_settings_applied"
	return n
}

// Inspect every traffic route, not only the health endpoint. Throw/bypass
// routes carry no tunnel traffic and intentionally have no packet-size metrics.
func networkRoutesMatch(data string, mtu int, ppp bool) bool {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "throw", "unreachable", "prohibit", "blackhole", "local", "broadcast":
			continue
		}
		gotMTU, gotMSS := 0, 0
		for i, field := range fields {
			if field != "mtu" && field != "advmss" {
				continue
			}
			if i+1 >= len(fields) {
				return false
			}
			value, err := strconv.Atoi(fields[i+1])
			if err != nil {
				return false
			}
			if field == "mtu" {
				gotMTU = value
			} else {
				gotMSS = value
			}
		}
		if ppp {
			if gotMTU != 0 && gotMTU != mtu || gotMSS != 0 && gotMSS != mtu-40 {
				return false
			}
		} else if gotMTU != mtu || gotMSS != mtu-40 {
			return false
		}
		count++
	}
	return count > 0
}
