package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/eventlog"
	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

func logQuery(args []string, now time.Time) (eventlog.Query, bool, error) {
	var q eventlog.Query
	if len(args) == 0 {
		return q, false, errors.New("usage: vpnmodo logs PROFILE [--since 24h] [--limit 200] [--before SEQUENCE] [--json]")
	}
	q.Profile = args[0]
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	since := flags.String("since", "24h", "")
	limit := flags.Int("limit", 200, "")
	before := flags.Uint64("before", 0, "")
	machine := flags.Bool("json", false, "")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return q, false, eventlog.ErrQuery
	}
	duration, err := time.ParseDuration(*since)
	if err == nil {
		if duration <= 0 || duration > eventlog.Retention {
			return q, false, eventlog.ErrQuery
		}
		q.Since = now.Add(-duration).UTC()
	} else {
		q.Since, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			return q, false, eventlog.ErrQuery
		}
	}
	q.Limit = *limit
	q.Before = *before
	if q.Validate() != nil || q.Limit == 0 {
		return q, false, eventlog.ErrQuery
	}
	return q, *machine, nil
}
func logsCommand(base context.Context, registry string, args []string, out io.Writer) error {
	q, machine, err := logQuery(args, time.Now())
	if err != nil {
		return err
	}
	reference, err := managedReference(host.ReferencePath(registry), q.Profile)
	if err != nil {
		return err
	}
	q.Profile = reference.Profile
	h, err := host.Find(registry, host.ManagedName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()
	client, err := host.Connect(ctx, h)
	if err != nil {
		return err
	}
	defer client.Close()
	var result eventlog.Result
	if err = client.Call(ctx, protocol.ConnectionLogs, q, &result); err != nil {
		return err
	}
	if machine {
		return json.NewEncoder(out).Encode(result)
	}
	for _, e := range result.Events {
		b, _ := json.Marshal(e)
		if _, err = fmt.Fprintln(out, string(b)); err != nil {
			return err
		}
	}
	if result.NextBefore != 0 {
		_, err = fmt.Fprintf(out, "More events: vpnmodo logs %s --since %s --before %d\n", args[0], q.Since.Format(time.RFC3339), result.NextBefore)
	}
	return err
}
