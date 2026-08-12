package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDefaultGrokStreamingHedgeConfig(t *testing.T) {
	resetViperWithJWTSecret(t)

	cfg, err := Load()

	require.NoError(t, err)
	require.False(t, cfg.Gateway.GrokStreamingHedge.Enabled)
	require.Equal(t, 8, cfg.Gateway.GrokStreamingHedge.DelaySeconds)
}

func TestValidateGrokStreamingHedgeDelay(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		delay   int
		wantErr bool
	}{
		{name: "disabled ignores zero delay", enabled: false, delay: 0},
		{name: "enabled rejects zero delay", enabled: true, delay: 0, wantErr: true},
		{name: "enabled accepts minimum delay", enabled: true, delay: 1},
		{name: "enabled accepts maximum delay", enabled: true, delay: 60},
		{name: "enabled rejects delay above maximum", enabled: true, delay: 61, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			cfg, err := Load()
			require.NoError(t, err)
			cfg.Gateway.GrokStreamingHedge = GatewayGrokStreamingHedgeConfig{
				Enabled:      test.enabled,
				DelaySeconds: test.delay,
			}

			err = cfg.Validate()
			if test.wantErr {
				require.ErrorContains(t, err, "gateway.grok_streaming_hedge.delay_seconds")
				return
			}
			require.NoError(t, err)
		})
	}
}
