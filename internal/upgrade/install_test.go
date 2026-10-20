package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
)

func TestTransactionResumeAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "rollback"}[rollback], func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			target := filepath.Join(root, "vpnmodo")
			old := []byte("previous binary")
			os.WriteFile(target, old, 0700)
			candidate := []byte("#!/bin/sh\nprintf 'v1.2.3+git.0123456789ab\\n'\n")
			m := fixture(candidate)
			calls := []string{}
			failCommit := true
			remote := func(_ context.Context, _ host.Host, p host.UpgradeParams) (host.UpgradeResult, error) {
				calls = append(calls, p.Action)
				if p.Action == "commit" && failCommit {
					return host.UpgradeResult{}, errors.New("synthetic transport interruption")
				}
				phase := "prepared"
				if p.Action == "commit" {
					phase = "committed"
				}
				if p.Action == "rollback" {
					phase = "rolled_back"
				}
				return host.UpgradeResult{OK: true, Phase: phase}, nil
			}
			i := Installer{Root: root, Remote: remote}
			h := host.Host{Name: "example", SSH: "synthetic"}
			if err := i.Start(context.Background(), h, target, m, candidate); !errors.Is(err, ErrPending) {
				t.Fatal(err)
			}
			if _, err := i.Pending(); err != nil {
				t.Fatal(err)
			}
			if err := i.Start(context.Background(), h, target, m, candidate); !errors.Is(err, ErrPending) {
				t.Fatal("overwrote pending transaction", err)
			}
			failCommit = false
			if err := i.Recover(context.Background(), rollback); err != nil {
				t.Fatal(err)
			}
			want := candidate
			if rollback {
				want = old
			}
			got, _ := os.ReadFile(target)
			if string(got) != string(want) {
				t.Fatal("wrong installed binary")
			}
			if _, err := i.Pending(); !os.IsNotExist(err) {
				t.Fatal("pending marker retained", err)
			}
			if !reflect.DeepEqual(calls[:2], []string{"prepare", "commit"}) {
				t.Fatal(calls)
			}
		})
	}
}
func TestExecutableRefusesSymlinkAndBadDigest(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	target := filepath.Join(root, "target")
	os.WriteFile(target, []byte("original"), 0700)
	link := filepath.Join(root, "link")
	os.Symlink(target, link)
	if AtomicExecutable(link, []byte("wrong")) == nil {
		t.Fatal("followed destination symlink")
	}
	i := Installer{Root: root, Remote: func(context.Context, host.Host, host.UpgradeParams) (host.UpgradeResult, error) {
		t.Fatal("remote called before integrity check")
		return host.UpgradeResult{}, nil
	}}
	if i.Start(context.Background(), host.Host{Name: "example", SSH: "synthetic"}, target, fixture([]byte("expected")), []byte("tampered")) == nil {
		t.Fatal("accepted mismatched digest")
	}
	if _, err := privatefile.Read(target, 100); err != nil {
		t.Fatal(err)
	}
}
