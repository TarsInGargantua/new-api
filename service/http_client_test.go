package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/require"
)

func TestRelayHTTPTransportUsesConfiguredResponseHeaderTimeout(t *testing.T) {
	oldTimeout := common.RelayResponseHeaderTimeout
	common.RelayResponseHeaderTimeout = 30
	t.Cleanup(func() { common.RelayResponseHeaderTimeout = oldTimeout })

	transport := newRelayHTTPTransport()
	require.Equal(t, 30*time.Second, transport.ResponseHeaderTimeout)
}
