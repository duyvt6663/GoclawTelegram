package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// CodexCLIProvider shells out to the local `codex` CLI and lets Codex manage
// its own session history, tool use, and workspace behavior.
type CodexCLIProvider struct {
	name                   string
	cliPath                string
	defaultModel           string
	defaultReasoningEffort string
	workDir                string
	sandboxMode            string
	approvalPolicy         string
	sessionMu              sync.Map // key: string, value: *sync.Mutex
	threadIDs              sync.Map // key: string, value: string
}

type CodexCLIOption func(*CodexCLIProvider)

func WithCodexCLIName(name string) CodexCLIOption {
	return func(p *CodexCLIProvider) {
		if name != "" {
			p.name = name
		}
	}
}

func WithCodexCLIModel(model string) CodexCLIOption {
	return func(p *CodexCLIProvider) {
		if model != "" {
			p.defaultModel = model
		}
	}
}

func WithCodexCLIReasoningEffort(effort string) CodexCLIOption {
	return func(p *CodexCLIProvider) {
		if effort := NormalizeReasoningEffort(effort); effort != "" && effort != "off" && effort != "auto" {
			p.defaultReasoningEffort = effort
		}
	}
}

func WithCodexCLIWorkDir(dir string) CodexCLIOption {
	return func(p *CodexCLIProvider) {
		if dir != "" {
			p.workDir = dir
		}
	}
}

func WithCodexCLISandboxMode(mode string) CodexCLIOption {
	return func(p *CodexCLIProvider) {
		if mode != "" {
			p.sandboxMode = mode
		}
	}
}

func WithCodexCLIApprovalPolicy(policy string) CodexCLIOption {
	return func(p *CodexCLIProvider) {
		if policy != "" {
			p.approvalPolicy = policy
		}
	}
}

func NewCodexCLIProvider(cliPath string, opts ...CodexCLIOption) *CodexCLIProvider {
	if cliPath == "" {
		cliPath = "codex"
	}
	p := &CodexCLIProvider{
		name:           "codex-cli",
		cliPath:        cliPath,
		defaultModel:   "gpt-5.4",
		sandboxMode:    "workspace-write",
		approvalPolicy: "never",
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *CodexCLIProvider) Name() string           { return p.name }
func (p *CodexCLIProvider) DefaultModel() string   { return p.defaultModel }
func (p *CodexCLIProvider) SupportsThinking() bool { return true }

func (p *CodexCLIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return p.run(ctx, req, nil)
}

func (p *CodexCLIProvider) ChatStream(ctx context.Context, req ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	return p.run(ctx, req, onChunk)
}

func (p *CodexCLIProvider) run(ctx context.Context, req ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	systemPrompt, userMsg, images := extractFromMessages(req.Messages)
	if len(images) > 0 {
		return nil, fmt.Errorf("codex-cli: image input is not yet supported")
	}

	sessionKey := extractStringOpt(req.Options, OptSessionKey)
	unlock := p.lockSession(sessionKey)
	defer unlock()

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("codex-cli: model is required")
	}

	workDir, err := p.resolveWorkDir(req.Options)
	if err != nil {
		return nil, err
	}

	threadID, resumed := p.lookupThreadID(sessionKey)
	prompt := formatCodexCLIPrompt(systemPrompt, userMsg, !resumed)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("codex-cli: empty prompt")
	}

	args := p.buildArgs(workDir, model, resolveCodexReasoningEffort(req.Options, p.defaultReasoningEffort), threadID, prompt, resumed)
	cmd := exec.CommandContext(ctx, p.cliPath, args...)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex-cli stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex-cli start: %w", err)
	}

	result, parseErr := p.parseOutput(stdout, sessionKey, onChunk)
	if waitErr := cmd.Wait(); waitErr != nil {
		if result != nil && result.Content != "" {
			return result, nil
		}
		return nil, fmt.Errorf("codex-cli: %w (stderr: %s)", waitErr, stderr.String())
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if result == nil {
		return nil, fmt.Errorf("codex-cli: empty response")
	}
	if onChunk != nil {
		onChunk(StreamChunk{Done: true})
	}
	return result, nil
}

