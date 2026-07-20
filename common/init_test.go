package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIRequestLogAsyncWriteEnabledFromEnv(t *testing.T) {
	tests := []struct {
		name          string
		asyncWrite    string
		allowVolatile string
		want          bool
	}{
		{name: "both unset", want: false},
		{name: "legacy async flag only", asyncWrite: "true", want: false},
		{name: "volatile opt in only", allowVolatile: "true", want: false},
		{name: "both explicitly enabled", asyncWrite: "true", allowVolatile: "true", want: true},
		{name: "async explicitly disabled", asyncWrite: "false", allowVolatile: "true", want: false},
		{name: "volatile explicitly disabled", asyncWrite: "true", allowVolatile: "false", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("API_REQUEST_LOG_ASYNC_WRITE", tt.asyncWrite)
			t.Setenv("API_REQUEST_LOG_ALLOW_VOLATILE_ASYNC_WRITE", tt.allowVolatile)

			require.Equal(t, tt.want, APIRequestLogAsyncWriteEnabledFromEnv())
		})
	}
}
