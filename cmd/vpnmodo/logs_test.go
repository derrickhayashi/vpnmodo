package main

import (
	"testing"
	"time"
)

func TestLogQueryValidation(t *testing.T) {
	now := time.Now()
	q, m, err := logQuery([]string{"example", "--since", "24h", "--limit", "20", "--before", "42", "--json"}, now)
	if err != nil || !m || q.Limit != 20 || q.Before != 42 || !q.Since.Equal(now.Add(-24*time.Hour)) {
		t.Fatal(q, m, err)
	}
	q, _, err = logQuery([]string{"example", "--since", "2026-10-20T00:00:00Z"}, now)
	if err != nil || q.Since.IsZero() {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"../escape"}, {"example", "--since", "-1h"}, {"example", "--since", "9999h"}, {"example", "--since", "bad"}, {"example", "--limit", "501"}, {"example", "--limit", "0"}, {"example", "--before", "-1"}, {"example", "--unknown"}, {"example", "extra"}} {
		if _, _, err := logQuery(args, now); err == nil {
			t.Fatal("invalid options accepted", args)
		}
	}
}
