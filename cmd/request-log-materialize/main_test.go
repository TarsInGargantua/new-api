package main

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseMaterializeCLIOptions(t *testing.T) {
	options, err := parseMaterializeCLIOptions([]string{"-poll", "2s", "-lease", "3m", "-max-jobs", "10", "-backfill-batch", "200", "-backlog-status", "-once"}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, options.PollInterval)
	require.Equal(t, 3*time.Minute, options.Lease)
	require.Equal(t, 10, options.MaxJobs)
	require.Equal(t, 200, options.BackfillBatch)
	require.True(t, options.BacklogStatus)
	require.True(t, options.Once)
}

func TestParseMaterializeCLIOptionsRejectsInvalidValues(t *testing.T) {
	_, err := parseMaterializeCLIOptions([]string{"-lease", "0s"}, io.Discard)
	require.Error(t, err)
	_, err = parseMaterializeCLIOptions([]string{"-poll", "-1s"}, io.Discard)
	require.Error(t, err)
	_, err = parseMaterializeCLIOptions([]string{"-backfill-batch", "-1"}, io.Discard)
	require.Error(t, err)
}
