package main

import (
	"io"
	"testing"
	"time"
)

func TestParseOrganizerCLIOptions(t *testing.T) {
	opts, err := parseOrganizerCLIOptions([]string{
		"-dry-run",
		"-batch-size", "25",
		"-after-id", "41",
		"-max-rows", "100",
		"-lag-seconds", "60",
		"-sleep", "250ms",
		"-ignore-progress",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseOrganizerCLIOptions() error = %v", err)
	}
	if !opts.DryRun || opts.InitOnly || !opts.IgnoreProgress {
		t.Fatalf("unexpected modes: %+v", opts)
	}
	if opts.BatchSize != 25 || opts.AfterID != 41 || opts.MaxRows != 100 || opts.LagSeconds != 60 || opts.Sleep != 250*time.Millisecond {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestParseOrganizerCLIOptionsRejectsInvalidValues(t *testing.T) {
	tests := [][]string{
		{"-batch-size", "0"},
		{"-after-id", "-1"},
		{"-max-rows", "-1"},
		{"-lag-seconds", "-1"},
		{"-sleep", "-1s"},
		{"-init-only", "-dry-run"},
		{"unexpected"},
	}
	for _, args := range tests {
		if _, err := parseOrganizerCLIOptions(args, io.Discard); err == nil {
			t.Fatalf("parseOrganizerCLIOptions(%v) unexpectedly succeeded", args)
		}
	}
}
