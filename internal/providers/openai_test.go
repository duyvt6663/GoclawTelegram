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

func TestOpenAIProviderBuildRequestBodyGPT54ToolsOmitReasoningEffortOnNativeOpenAIChatCompletions(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-5.4")
	p.WithProviderType("openai_compat")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Review this paper"}},
		Tools: []ToolDefinition{{
			Type: "function",
			Function: ToolFunctionSchema{
				Name:        "research_reviewer_prepare_review",
				Description: "Prepare a review bundle",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		Options: map[string]any{
			OptThinkingLevel: "xhigh",
		},
	}

	body := p.buildRequestBody("gpt-5.4", req, false)
	if _, ok := body[OptReasoningEffort]; ok {
		t.Fatalf("reasoning_effort = %v, want omitted for native OpenAI gpt-5.4 tool calls", body[OptReasoningEffort])
	}
}

func TestOpenAIProviderBuildRequestBodyGPT54NoToolsKeepsReasoningEffort(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-5.4")
	p.WithProviderType("openai_compat")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Options: map[string]any{
			OptThinkingLevel: "xhigh",
		},
	}

	body := p.buildRequestBody("gpt-5.4", req, false)
	if got := body[OptReasoningEffort]; got != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", got)
	}
}

func TestOpenAIProviderBuildRequestBodyHonorsToolChoiceOverride(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-4o")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "React with a sticker"}},
		Tools: []ToolDefinition{{
			Type: "function",
			Function: ToolFunctionSchema{
				Name:        "find_and_post_local_sticker",
				Description: "Attach a saved sticker",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		Options: map[string]any{
			OptToolChoice: "required",
		},
	}

	body := p.buildRequestBody("gpt-4o", req, false)
	if got := body["tool_choice"]; got != "required" {
		t.Fatalf("tool_choice = %v, want required", got)
	}
}

func TestOpenAIProviderBuildRequestBodyHonorsSpecificFunctionToolChoice(t *testing.T) {
	p := NewOpenAIProvider("openai", "test-key", "https://api.openai.com/v1", "gpt-4o")

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Open the pardon vote"}},
		Tools: []ToolDefinition{
			{
				Type: "function",
				Function: ToolFunctionSchema{
					Name:        "create_so_dau_bai_pardon_poll",
					Description: "Open a pardon poll",
					Parameters:  map[string]any{"type": "object"},
				},
			},
			{
				Type: "function",
				Function: ToolFunctionSchema{
					Name:        "find_and_post_local_sticker",
					Description: "Attach a saved sticker",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
		Options: map[string]any{
			OptToolChoice: "function:create_so_dau_bai_pardon_poll",
		},
	}

	body := p.buildRequestBody("gpt-4o", req, false)
	got, ok := body["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice = %T %v, want function object", body["tool_choice"], body["tool_choice"])
	}
	if got["type"] != "function" {
		t.Fatalf("tool_choice.type = %v, want function", got["type"])
	}
	fn, ok := got["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice.function = %T %v, want object", got["function"], got["function"])
	}
	if fn["name"] != "create_so_dau_bai_pardon_poll" {
		t.Fatalf("tool_choice.function.name = %v, want create_so_dau_bai_pardon_poll", fn["name"])
	}
}
