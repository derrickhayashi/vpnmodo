package managedvpn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

const backendLabel = "io.vpnmodo.backend"
const revisionLabel = "io.vpnmodo.revision"
const DefaultXAuthImage = "vpnmodo-xauth:dev"
const DefaultImage = "vpnmodo-l2tp:dev"
const DefaultApplicationImage = "alpine:3.22"

type deployment struct {
	SchemaVersion    int             `json:"schema_version"`
	Profile          profile.Profile `json:"profile"`
	Revision         uint64          `json:"revision"`
	Token            string          `json:"token"`
	VPNImage         string          `json:"vpn_image"`
	ApplicationImage string          `json:"application_image"`
}

func (d deployment) labels() map[string]string {
	id, _ := profile.Identity(d.Profile.Name)
	id.Labels[backendLabel] = string(d.Profile.Backend.Type)
	id.Labels[revisionLabel] = strconv.FormatUint(d.Revision, 10)
	return id.Labels
}
func manifest(d deployment, root string) ([]byte, error) {
	id, _ := profile.Identity(d.Profile.Name)
	c := profile.L2TPConfig{}
	if d.Profile.Backend.Type == profile.L2TP {
		c = profile.EffectiveL2TPConfig(*d.Profile.Backend.L2TP)
	} else {
		x := profile.EffectiveXAuthConfig(*d.Profile.Backend.XAuth)
		force := true
		c = profile.L2TPConfig{Server: x.Server, DNS: x.DNS, IKEProposals: x.IKEProposals, ESPProposals: x.ESPProposals, ForceEncapsulation: &force}
	}
	force := "no"
	if *c.ForceEncapsulation {
		force = "yes"
	}
	env := map[string]string{"VPN_SERVER": c.Server, "VPN_HEALTH_HOST": d.Profile.Health.Host, "VPN_HEALTH_PORT": strconv.Itoa(d.Profile.Health.Port), "VPN_DNS": strings.Join(c.DNS, ","), "VPN_FORCE_ENCAPS": force, "VPN_IKE_PROPOSALS": strings.Join(c.IKEProposals, ","), "VPN_ESP_PROPOSALS": strings.Join(c.ESPProposals, ","), "VPN_RECOVERY_OWNER": "controller"}
	secrets := map[string]any{}
	for _, key := range []string{"username", "password", "psk"} {
		env["VPN_"+strings.ToUpper(key)+"_FILE"] = "/run/secrets/vpn_" + key
		secrets["vpn_"+key] = map[string]string{"file": filepath.Join(root, "credentials", d.Token, key)}
	}
	model := map[string]any{"name": id.Project, "services": map[string]any{
		"vpn":         map[string]any{"image": d.VPNImage, "labels": d.labels(), "cap_add": []string{"NET_ADMIN", "NET_RAW"}, "devices": []string{"/dev/ppp:/dev/ppp"}, "sysctls": map[string]string{"net.ipv6.conf.all.disable_ipv6": "1", "net.ipv6.conf.default.disable_ipv6": "1"}, "environment": env, "secrets": []string{"vpn_username", "vpn_password", "vpn_psk"}, "restart": "no", "tmpfs": []string{"/run:rw,nosuid,nodev,mode=0755"}, "logging": map[string]any{"driver": "none"}, "healthcheck": map[string]any{"test": []string{"CMD", "/usr/local/bin/vpn-tunnelcheck"}, "interval": "10s", "timeout": "5s", "retries": 2, "start_period": "150s"}},
		"application": map[string]any{"image": d.ApplicationImage, "labels": d.labels(), "network_mode": "service:vpn", "user": strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "volumes": []any{map[string]any{"type": "bind", "source": filepath.Join(root, "resolv.conf"), "target": "/etc/resolv.conf", "read_only": true, "bind": map[string]bool{"create_host_path": false}}}, "restart": "no", "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "logging": map[string]any{"driver": "none"}, "command": []string{"sleep", "infinity"}},
	}, "secrets": secrets, "networks": map[string]any{"default": map[string]any{"labels": d.labels()}}}
	if d.Profile.Backend.Type == profile.XAuth {
		services := model["services"].(map[string]any)
		vpn := services["vpn"].(map[string]any)
		delete(vpn, "devices")
		vpn["init"] = true
		vpn["dns"] = []string{"127.0.0.1"}
		vpn["entrypoint"] = []string{"/usr/local/bin/vpn-managed-xauth"}
		vpn["read_only"] = true
		mounts := []any{map[string]any{"type": "bind", "source": filepath.Join(root, "xauth-resolv.conf"), "target": "/etc/resolv.conf", "read_only": true, "bind": map[string]bool{"create_host_path": false}}}
		services["application"].(map[string]any)["volumes"] = mounts
		vpn["volumes"] = mounts
		vpn["security_opt"] = []string{"no-new-privileges:true"}
		vpn["tmpfs"] = []string{"/run:rw,nosuid,nodev,mode=0755", "/tmp:rw,nosuid,nodev,noexec,mode=1777"}
		// IKE bypass is root-only; workloads always use an unprivileged UID.
		services["application"].(map[string]any)["user"] = "65534:65534"
	}
	// Compose interpolates dollar signs even in JSON. All manifest strings are data.
	return json.Marshal(escape(model))
}
func escape(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "$", "$$")
	case map[string]string:
		r := map[string]string{}
		for k, s := range x {
			r[k] = strings.ReplaceAll(s, "$", "$$")
		}
		return r
	case map[string]any:
		r := map[string]any{}
		for k, s := range x {
			r[k] = escape(s)
		}
		return r
	case []any:
		r := make([]any, len(x))
		for i, s := range x {
			r[i] = escape(s)
		}
		return r
	case []string:
		r := make([]string, len(x))
		for i, s := range x {
			r[i] = strings.ReplaceAll(s, "$", "$$")
		}
		return r
	}
	return v
}
