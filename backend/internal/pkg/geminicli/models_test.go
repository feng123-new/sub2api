package geminicli

import "testing"

func TestDefaultModels_ContainsImageModels(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		byID[model.ID] = model
	}

	required := []string{
		"gemini-2.5-flash-image",
		"gemini-3.1-flash-image",
	}

	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected curated Gemini model %q to exist", id)
		}
	}
}

func TestDefaultModels_ContainsGemini36Flash(t *testing.T) {
	t.Parallel()

	for _, model := range DefaultModels {
		if model.ID == "gemini-3.6-flash" {
			if model.DisplayName != "Gemini 3.6 Flash" {
				t.Fatalf("unexpected display name %q", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("expected curated Gemini 3.6 Flash model to exist")
}
