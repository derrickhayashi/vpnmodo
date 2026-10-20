// Package eventlog stores only typed, allowlisted VPN metadata, never transcripts.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

var ErrStorage = errors.New("VPN event logging unavailable")
var ErrQuery = errors.New("invalid VPN log query")
var sourcePattern = regexp.MustCompile(`^([a-f0-9-]{36}:[0-9]{1,10}|[a-f0-9]{64}:exit)$`)

const MaxResults = 500
const Retention = 14 * 24 * time.Hour

type Event struct {
	Sequence    uint64                `json:"sequence"`
	At          time.Time             `json:"at"`
	Profile     string                `json:"profile"`
	Backend     profile.BackendType   `json:"backend,omitempty"`
	Kind        string                `json:"kind"`
	OperationID string                `json:"operation_id,omitempty"`
	Operation   operation.Kind        `json:"operation,omitempty"`
	State       operation.State       `json:"state,omitempty"`
	Failure     operation.Failure     `json:"failure,omitempty"`
	Health      operation.HealthState `json:"health,omitempty"`
	Observed    operation.Observed    `json:"observed,omitempty"`
	Desired     operation.Intent      `json:"desired,omitempty"`
	Reason      string                `json:"reason,omitempty"`
	SourceID    string                `json:"source_id,omitempty"`
	ExitCode    *int                  `json:"exit_code,omitempty"`
}

func DiagnosticReason(s string) bool {
	switch s {
	case "established", "peer_delete", "dpd_timeout", "rekey_failed", "authentication_failed", "client_exit", "ppp_down", "support_process_exit", "stopping", "container_oom", "container_exit", "tunnel_state_lost":
		return true
	}
	return false
}
func (e Event) valid() bool {
	if e.ExitCode != nil && (*e.ExitCode < 0 || *e.ExitCode > 255) {
		return false
	}
	if e.At.IsZero() || !profile.ValidName(e.Profile) || e.Backend != "" && e.Backend != profile.L2TP && e.Backend != profile.XAuth || e.OperationID != "" && !operation.ValidID(e.OperationID) || e.SourceID != "" && !sourcePattern.MatchString(e.SourceID) {
		return false
	}
	if e.Operation != "" && !e.Operation.Valid() || e.Failure != "" && !e.Failure.Valid() || e.Health != "" && !e.Health.Valid() {
		return false
	}
	switch e.State {
	case "", operation.Queued, operation.Running, operation.AwaitingMFA, operation.Verifying, operation.Succeeded, operation.Failed, operation.Cancelled:
	default:
		return false
	}
	switch e.Desired {
	case "", operation.Connected, operation.Disconnected:
	default:
		return false
	}
	switch e.Observed {
	case "", operation.Unknown, operation.ObservedConnected, operation.ObservedDisconnected:
	default:
		return false
	}
	switch e.Kind {
	case "operation", "health", "connection":
		return e.Reason == ""
	case "diagnostic":
		return DiagnosticReason(e.Reason)
	}
	return false
}

type Query struct {
	Profile string    `json:"profile"`
	Since   time.Time `json:"since"`
	Before  uint64    `json:"before,omitempty"`
	Limit   int       `json:"limit,omitempty"`
}

func (q Query) Validate() error {
	if !profile.ValidName(q.Profile) || q.Limit < 0 || q.Limit > MaxResults {
		return ErrQuery
	}
	return nil
}

type Result struct {
	Events     []Event `json:"events"`
	NextBefore uint64  `json:"next_before,omitempty"`
}
type Store struct {
	mu        sync.Mutex
	dir       string
	maxBytes  int
	files     int
	sequence  uint64
	seen      map[string]bool
	failed    bool
	disabled  bool
	clock     func() time.Time
	lastPrune time.Time
}

