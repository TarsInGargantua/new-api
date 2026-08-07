package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayRetrySkipsOnlyAnIdenticalUpstreamTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, "https://packy.example")
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "shared-key")

	require.False(t, isDuplicateRelayUpstreamTarget(ctx))
	require.True(t, isDuplicateRelayUpstreamTarget(ctx))

	common.SetContextKey(ctx, constant.ContextKeyChannelKey, "standby-key")
	require.False(t, isDuplicateRelayUpstreamTarget(ctx))

	common.SetContextKey(ctx, constant.ContextKeyChannelBaseUrl, "https://standby.example")
	require.False(t, isDuplicateRelayUpstreamTarget(ctx))
}
