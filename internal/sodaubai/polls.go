package sodaubai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPollThreshold = 5
	PollActionAdd        = "add"
	PollActionRemove     = "remove"
)

type PollEntry struct {
	PollID        string   `json:"poll_id"`
	Action        string   `json:"action,omitempty"`
	Scope         string   `json:"scope,omitempty"`
	Channel       string   `json:"channel,omitempty"`
	ChatID        string   `json:"chat_id,omitempty"`
	LocalKey      string   `json:"local_key,omitempty"`
	ThreadID      int      `json:"thread_id,omitempty"`
	MessageID     int      `json:"message_id,omitempty"`
	Target        string   `json:"target"`
	TargetDisplay string   `json:"target_display,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Question      string   `json:"question,omitempty"`
	Threshold     int      `json:"threshold"`
	YesVoters     []string `json:"yes_voters,omitempty"`
	CreatedAt     string   `json:"created_at,omitempty"`
	CreatedDay    string   `json:"created_day,omitempty"`
	Resolved      bool     `json:"resolved,omitempty"`
	ResolvedAt    string   `json:"resolved_at,omitempty"`
	Closed        bool     `json:"closed,omitempty"`
}

type PollState struct {
	Date  string      `json:"date"`
	Polls []PollEntry `json:"polls"`
}

type PollCreate struct {
	PollID        string
	Action        string
	Scope         string
	Channel       string
	ChatID        string
	LocalKey      string
	ThreadID      int
	MessageID     int
	Target        string
	TargetDisplay string
	Reason        string
	Question      string
	Threshold     int
}

type PollVoteResult struct {
	Poll             *PollEntry
	YesVotes         int
	ThresholdReached bool
}

type PollService struct {
	mu     sync.Mutex
	paths  []string
	active string
	now    func() time.Time
	loc    *time.Location
	loaded bool
	state  PollState
}

func NewPollService(path string) *PollService {
	candidates := []string{path}
	fallback := fallbackPath(path, "so-dau-bai-polls", ".json")
	if fallback != "" && fallback != path {
		candidates = append(candidates, fallback)
	}
	return &PollService{
		paths: candidates,
		now:   time.Now,
		loc:   time.Local,
	}
}

func NormalizePollAction(action string) string {
	switch strings.TrimSpace(action) {
	case PollActionRemove:
		return PollActionRemove
	default:
		return PollActionAdd
	}
}

func (s *PollService) CreatePoll(input PollCreate) (PollEntry, error) {
	input.PollID = strings.TrimSpace(input.PollID)
	input.Action = NormalizePollAction(input.Action)
	input.Scope = strings.TrimSpace(input.Scope)
	input.Channel = strings.TrimSpace(input.Channel)
	input.ChatID = strings.TrimSpace(input.ChatID)
	input.LocalKey = strings.TrimSpace(input.LocalKey)
	input.Target = strings.TrimSpace(input.Target)
	input.TargetDisplay = strings.TrimSpace(input.TargetDisplay)
	input.Reason = strings.TrimSpace(input.Reason)
	input.Question = strings.TrimSpace(input.Question)
	if input.PollID == "" {
		return PollEntry{}, fmt.Errorf("poll_id is required")
	}
	if input.Target == "" {
		return PollEntry{}, fmt.Errorf("target is required")
	}
	if input.Threshold <= 0 {
		input.Threshold = DefaultPollThreshold
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return PollEntry{}, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return PollEntry{}, err
		}
	}

	for _, entry := range s.state.Polls {
		if entry.PollID == input.PollID {
			return entry, nil
		}
		if !entry.Resolved &&
			!entry.Closed &&
			sameRule(entry.Target, input.Target) &&
			NormalizePollAction(entry.Action) == input.Action &&
			samePollReuseScope(entry, input) {
			return PollEntry{}, fmt.Errorf("%s already has an active sổ đầu bài poll in this chat", entry.Target)
		}
	}

	entry := PollEntry{
		PollID:        input.PollID,
		Action:        input.Action,
		Scope:         input.Scope,
		Channel:       input.Channel,
		ChatID:        input.ChatID,
		LocalKey:      input.LocalKey,
		ThreadID:      input.ThreadID,
		MessageID:     input.MessageID,
		Target:        input.Target,
		TargetDisplay: input.TargetDisplay,
		Reason:        input.Reason,
		Question:      input.Question,
		Threshold:     input.Threshold,
		CreatedAt:     s.now().In(s.location()).Format(time.RFC3339),
		CreatedDay:    s.state.Date,
	}
	s.state.Polls = append(s.state.Polls, entry)
	if err := s.saveLocked(); err != nil {
		return PollEntry{}, err
	}
	return entry, nil
}

func (s *PollService) FindActiveByTarget(scope, target string) (*PollEntry, error) {
	return s.FindActiveByTargetAction(scope, target, "")
}

func (s *PollService) FindActiveByChatTargetAction(channel, chatID, target, action string) (*PollEntry, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, nil
	}
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	action = strings.TrimSpace(action)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}

	for _, entry := range s.state.Polls {
		if entry.Resolved || entry.Closed {
			continue
		}
		if action != "" && NormalizePollAction(entry.Action) != NormalizePollAction(action) {
			continue
		}
		if channel != "" && strings.TrimSpace(entry.Channel) != channel {
			continue
		}
		if chatID != "" && strings.TrimSpace(entry.ChatID) != chatID {
			continue
		}
		if sameRule(entry.Target, target) {
			matched := clonePollEntry(entry)
			return &matched, nil
		}
	}
	return nil, nil
}

func (s *PollService) FindActiveByTargetAction(scope, target, action string) (*PollEntry, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, nil
	}
	action = strings.TrimSpace(action)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}

	for _, entry := range s.state.Polls {
		if entry.Scope != strings.TrimSpace(scope) || entry.Resolved || entry.Closed {
			continue
		}
		if action != "" && NormalizePollAction(entry.Action) != NormalizePollAction(action) {
			continue
		}
		if sameRule(entry.Target, target) {
			matched := clonePollEntry(entry)
			return &matched, nil
		}
	}
	return nil, nil
}

func samePollReuseScope(entry PollEntry, input PollCreate) bool {
	if strings.TrimSpace(entry.Channel) != "" &&
		strings.TrimSpace(input.Channel) != "" &&
		strings.TrimSpace(entry.ChatID) != "" &&
		strings.TrimSpace(input.ChatID) != "" {
		return strings.TrimSpace(entry.Channel) == strings.TrimSpace(input.Channel) &&
			strings.TrimSpace(entry.ChatID) == strings.TrimSpace(input.ChatID)
	}
	return entry.Scope == input.Scope
}

func (s *PollService) RecordVote(pollID, voterID string, optionIDs []int) (PollVoteResult, error) {
	pollID = strings.TrimSpace(pollID)
	voterID = strings.TrimSpace(voterID)
	if pollID == "" {
		return PollVoteResult{}, fmt.Errorf("poll_id is required")
	}
	if voterID == "" {
		return PollVoteResult{}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return PollVoteResult{}, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return PollVoteResult{}, err
		}
	}

	for i := range s.state.Polls {
		entry := &s.state.Polls[i]
		if entry.PollID != pollID {
			continue
		}
		if entry.Resolved || entry.Closed {
			return PollVoteResult{Poll: pollEntryPtr(entry), YesVotes: len(entry.YesVoters)}, nil
		}

		yesVote := pollHasYesVote(optionIDs)
		entry.YesVoters = updateYesVoters(entry.YesVoters, voterID, yesVote)
		result := PollVoteResult{
			Poll:     pollEntryPtr(entry),
			YesVotes: len(entry.YesVoters),
		}
		if len(entry.YesVoters) >= entry.Threshold && !entry.Resolved {
			entry.Resolved = true
			entry.ResolvedAt = s.now().In(s.location()).Format(time.RFC3339)
			result.ThresholdReached = true
			result.Poll = pollEntryPtr(entry)
		}
		if err := s.saveLocked(); err != nil {
			return PollVoteResult{}, err
		}
		return result, nil
	}

	return PollVoteResult{}, nil
}

func (s *PollService) MarkClosed(pollID string) (*PollEntry, error) {
	pollID = strings.TrimSpace(pollID)
	if pollID == "" {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}

	for i := range s.state.Polls {
		if s.state.Polls[i].PollID != pollID {
			continue
		}
		if !s.state.Polls[i].Closed {
			s.state.Polls[i].Closed = true
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
		}
		return pollEntryPtr(&s.state.Polls[i]), nil
	}
	return nil, nil
}

func (s *PollService) ensureLoadedLocked() error {
	if s.loaded {
		return nil
	}
	s.loaded = true

	for _, path := range s.paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				if s.active == "" {
					s.active = path
				}
				continue
			}
			if s.active == "" {
				s.active = path
			}
			continue
		}
		s.active = path
		if len(data) == 0 {
			s.state = PollState{}
			return nil
		}
		if err := json.Unmarshal(data, &s.state); err != nil {
			return fmt.Errorf("decode so_dau_bai poll state: %w", err)
		}
		return nil
	}

	s.state = PollState{}
	if s.active == "" && len(s.paths) > 0 {
		s.active = s.paths[0]
	}
	return nil
}

func (s *PollService) ensureTodayLocked() bool {
	today := s.now().In(s.location()).Format("2006-01-02")
	if s.state.Date == today {
		return false
	}
	s.state = PollState{Date: today, Polls: nil}
	return true
}

func (s *PollService) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}

	var lastErr error
	for _, path := range s.writeCandidates() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			lastErr = err
			continue
		}
		tmpPath := path + ".tmp"
		if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
			lastErr = err
			continue
		}
		if err := os.Rename(tmpPath, path); err != nil {
			lastErr = err
			_ = os.Remove(tmpPath)
			continue
		}
		s.active = path
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no writable so_dau_bai poll path configured")
	}
	return lastErr
}

func (s *PollService) location() *time.Location {
	if s.loc != nil {
		return s.loc
	}
	return time.Local
}

func (s *PollService) writeCandidates() []string {
	seen := make(map[string]struct{}, len(s.paths)+1)
	var out []string
	if s.active != "" {
		seen[s.active] = struct{}{}
		out = append(out, s.active)
	}
	for _, path := range s.paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func pollHasYesVote(optionIDs []int) bool {
	for _, optionID := range optionIDs {
		if optionID == 0 {
			return true
		}
	}
	return false
}

func updateYesVoters(voters []string, voterID string, yesVote bool) []string {
	out := make([]string, 0, len(voters)+1)
	found := false
	for _, existing := range voters {
		if existing == voterID {
			found = true
			continue
		}
		out = append(out, existing)
	}
	if yesVote {
		out = append(out, voterID)
	} else if !found {
		return voters
	}
	return out
}

func clonePollEntry(in PollEntry) PollEntry {
	out := in
	out.YesVoters = append([]string(nil), in.YesVoters...)
	return out
}

func pollEntryPtr(entry *PollEntry) *PollEntry {
	if entry == nil {
		return nil
	}
	cloned := clonePollEntry(*entry)
	return &cloned
}
