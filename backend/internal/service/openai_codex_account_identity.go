package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexAccountIdentityNamespaceVersion = "v1"

const codexAccountIdentitySourceContextKey = "openai_codex_account_identity_source"

// prepareCodexAccountIdentitySource resolves credential shadows once per selected
// attempt. The handler reuses gin.Context across failover attempts, so every entry
// point overwrites the staged source before projecting outbound identity.
func (s *OpenAIGatewayService) prepareCodexAccountIdentitySource(ctx context.Context, c *gin.Context, account *Account, body []byte) (*Account, error) {
	source := account
	if account != nil && account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
		source = resolved
	}
	source = codexIdentityRelationsSourceForRequest(source, c, body)
	if c != nil {
		c.Set(codexAccountIdentitySourceContextKey, source)
	}
	return source, nil
}

func codexAccountIdentitySource(c *gin.Context, fallback *Account) *Account {
	if c != nil {
		if staged, ok := c.Get(codexAccountIdentitySourceContextKey); ok {
			if source, ok := staged.(*Account); ok && source != nil {
				if fallback != nil && fallback.ID != source.ID && codexIdentityRelationsEnabled(source) {
					copy := *source
					copy.Extra = maps.Clone(source.Extra)
					copy.Extra["codex_identity_relations_v1"] = false
					return &copy
				}
				return source
			}
		}
	}
	return fallback
}

// codexClientSessionID returns the client's own session header, before any
// outbound rewrite.
func codexClientSessionID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return extractClientSessionID(c.Request.Header)
}

// codexAccountIdentityNamespace returns a stable, credential-scoped namespace.
// Multiple local rows that use the same ChatGPT account intentionally share the
// same namespace. Setup tokens use an irreversible bearer fingerprint because
// they have no refresh lifecycle or imported account metadata. Refreshable OAuth
// otherwise falls back only to a persistent fingerprint seed: local row IDs are
// deployment-relative and must never become upstream identity.
func codexAccountIdentityNamespace(account *Account) string {
	if account == nil || !account.IsOpenAIOAuthLike() {
		return ""
	}
	if upstreamAccountID := strings.TrimSpace(account.GetChatGPTAccountID()); upstreamAccountID != "" {
		if upstreamUserID := strings.TrimSpace(account.GetCredential("chatgpt_user_id")); upstreamUserID != "" {
			return "chatgpt:" + upstreamAccountID + ":user:" + upstreamUserID
		}
		return "chatgpt:" + upstreamAccountID
	}
	if seed, ok := codexFingerprintSeed(account.Extra); ok {
		return "seed:" + seed
	}
	if account.Type == AccountTypeSetupToken {
		if token := strings.TrimSpace(account.GetOpenAIAccessToken()); token != "" {
			sum := sha256.Sum256([]byte("openai-setup-token:" + token))
			return fmt.Sprintf("setup-token:%x", sum[:16])
		}
	}
	return ""
}

// isolateOpenAIUpstreamSessionID preserves the existing API-key isolation while
// adding the selected OAuth credential namespace. A scheduler failover therefore
// cannot send the same session/conversation identity through two upstream accounts.
func isolateOpenAIUpstreamSessionID(apiKeyID int64, account *Account, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	namespace := codexAccountIdentityNamespace(account)
	if namespace == "" {
		return isolateOpenAISessionID(apiKeyID, raw)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("u%d:a%s:%s", apiKeyID, namespace, raw)))
	return fmt.Sprintf("%x", sum[:8])
}

func codexIdentityRelationsEnabled(account *Account) bool {
	if account == nil || account.GetCodexFingerprintMode() != codexFingerprintDevice {
		return false
	}
	enabled, _ := account.Extra["codex_identity_relations_v1"].(bool)
	return enabled
}

const codexIdentityRelationsCutoffExtraKey = "codex_identity_relations_v1_session_cutoff"