// Unavailable preserves controller lifecycle/teardown access if log storage is
// unsafe or corrupt at startup. It never resets or overwrites the failed files.
func Unavailable() *Store             { return &Store{failed: true, disabled: true} }
func Open(dir string) (*Store, error) { return open(dir, 10*1024*1024, 5, time.Now) }
func open(dir string, size, files int, clock func() time.Time) (*Store, error) {
	if privatefile.Dir(dir) != nil {
		return nil, ErrStorage
	}
	s := &Store{dir: dir, maxBytes: size, files: files, clock: clock, seen: map[string]bool{}}
	if err := s.prune(); err != nil {
		return nil, err
	}
	for i := files - 1; i >= 0; i-- {
		es, err := s.read(i)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if e.Sequence > s.sequence {
				s.sequence = e.Sequence
			}
			if e.SourceID != "" {
				s.seen[e.SourceID] = true
			}
		}
	}
	return s, nil
}
func (s *Store) path(i int) string {
	if i == 0 {
		return filepath.Join(s.dir, "events.jsonl")
	}
	return filepath.Join(s.dir, "events.jsonl."+strconv.Itoa(i))
}
func (s *Store) read(i int) ([]Event, error) {
	b, err := privatefile.Read(s.path(i), s.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		end := bytes.LastIndexByte(b, '\n') + 1
		f, e := os.OpenFile(s.path(i), os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return nil, ErrStorage
		}
		e = f.Truncate(int64(end))
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil || ce != nil {
			return nil, ErrStorage
		}
		b = b[:end]
	}
	var events []Event
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 4096), 4096)
	for scan.Scan() {
		var e Event
		if jsonstrict.Decode(scan.Bytes(), &e, 4096) != nil || !e.valid() || e.Sequence == 0 {
			return nil, ErrStorage
		}
		events = append(events, e)
	}
	if scan.Err() != nil {
		return nil, ErrStorage
	}
	return events, nil
}
func (s *Store) prune() error {
	cutoff := s.clock().Add(-Retention)
	for i := 0; i < s.files; i++ {
		es, err := s.read(i)
		if err != nil {
			return err
		}
		var data bytes.Buffer
		changed := false
		for _, e := range es {
			if e.At.Before(cutoff) {
				changed = true
				delete(s.seen, e.SourceID)
				continue
			}
			b, _ := json.Marshal(e)
			data.Write(b)
			data.WriteByte('\n')
		}
		if !changed {
			continue
		}
		if data.Len() == 0 {
			if os.Remove(s.path(i)) != nil {
				return ErrStorage
			}
			continue
		}
		f, err := os.CreateTemp(s.dir, ".events-")
		if err != nil {
			return ErrStorage
		}
		name := f.Name()
		_, err = f.Write(data.Bytes())
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil && closeErr == nil {
			err = os.Rename(name, s.path(i))
		}
		_ = os.Remove(name)
		if err != nil || closeErr != nil {
			return ErrStorage
		}
	}
	s.lastPrune = s.clock()
	return nil
}

// Maintain purges expired records even when no connection events are emitted.
func (s *Store) Maintain() error {
	if s.disabled {
		return ErrStorage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prune(); err != nil {
		s.failed = true
		return err
	}
	return nil
}
func (s *Store) Ready() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.failed }
func (s *Store) MarkFailed() { s.mu.Lock(); s.failed = true; s.mu.Unlock() }
func (s *Store) Append(e Event) (err error) {
	if s.disabled {
		return ErrStorage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if err != nil {
			s.failed = true
		}
	}()
	if e.At.IsZero() {
		e.At = s.clock().UTC()
	}
	if !e.valid() {
		return ErrStorage
	}
	e.At = e.At.UTC()
	if e.At.Before(s.clock().Add(-Retention)) {
		return nil
	}
	if e.At.After(s.clock().Add(time.Minute)) {
		return ErrStorage
	}
	if e.SourceID != "" && s.seen[e.SourceID] {
		return nil
	}
	if s.clock().Sub(s.lastPrune) >= time.Hour {
		if err = s.prune(); err != nil {
			return err
		}
	}
	e.Sequence = s.sequence + 1
	b, _ := json.Marshal(e)
	b = append(b, '\n')
	info, err := os.Lstat(s.path(0))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	if info != nil && (!info.Mode().IsRegular() || !privatefile.Owned(info) || info.Mode().Perm()&0077 != 0) {
		return ErrStorage
	}
	if len(b) > s.maxBytes {
		return ErrStorage
	}
	if info != nil && info.Size()+int64(len(b)) > int64(s.maxBytes) {
		// Validate every slot before rotation, including the file about to expire.
		for i := 0; i < s.files; i++ {
			if _, err = s.read(i); err != nil {
				return err
			}
		}
		expired, _ := s.read(s.files - 1)
		for _, x := range expired {
			delete(s.seen, x.SourceID)
		}
		if err = os.Remove(s.path(s.files - 1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrStorage
		}
		for i := s.files - 2; i >= 0; i-- {
			if err = os.Rename(s.path(i), s.path(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ErrStorage
			}
		}
	}
	f, err := os.OpenFile(s.path(0), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return ErrStorage
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !privatefile.Owned(fi) || fi.Mode().Perm()&0077 != 0 {
		f.Close()
		return ErrStorage
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil || ce != nil {
		return ErrStorage
	}
	if privatefile.SyncDir(s.dir) != nil {
		return ErrStorage
	}
	s.sequence = e.Sequence
	if e.SourceID != "" {
		s.seen[e.SourceID] = true
	}
	return nil
}
func (s *Store) Query(q Query) (result Result, err error) {
	result.Events = []Event{}
	if s.disabled {
		return result, ErrStorage
	}
	if q.Validate() != nil {
		return result, ErrQuery
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if err != nil {
			s.failed = true
		}
	}()
	if err = s.prune(); err != nil {
		return result, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 200
	}
	cutoff := s.clock().Add(-Retention)
	if q.Since.After(cutoff) {
		cutoff = q.Since
	}
	for i := 0; i < s.files; i++ {
		es, e := s.read(i)
		if e != nil {
			return result, e
		}
		for j := len(es) - 1; j >= 0; j-- {
			x := es[j]
			if x.Profile != q.Profile || x.At.Before(cutoff) || q.Before != 0 && x.Sequence >= q.Before {
				continue
			}
			if len(result.Events) == limit {
				result.NextBefore = result.Events[len(result.Events)-1].Sequence
				return result, nil
			}
			result.Events = append(result.Events, x)
		}
	}
	return result, nil
}
