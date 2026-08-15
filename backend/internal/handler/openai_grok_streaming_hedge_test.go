package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestShouldUseGrokStreamingHedge(t *testing.T) {
	enabled := &config.Config{Gateway: config.GatewayConfig{
		GrokStreamingHedge: config.GatewayGrokStreamingHedgeConfig{Enabled: true, DelaySeconds: 8},
	}}
	disabled := &config.Config{}
	grokAPIKey := &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey}
	grokOAuth := &service.Account{Platform: service.PlatformGrok, Type: service.AccountTypeOAuth}
	openAIAPIKey := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}

	require.True(t, shouldUseGrokStreamingHedge(enabled, true, service.PlatformGrok, grokAPIKey))
	require.False(t, shouldUseGrokStreamingHedge(disabled, true, service.PlatformGrok, grokAPIKey))
	require.False(t, shouldUseGrokStreamingHedge(enabled, false, service.PlatformGrok, grokAPIKey))
	require.False(t, shouldUseGrokStreamingHedge(enabled, true, service.PlatformGrok, grokOAuth))
	require.False(t, shouldUseGrokStreamingHedge(enabled, true, service.PlatformOpenAI, openAIAPIKey))
}
