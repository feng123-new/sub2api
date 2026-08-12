package service

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentModerationKeywordMatcherMatchesLegacyBehavior(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		keywords []string
	}{
		{name: "miss", text: "clean prompt", keywords: []string{"blocked", "secret"}},
		{name: "case insensitive", text: "contains SECRET value", keywords: []string{"secret"}},
		{name: "configured order wins", text: "early appears before later", keywords: []string{"later", "early"}},
		{name: "overlap uses configured order", text: "abc", keywords: []string{"bc", "abc"}},
		{name: "unicode", text: "这里包含敏感词和世界", keywords: []string{"世界", "敏感词"}},
		{name: "duplicates", text: "duplicate", keywords: []string{"duplicate", "DUPLICATE"}},
		{name: "empty entries", text: "blocked", keywords: []string{"", "blocked"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantKeyword, wantHit := matchBlockedKeyword(tt.text, tt.keywords)
			gotKeyword, gotHit := newContentModerationKeywordMatcher(tt.keywords).Match(tt.text)
			require.Equal(t, wantHit, gotHit)
			require.Equal(t, wantKeyword, gotKeyword)
		})
	}
}

func TestContentModerationKeywordMatchersApplyBoundaryAndTrimmingRules(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		keywords    []string
		wantKeyword string
		wantHit     bool
	}{
		{name: "latin substring inside word misses", text: "concatenate", keywords: []string{"cat"}},
		{name: "latin word matches", text: "a cat!", keywords: []string{"cat"}, wantKeyword: "cat", wantHit: true},
		{name: "underscore remains a word rune", text: "safe_bad_value", keywords: []string{"bad"}},
		{name: "trimmed latin keyword matches", text: "this is bad", keywords: []string{"  bad\t"}, wantKeyword: "  bad\t", wantHit: true},
		{name: "non latin keyword remains substring", text: "前置敏感词后置", keywords: []string{"敏感"}, wantKeyword: "敏感", wantHit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyKeyword, legacyHit := matchBlockedKeyword(tt.text, tt.keywords)
			require.Equal(t, tt.wantHit, legacyHit)
			require.Equal(t, tt.wantKeyword, legacyKeyword)

			matcherKeyword, matcherHit := newContentModerationKeywordMatcher(tt.keywords).Match(tt.text)
			require.Equal(t, tt.wantHit, matcherHit)
			require.Equal(t, tt.wantKeyword, matcherKeyword)
		})
	}

	require.Nil(t, newContentModerationKeywordMatcher([]string{"", " \t "}))
}

func TestContentModerationKeywordMatcherAppliesBoundariesOnlyToWordRuneEdges(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		keyword string
		wantHit bool
	}{
		{name: "trailing punctuation skips following boundary", text: "C++17", keyword: "C++", wantHit: true},
		{name: "leading word rune still requires preceding boundary", text: "XC++", keyword: "C++"},
		{name: "leading punctuation skips preceding boundary", text: "file.exe", keyword: ".exe", wantHit: true},
		{name: "trailing word rune still requires following boundary", text: ".exe2", keyword: ".exe"},
		{name: "multibyte leading word rune requires preceding boundary", text: "Xé+", keyword: "é+"},
		{name: "multibyte trailing word rune requires following boundary", text: ".é2", keyword: ".é"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyword, hit := newContentModerationKeywordMatcher([]string{tt.keyword}).Match(tt.text)
			require.Equal(t, tt.wantHit, hit)
			if tt.wantHit {
				require.Equal(t, tt.keyword, keyword)
			} else {
				require.Empty(t, keyword)
			}
		})
	}
}

func TestContentModerationKeywordMatcherValidatesTerminalOutputChainBoundaries(t *testing.T) {
	keywords := []string{"he", "she"}

	keyword, hit := newContentModerationKeywordMatcher(keywords).Match("she!")

	require.True(t, hit)
	require.Equal(t, "she", keyword)
}

func TestContentModerationRuntimeSnapshotNilConfigFailsOpen(t *testing.T) {
	var snapshot *contentModerationRuntimeSnapshot

	keyword, hit := snapshot.matchBlockedKeyword("blocked")

	require.False(t, hit)
	require.Empty(t, keyword)
}

func TestContentModerationKeywordMatcherRandomizedParity(t *testing.T) {
	rng := rand.New(rand.NewSource(20260714))
	const alphabet = "abcXYZ"
	for iteration := 0; iteration < 1000; iteration++ {
		keywords := make([]string, 1+rng.Intn(30))
		for index := range keywords {
			length := 1 + rng.Intn(8)
			var value strings.Builder
			for range length {
				_ = value.WriteByte(alphabet[rng.Intn(len(alphabet))])
			}
			keywords[index] = value.String()
		}
		var text strings.Builder
		for range 20 + rng.Intn(100) {
			_ = text.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}

		wantKeyword, wantHit := matchBlockedKeyword(text.String(), keywords)
		gotKeyword, gotHit := newContentModerationKeywordMatcher(keywords).Match(text.String())
		require.Equal(t, wantHit, gotHit, "iteration %d", iteration)
		require.Equal(t, wantKeyword, gotKeyword, "iteration %d", iteration)
	}
}
