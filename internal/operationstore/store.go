// Package operationstore journals bounded non-secret lifecycle metadata in bbolt.
package operationstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	bolt "go.etcd.io/bbolt"
)

const (
	MaxOperations  = 512
	MaxConnections = 128
	maxBytes       = 1024 * 1024
)

var ErrStorage = errors.New("operation persistence unavailable; restart and reconcile before retrying")

type State struct {
	SchemaVersion int                             `json:"schema_version"`
	Connections   map[string]operation.Connection `json:"connections"`
	Operations    map[string]operation.Record     `json:"operations"`
}

func Empty() State {
	return State{SchemaVersion: 1, Connections: map[string]operation.Connection{}, Operations: map[string]operation.Record{}}
}
func (s State) Clone() State {
	r := Empty()
	for k, c := range s.Connections {
		r.Connections[k] = c
	}
	for k, o := range s.Operations {
		if o.Challenge != nil {
			c := *o.Challenge
			o.Challenge = &c
		}
		r.Operations[k] = o
	}
	return r
}
func (s State) Validate() error {
	if s.SchemaVersion != 1 || s.Connections == nil || s.Operations == nil || len(s.Connections) > MaxConnections || len(s.Operations) > MaxOperations {
		return ErrStorage
	}
	for id, o := range s.Operations {
		if o.ID != id || o.Validate() != nil {
			return ErrStorage
		}
	}
	for name, c := range s.Connections {
		if c.Profile != name || c.Validate() != nil {
			return ErrStorage
		}
		if c.ActiveOperation != "" {
			o, ok := s.Operations[c.ActiveOperation]
			if !ok || o.Profile != name || o.State.Terminal() || c.LastOperation != c.ActiveOperation || o.Kind == operation.DisconnectConnection && c.Desired != operation.Disconnected || o.Kind != operation.DisconnectConnection && c.Desired != operation.Connected {
				return ErrStorage
			}
		}
		if c.LastOperation != "" {
			o, ok := s.Operations[c.LastOperation]
			if !ok || o.Profile != name {
				return ErrStorage
			}
		}
	}
	for _, o := range s.Operations {
		if !o.State.Terminal() {
			c, ok := s.Connections[o.Profile]
			if !ok || c.ActiveOperation != o.ID {
				return ErrStorage
			}
		}
	}
	return nil
}

type Journal interface {
	Load() (State, error)
	Save(State) error
	Close() error
}
type Store struct{ db *bolt.DB }

func Open(root string) (*Store, error) {
	if privatefile.Dir(root) != nil {
		return nil, ErrStorage
	}
	path := filepath.Join(root, "operations.db")
	info, statErr := os.Lstat(path)
	existed := statErr == nil
	if existed && info.Size() == 0 {
		return nil, ErrStorage
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, ErrStorage
	}
	options := &bolt.Options{Timeout: time.Second, MaxSize: 32 * 1024 * 1024, OpenFile: func(path string, flags int, mode os.FileMode) (*os.File, error) {
		file, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW, mode)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || !privatefile.Owned(info) || info.Mode().Perm()&0077 != 0 {
			file.Close()
			return nil, ErrStorage
		}
		return file, nil
	}}
	db, err := bolt.Open(path, 0600, options)
	if err != nil {
		return nil, ErrStorage
	}
	store := &Store{db: db}
	if privatefile.SyncDir(root) != nil {
		db.Close()
		return nil, ErrStorage
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if existed {
			bucket := tx.Bucket([]byte("lifecycle-v1"))
			if bucket == nil || bucket.Get([]byte("state")) == nil {
				return ErrStorage
			}
			return nil
		}
		bucket, err := tx.CreateBucketIfNotExists([]byte("lifecycle-v1"))
		if err != nil {
			return err
		}
		if bucket.Get([]byte("state")) == nil {
			data, _ := json.Marshal(Empty())
			return bucket.Put([]byte("state"), data)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	if _, err = store.Load(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}
func (s *Store) Load() (State, error) {
	state := Empty()
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("lifecycle-v1"))
		if bucket == nil {
			return ErrStorage
		}
		data := bucket.Get([]byte("state"))
		if jsonstrict.Decode(data, &state, maxBytes) != nil {
			return ErrStorage
		}
		return state.Validate()
	})
	if err != nil {
		return State{}, ErrStorage
	}
	return state, nil
}
func (s *Store) Save(state State) error {
	if state.Validate() != nil {
		return ErrStorage
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > maxBytes {
		return ErrStorage
	}
	if s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("lifecycle-v1")).Put([]byte("state"), data) }) != nil {
		return ErrStorage
	}
	return nil
}
func (s *Store) Close() error {
	if s.db.Close() != nil {
		return ErrStorage
	}
	return nil
}