// Freeze projection for the attempt. A persisted cutoff keeps old or opaque
// session lineages on their original identities even after cache expiry/restart.
func codexIdentityRelationsSourceForRequest(account *Account, c *gin.Context, body []byte) *Account {
	if !codexIdentityRelationsEnabled(account) {
		return account
	}
	raw, configured := account.Extra[codexIdentityRelationsCutoffExtraKey]
	if !configured {
		return account
	}
	value, valid := raw.(string)
	cutoff, err := time.Parse(time.RFC3339Nano, value)
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	if valid && err == nil && cutoff.Nanosecond()%int(time.Millisecond) == 0 && codexIdentitySessionAfterCutoff(headers, body, cutoff) {
		return account
	}
	copy := *account
	copy.Extra = maps.Clone(account.Extra)
	copy.Extra["codex_identity_relations_v1"] = false
	return &copy
}

func codexIdentitySessionAfterCutoff(headers http.Header, body []byte, cutoff time.Time) bool {
	metadata := make([]gjson.Result, 0, 3)
	if raw := strings.TrimSpace(headers.Get(openAIWSTurnMetadataHeader)); raw != "" {
		value := gjson.Parse(raw)
		if !value.IsObject() {
			return false
		}
		metadata = append(metadata, value)
	}
	if len(body) > 0 {
		payload := openAIRequestPayloadView(body)
		if !payload.IsObject() {
			return false
		}
		if cm := payload.Get("client_metadata"); cm.Exists() {
			if !cm.IsObject() {
				return false
			}
			metadata = append(metadata, cm)
			if embedded := cm.Get(openAIWSTurnMetadataHeader); embedded.Exists() {
				if embedded.Type != gjson.String {
					return false
				}
				if raw := strings.TrimSpace(embedded.String()); raw != "" {
					value := gjson.Parse(raw)
					if !value.IsObject() {
						return false
					}
					metadata = append(metadata, value)
				}
			}
		}
	}
	fields := [...]string{"session_id", "session-id", "thread_id", "thread-id", "parent_thread_id", "x-codex-parent-thread-id", "forked_from_thread_id"}
	for _, field := range fields {
		if value := strings.TrimSpace(headers.Get(field)); value != "" && !codexIdentityUUIDv7AtOrAfter(value, cutoff) {
			return false
		}
	}
	canonical := extractClientSessionID(headers)
	fallback := ""
	for _, values := range metadata {
		for _, field := range fields {
			value := values.Get(field)
			if !value.Exists() {
				continue
			}
			if value.Type != gjson.String {
				return false
			}
			raw := strings.TrimSpace(value.String())
			if raw == "" {
				continue
			}
			if !codexIdentityUUIDv7AtOrAfter(raw, cutoff) {
				return false
			}
			if canonical == "" && (field == "session_id" || field == "session-id") {
				if fallback != "" && fallback != raw {
					return false
				}
				fallback = raw
			}
		}
	}
	if canonical == "" {
		canonical = fallback
	}
	return codexIdentityUUIDv7AtOrAfter(canonical, cutoff)
}

func codexIdentityUUIDv7AtOrAfter(raw string, cutoff time.Time) bool {
	id, err := uuid.Parse(raw)
	if err != nil || id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		return false
	}
	seconds, nanos := id.Time().UnixTime()
	return !time.Unix(seconds, nanos).Before(cutoff)
}

func scopeCodexAccountIdentityValue(account *Account, apiKeyID int64, kind, raw string) string {
	raw = strings.TrimSpace(raw)
	namespace := codexAccountIdentityNamespace(account)
	if raw == "" || namespace == "" {
		return raw
	}
	if codexIdentityRelationsEnabled(account) {
		if kind == "thread" {
			kind = "session"
		}
		if kind == "window" {
			if split := strings.LastIndexByte(raw, ':'); split > 0 {
				thread, number := raw[:split], raw[split+1:]
				_, threadErr := uuid.Parse(thread)
				_, numberErr := strconv.ParseUint(number, 10, 64)
				if threadErr == nil && numberErr == nil {
					return scopeCodexAccountIdentityValue(account, apiKeyID, "thread", thread) + ":" + number
				}
			}
		}
	}
	return deriveStableUUIDv4(fmt.Sprintf(
		"sub2api:codex-account-identity:%s:user:%d:account:%s:kind:%s:value:%s",
		codexAccountIdentityNamespaceVersion,
		apiKeyID,
		namespace,
		kind,
		raw,
	))
}

