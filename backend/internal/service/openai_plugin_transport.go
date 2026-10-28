package service

import (
	"context"
	"net/http"
)

type codexOAuthRustEligibilityKey struct{}

// MarkOpenAICodexOAuthRequest opts only validated OAuth account requests into
// the admin's global Rust mode. API-key calls with the same URL stay on Go.
func MarkOpenAICodexOAuthRequest(request *http.Request, account *Account) *http.Request {
	if request == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return request
	}
	ctx := context.WithValue(request.Context(), codexOAuthRustEligibilityKey{}, true)
	return request.WithContext(ctx)
}

func IsOpenAICodexOAuthRequest(ctx context.Context) bool {
	return ctx != nil && ctx.Value(codexOAuthRustEligibilityKey{}) == true
}

// An opted-in Rust transport owns the HTTP/SSE network path for the selected
// account, including when the legacy OAuth outbound plugin has a rollout.
// The optional interface keeps the shared HTTPUpstream interface unchanged.
func useOpenAICodexRustTransport(upstream HTTPUpstream, request *http.Request, account *Account) bool {
	if upstream == nil || request == nil || account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return false
	}
	checker, ok := upstream.(interface {
		UseCodexRustFor(*http.Request, int64) bool
	})
	return ok && checker.UseCodexRustFor(request, account.ID)
}

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	request = MarkOpenAICodexOAuthRequest(request, account)
	ApplyOpenAIOutboundTimezone(request)
	if useOpenAICodexRustTransport(s.httpUpstream, request, account) {
		return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	request = MarkOpenAICodexOAuthRequest(request, account)
	ApplyOpenAIOutboundTimezone(request)
	if useOpenAICodexRustTransport(s.httpUpstream, request, account) {
		return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
