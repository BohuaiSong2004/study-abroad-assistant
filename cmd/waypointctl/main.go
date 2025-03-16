// Command waypointctl is the operator CLI for the timeline service's event
// sync: consumer status, dead letters and re-planning.
//
//	waypointctl status
//	waypointctl dlq list [-consumer NAME]
//	waypointctl dlq replay CONSUMER SEQ
//	waypointctl resync [-student ID]
//
// It talks to TIMELINE_ADDR (default dns:///localhost:9093) with
// INTERNAL_TOKEN, acting as the internal service "waypointctl".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/svc"
)

const usage = `usage:
  waypointctl status
  waypointctl dlq list [-consumer NAME]
  waypointctl dlq replay CONSUMER SEQ
  waypointctl resync [-student ID]`

func main() {
	cc, err := platform.Dial(platform.Env("TIMELINE_ADDR", "dns:///localhost:9093"), platform.Env("INTERNAL_TOKEN", svc.DevInternalToken))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer func() { _ = cc.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), platform.EnvDuration("WAYPOINTCTL_TIMEOUT", 5*time.Minute))
	defer cancel()
	c := clients{sync: timelinev1.NewTimelineServiceClient(cc), admin: timelinev1.NewSyncAdminServiceClient(cc)}
	if err := run(platform.ServiceContext(ctx, "waypointctl"), c, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "waypointctl:", err)
		if errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type clients struct {
	sync  timelinev1.TimelineServiceClient
	admin timelinev1.SyncAdminServiceClient
}

var errUsage = errors.New("invalid arguments")

func run(ctx context.Context, c clients, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	err := command(ctx, c, args, tw)
	if ferr := tw.Flush(); err == nil {
		err = ferr
	}
	return err
}

// command writes to a tabwriter; write errors surface from its Flush.
//
//nolint:errcheck // see above
func command(ctx context.Context, c clients, args []string, tw *tabwriter.Writer) error {
	switch args[0] {
	case "status":
		res, err := c.sync.GetSyncStatus(ctx, &timelinev1.GetSyncStatusRequest{})
		if err != nil {
			return err
		}
		fmt.Fprintln(tw, "STREAM\tCURSOR\tHEAD\tLAG\tCONNECTED\tLEADER\tAPPLIED\tPARKED")
		for _, s := range res.Streams {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%t\t%t\t%d\t%d\n", s.Stream, s.Cursor, s.HeadSeq, max(s.HeadSeq-s.Cursor, 0), s.Connected, s.Leader, s.EventsApplied, s.DeadLetters)
		}
		return nil
	case "dlq":
		if len(args) < 2 {
			return errUsage
		}
		switch args[1] {
		case "list":
			fs := flag.NewFlagSet("dlq list", flag.ContinueOnError)
			consumer := fs.String("consumer", "", "only this consumer (timeline.catalog, timeline.applications)")
			if err := fs.Parse(args[2:]); err != nil {
				return errUsage
			}
			res, err := c.admin.ListDeadLetters(ctx, &timelinev1.ListDeadLettersRequest{Consumer: *consumer})
			if err != nil {
				return err
			}
			if len(res.DeadLetters) == 0 {
				fmt.Fprintln(tw, "no dead letters")
				return nil
			}
			fmt.Fprintln(tw, "CONSUMER\tSEQ\tTYPE\tAGGREGATE\tPARKED\tERROR")
			for _, d := range res.DeadLetters {
				fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", d.Consumer, d.Seq, d.Type, d.AggregateId, d.ParkedAt.AsTime().UTC().Format(time.RFC3339), d.Error)
			}
			return nil
		case "replay":
			if len(args) != 4 {
				return errUsage
			}
			seq, err := strconv.ParseInt(args[3], 10, 64)
			if err != nil {
				return errUsage
			}
			res, err := c.admin.ReplayDeadLetter(ctx, &timelinev1.ReplayDeadLetterRequest{Consumer: args[2], Seq: seq})
			if err != nil {
				return err
			}
			if !res.Applied {
				return fmt.Errorf("still failing, left parked: %s", res.Error)
			}
			fmt.Fprintf(tw, "applied %s #%d\n", args[2], seq)
			return nil
		}
		return errUsage
	case "resync":
		fs := flag.NewFlagSet("resync", flag.ContinueOnError)
		student := fs.String("student", "", "only this student's timelines")
		if err := fs.Parse(args[1:]); err != nil {
			return errUsage
		}
		res, err := c.admin.Resync(ctx, &timelinev1.ResyncRequest{StudentId: *student})
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "examined %d timelines, re-planned %d\n", res.Timelines, res.Replanned)
		return nil
	}
	return errUsage
}
