package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/model"
)

type materializeCLIOptions struct {
	PollInterval  time.Duration
	Lease         time.Duration
	MaxJobs       int
	BackfillBatch int
	BacklogStatus bool
	Once          bool
}

func main() {
	options, err := parseMaterializeCLIOptions(os.Args[1:], os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMaterializeWorker(ctx, options); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func parseMaterializeCLIOptions(args []string, output io.Writer) (materializeCLIOptions, error) {
	options := materializeCLIOptions{}
	flags := flag.NewFlagSet("request-log-materialize", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.DurationVar(&options.PollInterval, "poll", 500*time.Millisecond, "delay when no materialization job is ready")
	flags.DurationVar(&options.Lease, "lease", 5*time.Minute, "time before an interrupted processing job may be reclaimed")
	flags.IntVar(&options.MaxJobs, "max-jobs", 0, "maximum jobs to claim before exiting, 0 means unlimited")
	flags.IntVar(&options.BackfillBatch, "backfill-batch", 500, "pending raw request logs to schedule when the durable queue is empty, 0 disables backfill")
	flags.BoolVar(&options.BacklogStatus, "backlog-status", false, "report recoverable and parent-only historical backlog, then exit")
	flags.BoolVar(&options.Once, "once", false, "exit when no job is immediately ready")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.PollInterval < 0 {
		return options, errors.New("poll must be greater than or equal to zero")
	}
	if options.Lease <= 0 {
		return options, errors.New("lease must be greater than zero")
	}
	if options.MaxJobs < 0 {
		return options, errors.New("max-jobs must be greater than or equal to zero")
	}
	if options.BackfillBatch < 0 {
		return options, errors.New("backfill-batch must be greater than or equal to zero")
	}
	return options, nil
}

func runMaterializeWorker(ctx context.Context, options materializeCLIOptions) error {
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
	if err := model.EnsureAPIRequestLogMaterializedTables(db); err != nil {
		return fmt.Errorf("initialize request log materialization tables: %w", err)
	}
	if options.BacklogStatus {
		status, err := model.GetAPIRequestLogMaterializationBacklogStatus(ctx, db)
		if err != nil {
			return fmt.Errorf("inspect request log materialization backlog: %w", err)
		}
		log.Printf(
			"backlog: recoverable=%d oldest_recoverable_age=%s parent_only=%d oldest_parent_only_age=%s",
			status.Recoverable,
			time.Duration(status.OldestRecoverableAgeSeconds)*time.Second,
			status.ParentOnly,
			time.Duration(status.OldestParentOnlyAgeSeconds)*time.Second,
		)
		return nil
	}
	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := model.ClaimAPIRequestLogMaterializationJob(ctx, db, model.APIRequestLogMaterializationWorkerOptions{Lease: options.Lease})
		if err != nil {
			return fmt.Errorf("claim request log materialization job: %w", err)
		}
		if job == nil {
			if options.BackfillBatch > 0 {
				backfill, err := model.ScheduleAPIRequestLogMaterializationBacklog(ctx, db, options.BackfillBatch)
				if err != nil {
					return fmt.Errorf("schedule request log materialization backlog: %w", err)
				}
				if backfill.Candidates > 0 {
					log.Printf("scheduled %d of %d recoverable request logs for materialization", backfill.Scheduled, backfill.Candidates)
					continue
				}
			}
			if options.Once {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(options.PollInterval):
			}
			continue
		}
		if err := model.ProcessAPIRequestLogMaterializationJob(ctx, db, job, nil); err != nil {
			log.Printf("request log materialization job %d for log %d will retry: %v", job.Id, job.LogId, err)
		}
		processed++
		if options.MaxJobs > 0 && processed >= options.MaxJobs {
			return nil
		}
	}
}
