package handler

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildGrokXSearchResponsesBody(t *testing.T) {
	t.Parallel()
	understandImages := true
	understandVideos := false
	body, err := buildGrokXSearchResponsesBody(grokStandaloneSearchRequest{
		Query:                    "latest posts from xAI",
		AllowedXHandles:          []string{"xai"},
		ExcludedXHandles:         []string{"spam"},
		FromDate:                 "2026-08-01",
		ToDate:                   "2026-08-10",
		EnableImageUnderstanding: &understandImages,
		EnableVideoUnderstanding: &understandVideos,
	}, xai.DefaultTextModel)
	require.NoError(t, err)
	require.Equal(t, xai.DefaultTextModel, gjson.GetBytes(body, "model").String())
	require.Contains(t, gjson.GetBytes(body, "input").String(), "latest posts from xAI")
	require.Contains(t, gjson.GetBytes(body, "input").String(), "Return ONLY valid JSON")
	require.False(t, gjson.GetBytes(body, "include").Exists())
	require.Equal(t, "required", gjson.GetBytes(body, "tool_choice").String())
	require.Equal(t, "x_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "xai", gjson.GetBytes(body, "tools.0.allowed_x_handles.0").String())
	require.Equal(t, "spam", gjson.GetBytes(body, "tools.0.excluded_x_handles.0").String())
	require.Equal(t, "2026-08-01", gjson.GetBytes(body, "tools.0.from_date").String())
	require.Equal(t, "2026-08-10", gjson.GetBytes(body, "tools.0.to_date").String())
	require.True(t, gjson.GetBytes(body, "tools.0.enable_image_understanding").Bool())
	require.False(t, gjson.GetBytes(body, "tools.0.enable_video_understanding").Bool())
	require.False(t, gjson.GetBytes(body, "store").Bool())
	require.False(t, gjson.GetBytes(body, "stream").Bool())
}

func TestBuildGrokXSearchResponsesBodyAcceptsInputAlias(t *testing.T) {
	t.Parallel()
	body, err := buildGrokXSearchResponsesBody(grokStandaloneSearchRequest{Input: "latest posts from xAI"}, xai.DefaultTextModel)
	require.NoError(t, err)
	require.Contains(t, gjson.GetBytes(body, "input").String(), "latest posts from xAI")
}

func TestBuildGrokXSearchResponsesBodyUsesWebSearchRoute(t *testing.T) {
	t.Parallel()
	body, err := buildGrokXSearchResponsesBody(grokStandaloneSearchRequest{
		Query: "recent announcements",
	}, "Web/grok-chat-fast")
	require.NoError(t, err)
	require.Equal(t, "Web/grok-chat-fast", gjson.GetBytes(body, "model").String())
	require.False(t, gjson.GetBytes(body, "tools").Exists())
	require.False(t, gjson.GetBytes(body, "tool_choice").Exists())
}

func TestValidateGrokWebXSearchFiltersRejectsUnsupportedFilters(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateGrokWebXSearchFilters(grokStandaloneSearchRequest{Query: "recent announcements"}))
	require.Error(t, validateGrokWebXSearchFilters(grokStandaloneSearchRequest{Query: "recent announcements", AllowedXHandles: []string{"OpenAI"}}))
	require.Error(t, validateGrokWebXSearchFilters(grokStandaloneSearchRequest{Query: "recent announcements", FromDate: "2026-08-01"}))
	understandImages := true
	require.Error(t, validateGrokWebXSearchFilters(grokStandaloneSearchRequest{Query: "recent announcements", EnableImageUnderstanding: &understandImages}))
}

func TestValidateGrokNativeXSearchResponseRequiresCompletedCallAndSources(t *testing.T) {
	t.Parallel()
	messageOnly := []byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
	require.Error(t, validateGrokNativeXSearchResponse(messageOnly, nil))

	withSources := []byte(`{"status":"completed","output":[{"type":"x_search_call","status":"completed","action":{"sources":[{"type":"url","url":"https://x.com/OpenAI/status/1"}]}},{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
	results := extractGrokWebSearchSources(withSources, 5)
	require.NoError(t, validateGrokNativeXSearchResponse(withSources, results))
}

func TestResolveGrokStandaloneSearchModelUsesRuntimeDefault(t *testing.T) {
	original := xai.RuntimeModelMappingOptions()
	t.Cleanup(func() { xai.SetRuntimeModelMappingOptions(original) })
	xai.SetRuntimeModelMappingOptions(xai.ModelMappingOptions{DefaultText: "grok-4.6"})

	model := resolveGrokStandaloneSearchModel()
	body, err := buildGrokXSearchResponsesBody(grokStandaloneSearchRequest{Query: "latest posts from xAI"}, model)
	require.NoError(t, err)
	require.Equal(t, "grok-4.6", model)
	require.Equal(t, model, gjson.GetBytes(body, "model").String())
}

func TestResolveGrokStandaloneXSearchModelUsesDedicatedRoute(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok-x-search", resolveGrokStandaloneXSearchModel())
}

func TestExtractGrokWebSearchSourcesReturnsEmptyArray(t *testing.T) {
	t.Parallel()

	results := extractGrokWebSearchSources([]byte(`{"status":"completed","output":[{"type":"x_search_call","status":"completed"}]}`), 1)
	body, err := json.Marshal(results)
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(body))
}