var codexAccountIdentityFields = []struct {
	name          string
	kind          string
	relationsOnly bool
}{
	{name: "installation_id", kind: "installation"},
	{name: "x-codex-installation-id", kind: "installation"},
	{name: "session_id", kind: "session"},
	{name: "session-id", kind: "session"},
	{name: "thread_id", kind: "thread"},
	{name: "thread-id", kind: "thread"},
	{name: "turn_id", kind: "turn"},
	{name: "turn-id", kind: "turn"},
	{name: "window_id", kind: "window"},
	{name: "x-codex-window-id", kind: "window"},
	{name: "x-client-request-id", kind: "request"},
	{name: "parent_thread_id", kind: "thread", relationsOnly: true},
	{name: "x-codex-parent-thread-id", kind: "thread", relationsOnly: true},
	{name: "forked_from_thread_id", kind: "thread", relationsOnly: true},
	{name: "parent_turn_id", kind: "turn", relationsOnly: true},
	{name: "root_turn_id", kind: "turn", relationsOnly: true},
}

func applyCodexAccountIdentityFields(values map[string]any, account *Account, apiKeyID int64) bool {
	if values == nil || codexAccountIdentityNamespace(account) == "" {
		return false
	}
	changed := false
	for _, field := range codexAccountIdentityFields {
		if field.relationsOnly && !codexIdentityRelationsEnabled(account) {
			continue
		}
		raw, ok := values[field.name].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		next := scopeCodexAccountIdentityValue(account, apiKeyID, field.kind, raw)
		if next != raw {
			values[field.name] = next
			changed = true
		}
	}
	return changed
}

func applyCodexAccountIdentityEmbeddedMetadata(values map[string]any, account *Account, apiKeyID int64) bool {
	raw, ok := values[openAIWSTurnMetadataHeader].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return false
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return false
	}
	if !applyCodexAccountIdentityFields(metadata, account, apiKeyID) {
		return false
	}
	rebuilt, err := marshalCodexTurnMetadata(metadata)
	if err != nil {
		return false
	}
	values[openAIWSTurnMetadataHeader] = string(rebuilt)
	return true
}

// codexPromptCacheKeyKind scopes a prompt_cache_key that names the client's
// session in the session namespace, so it stays equal to the scoped session.
// Codex derives the key from its session-id header, which matches
// client_metadata.session_id except on an ephemeral fork such as thread/sample:
// the fork names itself in the metadata but keeps its parent's header and key.
func codexPromptCacheKeyKind(promptCacheKey, bodySessionID, clientSessionID string) string {
	for _, sessionID := range []string{bodySessionID, clientSessionID} {
		if strings.TrimSpace(sessionID) != "" && promptCacheKey == sessionID {
			return "session"
		}
	}
	return "prompt-cache"
}

func applyCodexAccountIdentityClientMetadataMap(requestBody map[string]any, account *Account, apiKeyID int64, clientSessionID string) bool {
	if requestBody == nil || codexAccountIdentityNamespace(account) == "" {
		return false
	}
	changed := false
	clientMetadata, _ := requestBody["client_metadata"].(map[string]any)
	originalBodySessionID := ""
	if clientMetadata != nil {
		originalBodySessionID, _ = clientMetadata["session_id"].(string)
		if applyCodexAccountIdentityFields(clientMetadata, account, apiKeyID) {
			changed = true
		}
		if applyCodexAccountIdentityEmbeddedMetadata(clientMetadata, account, apiKeyID) {
			changed = true
		}
	}
	if raw, ok := requestBody["prompt_cache_key"].(string); ok && strings.TrimSpace(raw) != "" {
		kind := codexPromptCacheKeyKind(raw, originalBodySessionID, clientSessionID)
		next := scopeCodexAccountIdentityValue(account, apiKeyID, kind, raw)
		if next != raw {
			requestBody["prompt_cache_key"] = next
			changed = true
		}
	}
	return changed
}

