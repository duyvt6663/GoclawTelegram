package sodaubai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

type Entry struct {
	Target   string `json:"target"`
	AddedBy  string `json:"added_by,omitempty"`
	AddedAt  string `json:"added_at,omitempty"`
	Note     string `json:"note,omitempty"`
	AddedDay string `json:"added_day,omitempty"`
}

type State struct {
	Date    string  `json:"date"`
	Entries []Entry `json:"entries"`
}

type Service struct {
	mu     sync.Mutex
	paths  []string
	active string
	now    func() time.Time
	loc    *time.Location
	always map[string][]string
	loaded bool
	state  State
}

func NewService(path string) *Service {
	candidates := []string{path}
	fallback := filepath.Join(os.TempDir(), "goclaw", "so-dau-bai.json")
	if fallback != "" && fallback != path {
		candidates = append(candidates, fallback)
	}
	return &Service{
		paths:  candidates,
		now:    time.Now,
		loc:    time.Local,
		always: make(map[string][]string),
	}
}

func ScopeKey(channel, localKey, chatID string) string {
	channel = strings.TrimSpace(channel)
	localKey = strings.TrimSpace(localKey)
	chatID = strings.TrimSpace(chatID)

	switch {
	case channel != "" && localKey != "":
		return channel + "|" + localKey
	case channel != "" && chatID != "":
		return channel + "|" + chatID
	case localKey != "":
		return localKey
	case chatID != "":
		return chatID
	default:
		return channel
	}
}

func (s *Service) Today() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return State{}, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return State{}, err
		}
	}
	return cloneState(s.state), nil
}

func (s *Service) TodayForScope(scope string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return State{}, err
	}
	if dirty := s.ensureTodayLocked(); dirty {
		if err := s.saveLocked(); err != nil {
			return State{}, err
		}
	}

	state := cloneState(s.state)
	state.Entries = mergeScopeAlwaysEntries(state.Entries, state.Date, s.alwaysEntriesLocked(scope))
	return state, nil
}

func (s *Service) AddToday(target, addedBy, note string) (Entry, bool, error) {
	target = strings.TrimSpace(target)
	addedBy = strings.TrimSpace(addedBy)
	note = strings.TrimSpace(note)
	if target == "" {
		return Entry{}, false, fmt.Errorf("target is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return Entry{}, false, err
	}
	if s.ensureTodayLocked() {
		if err := s.saveLocked(); err != nil {
			return Entry{}, false, err
		}
	}

	for _, entry := range s.state.Entries {
		if sameRule(entry.Target, target) {
			return entry, false, nil
		}
	}

	entry := Entry{
		Target:   target,
		AddedBy:  addedBy,
		AddedAt:  s.now().In(s.location()).Format(time.RFC3339),
		AddedDay: s.state.Date,
	}
	if note != "" {
		entry.Note = note
	}
	s.state.Entries = append(s.state.Entries, entry)
	if err := s.saveLocked(); err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

func (s *Service) RemoveToday(target string) (*Entry, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, false, fmt.Errorf("target is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return nil, false, err
	}
	if s.ensureTodayLocked() {
		if err := s.saveLocked(); err != nil {
			return nil, false, err
		}
	}

	for i, entry := range s.state.Entries {
		if !sameRule(entry.Target, target) {
			continue
		}
		removed := entry
		s.state.Entries = append(s.state.Entries[:i], s.state.Entries[i+1:]...)
		if err := s.saveLocked(); err != nil {
			return nil, false, err
		}
		return &removed, true, nil
	}
	return nil, false, nil
}

func (s *Service) MatchTodayForScope(scope, senderID, userID string) (*Entry, error) {
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

	for _, entry := range s.state.Entries {
		if channels.SenderMatchesList(senderID, []string{entry.Target}) || channels.SenderMatchesList(userID, []string{entry.Target}) {
			matched := entry
			return &matched, nil
		}
	}

	for _, entry := range s.alwaysEntriesLocked(scope) {
		if channels.SenderMatchesList(senderID, []string{entry.Target}) || channels.SenderMatchesList(userID, []string{entry.Target}) {
			matched := entry
			return &matched, nil
		}
	}

	return nil, nil
}

func (s *Service) SetAlways(scope string, rules []string) {
	scope = strings.TrimSpace(scope)

	s.mu.Lock()
	defer s.mu.Unlock()

	if scope == "" {
		return
	}
	rules = uniqueRules(rules)
	if len(rules) == 0 {
		delete(s.always, scope)
		return
	}
	s.always[scope] = append([]string(nil), rules...)
}

func (s *Service) HasAlways(scope, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, rule := range s.always[strings.TrimSpace(scope)] {
		if sameRule(rule, target) {
			return true
		}
	}
	return false
}

func (s *Service) MatchToday(senderID, userID string) (*Entry, error) {
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

	for _, entry := range s.state.Entries {
		if channels.SenderMatchesList(senderID, []string{entry.Target}) || channels.SenderMatchesList(userID, []string{entry.Target}) {
			matched := entry
			return &matched, nil
		}
	}
	return nil, nil
}

func (s *Service) ensureLoadedLocked() error {
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
			s.state = State{}
			return nil
		}
		if err := json.Unmarshal(data, &s.state); err != nil {
			return fmt.Errorf("decode so_dau_bai state: %w", err)
		}
		return nil
	}

	s.state = State{}
	if s.active == "" && len(s.paths) > 0 {
		s.active = s.paths[0]
	}
	return nil
}

func (s *Service) ensureTodayLocked() bool {
	today := s.now().In(s.location()).Format("2006-01-02")
	if s.state.Date == today {
		return false
	}
	s.state = State{Date: today, Entries: nil}
	return true
}

func (s *Service) saveLocked() error {
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
		lastErr = fmt.Errorf("no writable so_dau_bai path configured")
	}
	return lastErr
}

func (s *Service) location() *time.Location {
	if s.loc != nil {
		return s.loc
	}
	return time.Local
}

func sameRule(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return channels.SenderMatchesList(a, []string{b}) || channels.SenderMatchesList(b, []string{a})
}

func cloneState(in State) State {
	out := State{
		Date:    in.Date,
		Entries: make([]Entry, len(in.Entries)),
	}
	copy(out.Entries, in.Entries)
	return out
}

func (s *Service) writeCandidates() []string {
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

func (s *Service) alwaysEntriesLocked(scope string) []Entry {
	scope = strings.TrimSpace(scope)
	rules := s.always[scope]
	if len(rules) == 0 {
		return nil
	}

	entries := make([]Entry, 0, len(rules))
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		entries = append(entries, Entry{
			Target: rule,
			Note:   "always blocked via deny_from",
		})
	}
	return entries
}

func mergeScopeAlwaysEntries(entries []Entry, day string, always []Entry) []Entry {
	if len(always) == 0 {
		return entries
	}

	out := make([]Entry, 0, len(entries)+len(always))
	out = append(out, entries...)
	for _, extra := range always {
		found := false
		for _, existing := range out {
			if sameRule(existing.Target, extra.Target) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		extra.AddedDay = day
		out = append(out, extra)
	}
	return out
}

func uniqueRules(rules []string) []string {
	seen := make([]string, 0, len(rules))
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		duplicate := false
		for _, existing := range seen {
			if sameRule(existing, rule) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		seen = append(seen, rule)
		out = append(out, rule)
	}
	return out
}
