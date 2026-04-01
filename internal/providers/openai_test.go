package providers

import "testing"

func TestOpenAIProviderBuildRequestBodyGPT5FamilyOmitsTemperature(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-5.4")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Options: map[string]any{
			OptMaxTokens:   512,
			OptTemperature: 0.7,
		},
	}

	for _, model := range []string{
		"gpt-5.3-chat-latest",
		"openai/gpt-5.4",
		"gpt-5-mini",
		"o3",
	} {
		body := p.buildRequestBody(model, req, false)

		if _, ok := body["temperature"]; ok {
			t.Fatalf("model %q should omit temperature, got %v", model, body["temperature"])
		}
		if got := body["max_completion_tokens"]; got != 512 {
			t.Fatalf("model %q max_completion_tokens = %v, want 512", model, got)
		}
		if _, ok := body["max_tokens"]; ok {
			t.Fatalf("model %q should not set max_tokens", model)
		}
	}
}

func TestOpenAIProviderBuildRequestBodyGPT4KeepsTemperature(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-4o")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Options: map[string]any{
			OptMaxTokens:   256,
			OptTemperature: 0.3,
		},
	}

	body := p.buildRequestBody("gpt-4o", req, false)

	if got := body["temperature"]; got != 0.3 {
		t.Fatalf("temperature = %v, want 0.3", got)
	}
	if got := body["max_tokens"]; got != 256 {
		t.Fatalf("max_tokens = %v, want 256", got)
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Fatal("gpt-4o should not use max_completion_tokens")
	}
}

func TestOpenAIProviderBuildRequestBodyGPT53ChatNormalizesReasoningEffort(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-5.3-chat-latest")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Options: map[string]any{
			OptThinkingLevel: "low",
		},
	}

	for _, model := range []string{
		"gpt-5.3-chat-latest",
		"openai/gpt-5.3-chat-latest",
	} {
		body := p.buildRequestBody(model, req, false)
		if got := body[OptReasoningEffort]; got != "medium" {
			t.Fatalf("model %q reasoning_effort = %v, want medium", model, got)
		}
	}
}