// applyCodexAccountIdentityClientMetadataRaw scopes only the small identity
// subobjects with gjson/sjson. The passthrough hot path never unmarshals the
// potentially multi-megabyte request body.
func applyCodexAccountIdentityClientMetadataRaw(body []byte, account *Account, apiKeyID int64, clientSessionID string) ([]byte, bool, error) {
	if len(body) == 0 || codexAccountIdentityNamespace(account) == "" {
		return body, false, nil
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false, nil
	}

	next := body
	changed := false
	originalBodySessionID := ""
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		clientMetadata := map[string]any{}
		if err := json.Unmarshal([]byte(cm.Raw), &clientMetadata); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for account identity: %w", err)
		}
		originalBodySessionID, _ = clientMetadata["session_id"].(string)
		metadataChanged := applyCodexAccountIdentityFields(clientMetadata, account, apiKeyID)
		if applyCodexAccountIdentityEmbeddedMetadata(clientMetadata, account, apiKeyID) {
			metadataChanged = true
		}
		if metadataChanged {
			raw, err := json.Marshal(clientMetadata)
			if err != nil {
				return body, false, fmt.Errorf("encode account-scoped client_metadata: %w", err)
			}
			var setErr error
			next, setErr = sjson.SetRawBytes(next, "client_metadata", raw)
			if setErr != nil {
				return body, false, fmt.Errorf("splice account-scoped client_metadata: %w", setErr)
			}
			changed = true
		}
	}
	if promptCacheKey := gjson.GetBytes(body, "prompt_cache_key"); promptCacheKey.Type == gjson.String && strings.TrimSpace(promptCacheKey.String()) != "" {
		raw := promptCacheKey.String()
		kind := codexPromptCacheKeyKind(raw, originalBodySessionID, clientSessionID)
		scoped := scopeCodexAccountIdentityValue(account, apiKeyID, kind, raw)
		if scoped != raw {
			rewritten, err := sjson.SetBytes(next, "prompt_cache_key", scoped)
			if err != nil {
				return body, false, fmt.Errorf("splice account-scoped prompt_cache_key: %w", err)
			}
			next = rewritten
			changed = true
		}
	}
	return next, changed, nil
}

func copyCodexIdentityRelationHeaders(c *gin.Context, account *Account, headers http.Header) {
	if c == nil || c.Request == nil || !codexIdentityRelationsEnabled(codexAccountIdentitySource(c, account)) {
		return
	}
	for _, name := range [...]string{"session-id", "thread-id", "x-codex-parent-thread-id"} {
		if value := c.Request.Header.Get(name); strings.TrimSpace(value) != "" {
			headers.Set(name, value)
		}
	}
}

func applyCodexAccountIdentityHeaders(headers http.Header, account *Account, apiKeyID int64) {
	if headers == nil || codexAccountIdentityNamespace(account) == "" {
		return
	}
	for _, field := range codexAccountIdentityFields {
		if field.relationsOnly && !codexIdentityRelationsEnabled(account) {
			continue
		}
		// Underscore session/conversation headers are rebuilt separately from the
		// prompt cache key by each request builder.
		if field.name == "session_id" {
			continue
		}
		raw := strings.TrimSpace(headers.Get(field.name))
		if raw != "" {
			headers.Set(field.name, scopeCodexAccountIdentityValue(account, apiKeyID, field.kind, raw))
		}
	}
	if raw := strings.TrimSpace(headers.Get(openAIWSTurnMetadataHeader)); raw != "" {
		metadata := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &metadata); err == nil && metadata != nil && applyCodexAccountIdentityFields(metadata, account, apiKeyID) {
			if rebuilt, err := marshalCodexTurnMetadata(metadata); err == nil {
				headers.Set(openAIWSTurnMetadataHeader, string(rebuilt))
			}
		}
	}
}
