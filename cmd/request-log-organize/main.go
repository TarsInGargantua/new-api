package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
)

type organizerCLIOptions struct {
	InitOnly       bool
	DryRun         bool
	BatchSize      int
	AfterID        int64
	MaxRows        int
	LagSeconds     int64
	Sleep          time.Duration
	IgnoreProgress bool
}

func main() {
	opts, err := parseOrganizerCLIOptions(os.Args[1:], os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if err := runOrganizer(context.Background(), opts); err != nil {
		log.Fatal(err)
	}
}

func parseOrganizerCLIOptions(args []string, output io.Writer) (organizerCLIOptions, error) {
	opts := organizerCLIOptions{}
	flags := flag.NewFlagSet("request-log-organize", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&opts.InitOnly, "init-only", false, "only initialize turn materialization tables")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "organize and report without writing turn tables")
	flags.IntVar(&opts.BatchSize, "batch-size", 500, "number of request-log parents per batch")
	flags.Int64Var(&opts.AfterID, "after-id", 0, "only process request-log parents with id greater than this value")
	flags.IntVar(&opts.MaxRows, "max-rows", 0, "maximum number of request-log parents to process, 0 means all")
	flags.Int64Var(&opts.LagSeconds, "lag-seconds", 300, "leave this many seconds of recent parents unorganized")
	flags.DurationVar(&opts.Sleep, "sleep", 0, "pause between committed batches, for example 250ms")
	flags.BoolVar(&opts.IgnoreProgress, "ignore-progress", false, "ignore saved progress and rescan from after-id")
	if err := flags.Parse(args); err != nil {
		return organizerCLIOptions{}, err
	}
	if flags.NArg() != 0 {
		return organizerCLIOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if opts.BatchSize <= 0 {
		return organizerCLIOptions{}, errors.New("batch-size must be greater than 0")
	}
	if opts.AfterID < 0 {
		return organizerCLIOptions{}, errors.New("after-id must be greater than or equal to 0")
	}
	if opts.MaxRows < 0 {
		return organizerCLIOptions{}, errors.New("max-rows must be greater than or equal to 0")
	}
	if opts.LagSeconds < 0 {
		return organizerCLIOptions{}, errors.New("lag-seconds must be greater than or equal to 0")
	}
	if opts.Sleep < 0 {
		return organizerCLIOptions{}, errors.New("sleep must be greater than or equal to 0")
	}
	if opts.InitOnly && opts.DryRun {
		return organizerCLIOptions{}, errors.New("init-only and dry-run cannot be used together")
	}
	return opts, nil
}

func runOrganizer(ctx context.Context, opts organizerCLIOptions) error {
	if strings.TrimSpace(os.Getenv("REQUEST_LOG_SQL_DSN")) == "" {
		return errors.New("REQUEST_LOG_SQL_DSN is required")
	}
	db, err := model.OpenAPIRequestLogOrganizerDB()
	if err != nil {
		return fmt.Errorf("open request log database: %w", err)
	}
	if sqlDB, sqlErr := db.DB(); sqlErr == nil {
		defer sqlDB.Close()
	}
	if opts.InitOnly {
		if err := model.EnsureAPIRequestLogMaterializedTables(db); err != nil {
			return fmt.Errorf("initialize turn tables: %w", err)
		}
		if err := model.EnsureAPIRequestLogOrganizerStateTable(db); err != nil {
			return fmt.Errorf("initialize organizer state table: %w", err)
		}
		log.Print("turn materialization tables initialized")
		return nil
	}

	stats, err := model.OrganizeAPIRequestLogTurns(ctx, db, model.APIRequestLogOrganizerOptions{
		BatchSize:      opts.BatchSize,
		AfterID:        opts.AfterID,
		MaxRows:        opts.MaxRows,
		LagSeconds:     opts.LagSeconds,
		Sleep:          opts.Sleep,
		DryRun:         opts.DryRun,
		IgnoreProgress: opts.IgnoreProgress,
	})
	if err != nil {
		return err
	}
	log.Printf(
		"done: exact=%d inferred=%d unknown=%d open=%d completed=%d requests=%d items=%d last_id=%d",
		stats.Exact, stats.Inferred, stats.Unknown, stats.Open, stats.Completed,
		stats.Requests, stats.Items, stats.LastID,
	)
	return nil
}