func (p *CodexCLIProvider) lockSession(sessionKey string) func() {
	actual, _ := p.sessionMu.LoadOrStore(sessionKey, &sync.Mutex{})
	m := actual.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

func (p *CodexCLIProvider) resolveWorkDir(opts map[string]any) (string, error) {
	dir := strings.TrimSpace(extractStringOpt(opts, OptWorkspace))
	if dir == "" {
		dir = strings.TrimSpace(p.workDir)
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("codex-cli: resolve workdir: %w", err)
		}
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("codex-cli: resolve workdir: %w", err)
		}
		dir = abs
	}
	return dir, nil
}

func (p *CodexCLIProvider) lookupThreadID(sessionKey string) (string, bool) {
	if sessionKey == "" {
		return "", false
	}
	if v, ok := p.threadIDs.Load(sessionKey); ok {
		if threadID, ok := v.(string); ok && threadID != "" {
			return threadID, true
		}
	}
	return "", false
}

func (p *CodexCLIProvider) buildArgs(workDir, model, reasoningEffort, threadID, prompt string, resumed bool) []string {
	args := []string{
		"-C", workDir,
		"-s", p.sandboxMode,
		"-a", p.approvalPolicy,
		"exec",
	}
	if resumed {
		args = append(args, "resume", threadID)
	}
	args = append(args, "--json", "-m", model)
	if reasoningEffort != "" {
		args = append(args, "-c", fmt.Sprintf(`model_reasoning_effort="%s"`, reasoningEffort))
	}
	args = append(args, prompt)
	return args
}

func resolveCodexReasoningEffort(opts map[string]any, defaultEffort string) string {
	if effort := NormalizeReasoningEffort(extractStringOpt(opts, OptThinkingLevel)); effort != "" && effort != "off" && effort != "auto" {
		return effort
	}
	if effort := NormalizeReasoningEffort(defaultEffort); effort != "" && effort != "off" && effort != "auto" {
		return effort
	}
	return ""
}

func formatCodexCLIPrompt(systemPrompt, userMsg string, includeSystem bool) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	userMsg = strings.TrimSpace(userMsg)
	if !includeSystem || systemPrompt == "" {
		return userMsg
	}
	if userMsg == "" {
		return systemPrompt
	}
	return fmt.Sprintf("<system_instructions>\n%s\n</system_instructions>\n\n<user_message>\n%s\n</user_message>", systemPrompt, userMsg)
}

func (p *CodexCLIProvider) parseOutput(stdout io.Reader, sessionKey string, onChunk func(StreamChunk)) (*ChatResponse, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, StdioScanBufInit), StdioScanBufMax)

	resp := &ChatResponse{FinishReason: "stop"}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event codexCLIEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		switch event.Type {
		case "thread.started":
			if sessionKey != "" && event.ThreadID != "" {
				p.threadIDs.Store(sessionKey, event.ThreadID)
			}
		case "item.completed":
			if event.Item == nil || event.Item.Type != "agent_message" || event.Item.Text == "" {
				continue
			}
			resp.Content += event.Item.Text
			if onChunk != nil {
				onChunk(StreamChunk{Content: event.Item.Text})
			}
		case "turn.completed":
			if event.Usage != nil {
				resp.Usage = &Usage{
					PromptTokens:     event.Usage.InputTokens,
					CompletionTokens: event.Usage.OutputTokens,
					TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
					CacheReadTokens:  event.Usage.CachedInputTokens,
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("codex-cli: stream read error: %w", err)
	}
	return resp, nil
}

type codexCLIEvent struct {
	Type     string             `json:"type"`
	ThreadID string             `json:"thread_id,omitempty"`
	Item     *codexCLIEventItem `json:"item,omitempty"`
	Usage    *codexCLIUsage     `json:"usage,omitempty"`
}

type codexCLIEventItem struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type codexCLIUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}
