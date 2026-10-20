package eventlog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

func example() Event {
	return Event{Profile: "example", Backend: profile.XAuth, Kind: "diagnostic", Reason: "peer_delete"}
}
func TestPersistencePaginationDedupAndPrivateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := example()
	e.SourceID = "01234567-0123-0123-0123-0123456789ab:0"
	for i := 0; i < 2; i++ {
		if err = s.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err = s.Append(example()); err != nil {
			t.Fatal(err)
		}
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Append(e); err != nil {
		t.Fatal(err)
	}
	first, err := s.Query(Query{Profile: "example", Limit: 2})
	if err != nil || len(first.Events) != 2 || first.NextBefore == 0 {
		t.Fatal(first, err)
	}
	second, err := s.Query(Query{Profile: "example", Limit: 2, Before: first.NextBefore})
	if err != nil || len(second.Events) != 2 || second.NextBefore != 0 || second.Events[0].Sequence >= first.Events[1].Sequence {
		t.Fatal(second, err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "events.jsonl")} {
		i, err := os.Stat(path)
		if err != nil || i.Mode().Perm()&0077 != 0 {
			t.Fatal("public storage", err)
		}
	}
	empty, err := s.Query(Query{Profile: "other"})
	if err != nil || len(empty.Events) != 0 {
		t.Fatal(empty, err)
	}
}
func TestRotationAndRetention(t *testing.T) {
	now := time.Now().UTC()
	s, err := open(filepath.Join(t.TempDir(), "logs"), 512, 3, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if err = s.Append(example()); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := os.ReadDir(s.dir)
	if len(files) > 3 {
		t.Fatal("unbounded files", len(files))
	}
	for _, f := range files {
		i, _ := f.Info()
		if i.Size() > 512 {
			t.Fatal("unbounded file")
		}
	}
	result, err := s.Query(Query{Profile: "example"})
	if err != nil || len(result.Events) == 0 || len(result.Events) >= 40 {
		t.Fatal(result, err)
	}
	now = now.Add(Retention + time.Second)
	result, err = s.Query(Query{Profile: "example"})
	if err != nil || len(result.Events) != 0 {
		t.Fatal(result, err)
	}
	files, _ = os.ReadDir(s.dir)
	if len(files) != 0 {
		t.Fatal("expired events retained")
	}
	if err = s.Append(example()); err != nil {
		t.Fatal(err)
	}
}
func TestRejectSensitiveFieldsAndUnsafeStorage(t *testing.T) {
	for _, mutate := range []func(*Event){func(e *Event) { e.Reason = "secret-password" }, func(e *Event) { e.Health = "private-error" }, func(e *Event) { e.SourceID = "secret-otp" }, func(e *Event) { e.State = "private-error" }, func(e *Event) { e.OperationID = "secret-code" }, func(e *Event) { e.Backend = "private-value" }} {
		s, _ := Open(filepath.Join(t.TempDir(), "logs"))
		e := example()
		mutate(&e)
		if s.Append(e) == nil || s.Ready() {
			t.Fatal("sensitive metadata accepted")
		}
		b, _ := os.ReadFile(s.path(0))
		if bytes.Contains(b, []byte("secret")) || bytes.Contains(b, []byte("private-")) {
			t.Fatal("sensitive metadata persisted")
		}
	}
	for _, mode := range []string{"symlink", "public", "directory"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			os.WriteFile(target, []byte("unchanged"), 0600)
			dir := filepath.Join(root, "logs")
			os.Mkdir(dir, 0700)
			path := filepath.Join(dir, "events.jsonl")
			switch mode {
			case "symlink":
				os.Symlink(target, path)
			case "public":
				os.WriteFile(path, nil, 0644)
			case "directory":
				os.Mkdir(path, 0700)
			}
			if _, err := Open(dir); err == nil {
				t.Fatal("unsafe storage accepted")
			}
			b, _ := os.ReadFile(target)
			if string(b) != "unchanged" {
				t.Fatal("symlink target changed")
			}
		})
	}
}
func TestCrashTailRepairAndConcurrentAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	s, _ := Open(dir)
	s.Append(example())
	f, _ := os.OpenFile(s.path(0), os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"partial":`)
	f.Close()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := s.Append(example()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	result, err := s.Query(Query{Profile: "example", Limit: 100})
	if err != nil || len(result.Events) != 41 {
		t.Fatal(len(result.Events), err)
	}
	for i, e := range result.Events {
		if e.Sequence != uint64(41-i) {
			t.Fatal("sequence ordering")
		}
	}
}
func TestBoundedQueriesAndCorruption(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "logs"))
	for _, q := range []Query{{Profile: "../escape"}, {Profile: "example", Limit: -1}, {Profile: "example", Limit: 501}} {
		if _, err := s.Query(q); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	s.Append(example())
	os.WriteFile(s.path(0), []byte(fmt.Sprintf("%s\n", strings.Repeat("x", 300))), 0600)
	if _, err := s.Query(Query{Profile: "example"}); err == nil || s.Ready() {
		t.Fatal("corruption hidden")
	}
}
