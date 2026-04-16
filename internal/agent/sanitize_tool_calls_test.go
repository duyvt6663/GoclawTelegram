package agent

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestLooksLikeDowngradedToolCallText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "plain text function call with json args",
			in:   `{"config_id":"abc"} to=functions.job_crawler_run  JSON`,
			want: true,
		},
		{
			name: "plain text function call with config json",
			in:   `{"enable_linkedin_proxy_source":true} json to=functions.job_crawler_config_upsert run code`,
			want: true,
		},
		{
			name: "json tool envelope with tool_name",
			in:   `{"tool_name":"list_features","arguments":{"query":"crawl job"}}`,
			want: true,
		},
		{
			name: "json tool envelope with toolname trailing junk",
			in:   `{"toolname":"listfeatures","arguments":{"query":"crawl job"}}====`,
			want: true,
		},
		{
			name: "legacy bracketed tool block",
			in:   `[Tool Call: job_crawler_run]`,
			want: true,
		},
		{
			name: "normal prose",
			in:   `Configured. Want me to run the feed now?`,
			want: false,
		},
		{
			name: "explaining tool name without pseudo syntax",
			in:   `The tool is job_crawler_run and it reruns the feed.`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeDowngradedToolCallText(tt.in); got != tt.want {
				t.Fatalf("looksLikeDowngradedToolCallText(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeAssistantContent_StripsPlainTextToolCall(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "entire response is pseudo tool call",
			in:   `{"config_id":"abc"} to=functions.job_crawler_run JSON`,
			want: "",
		},
		{
			name: "mixed response keeps surrounding prose",
			in:   "Applied.\n{\"remote_only\":true} to=functions.job_crawler_config_upsert json\nRun the feed after that.",
			want: "Applied.\nRun the feed after that.",
		},
		{
			name: "json tool envelope line is stripped",
			in:   "{\"tool_name\":\"list_features\",\"arguments\":{}}\nMình đã list các beta features hiện có ở đây.",
			want: "Mình đã list các beta features hiện có ở đây.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeAssistantContent(tt.in); got != tt.want {
				t.Fatalf("SanitizeAssistantContent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeHistory_StripsDowngradedToolText(t *testing.T) {
	msgs := []providers.Message{
		{Role: "assistant", Content: `{"config_id":"abc"} to=functions.job_crawler_run JSON`},
		{Role: "user", Content: "run it again"},
	}

	got, dropped := sanitizeHistory(msgs)
	if dropped != 0 {
		t.Fatalf("sanitizeHistory dropped = %d, want 0", dropped)
	}
	if len(got) != 2 {
		t.Fatalf("sanitizeHistory len = %d, want 2", len(got))
	}
	if got[0].Content != "" {
		t.Fatalf("assistant content = %q, want empty after sanitization", got[0].Content)
	}
}
