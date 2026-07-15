package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallLogExcludedUsernames(t *testing.T) {
	SetCallLogExcludedUsernames(" ryan, Alice ")
	t.Cleanup(func() { SetCallLogExcludedUsernames("ryan") })

	require.True(t, IsCallLogExcludedUsername("ryan"))
	require.True(t, IsCallLogExcludedUsername("RYAN"))
	require.True(t, IsCallLogExcludedUsername(" alice "))
	require.False(t, IsCallLogExcludedUsername("bob"))
	require.False(t, IsCallLogExcludedUsername(""))
}
