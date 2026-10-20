package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/upgrade"
)

func upgradeCommand(ctx context.Context, registry string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	check := flags.Bool("check", false, "")
	tag := flags.String("version", "", "")
	pre := flags.Bool("prerelease", false, "")
	tokenFile := flags.String("github-token-file", "", "")
	resume := flags.Bool("resume", false, "")
	rollback := flags.Bool("rollback", false, "")
	hostName := flags.String("host", host.ManagedName, "")
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(out, "Usage: vpnmodo upgrade [--check] [--version TAG] [--prerelease] [--github-token-file FILE] [--host NAME]\n       vpnmodo upgrade {--resume|--rollback}\nUpdates the CLI and controller in the existing VM. Running VPN containers keep their images until the next explicit reconnect.")
		return nil
	}
	if flags.Parse(args) != nil || len(flags.Args()) != 0 || *resume && *rollback || (*resume || *rollback) && (*check || *tag != "" || *pre || *tokenFile != "") {
		return errors.New("invalid upgrade options; run vpnmodo upgrade --help")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("in-place upgrade currently supports macOS arm64 installations")
	}
	if registry == "" {
		var err error
		registry, err = host.DefaultRegistry()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(registry) {
		return errors.New("use an absolute host registry path")
	}
	root := filepath.Join(filepath.Dir(registry), "upgrades")
	if err := privatefile.Dir(root); err != nil {
		return err
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(root, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	installer := upgrade.Installer{Root: root, Remote: host.Upgrade}
	if *resume || *rollback {
		if err = installer.Recover(ctx, *rollback); err != nil {
			return err
		}
		fmt.Fprintln(out, "Pending upgrade resolved. VPN containers were preserved.")
		return nil
	}
	if _, err = installer.Pending(); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return upgrade.ErrPending
	}
	token := ""
	if *tokenFile != "" {
		b, err := privatefile.Read(*tokenFile, 4096)
		if err != nil {
			return errors.New("GitHub token file must be private and owned by this user")
		}
		token = string(b)
		for len(token) > 0 && (token[len(token)-1] == '\n' || token[len(token)-1] == '\r') {
			token = token[:len(token)-1]
		}
	}
	client := upgrade.NewClient(token)
	release, err := client.Resolve(ctx, *tag, *pre)
	if err != nil {
		return err
	}
	m, err := client.Manifest(ctx, release, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed CLI: %s\nSelected release: %s\n", version, m.CLIVersion)
	if *check {
		fmt.Fprintln(out, "Release metadata verified. No installation changes made.")
		return nil
	}
	h, err := host.Find(registry, *hostName)
	if err != nil {
		return err
	}
	binary, err := client.Binary(ctx, release, m)
	if err != nil {
		return err
	}
	target, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Staging verified images and backing up controller state. Running VPNs remain connected.")
	if err = installer.Start(ctx, h, target, m, binary); err != nil {
		return err
	}
	fmt.Fprintf(out, "Upgraded to %s. Existing VPNs use the new images on their next explicit reconnect.\n", m.CLIVersion)
	return nil
}
