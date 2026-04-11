package jobcrawler

import (
	"sort"
	"strings"
	"unicode"
)

const (
	roleSoftwareEngineer = "software_engineer"
	roleBackend          = "backend"
	roleFrontend         = "frontend"
	roleFullstack        = "fullstack"
	roleAIEngineer       = "ai_engineer"
	roleMLEngineer       = "ml_engineer"

	seniorityAny       = "any"
	seniorityJunior    = "junior"
	seniorityMid       = "mid"
	senioritySenior    = "senior"
	seniorityStaff     = "staff"
	seniorityPrincipal = "principal"
	seniorityDirector  = "director"

	defaultMaxSeniorityLevel    = seniorityMid
	dedupeTokenOverlapThreshold = 0.28
)

var (
	defaultAllowedRoles = []string{
		roleSoftwareEngineer,
		roleBackend,
		roleFrontend,
		roleFullstack,
		roleAIEngineer,
		roleMLEngineer,
	}
	roleAliases = map[string]string{
		"software engineer":         roleSoftwareEngineer,
		"software_engineer":         roleSoftwareEngineer,
		"software developer":        roleSoftwareEngineer,
		"backend":                   roleBackend,
		"backend engineer":          roleBackend,
		"back end":                  roleBackend,
		"front end":                 roleFrontend,
		"frontend":                  roleFrontend,
		"frontend engineer":         roleFrontend,
		"full stack":                roleFullstack,
		"full-stack":                roleFullstack,
		"fullstack":                 roleFullstack,
		"ai engineer":               roleAIEngineer,
		"ai_engineer":               roleAIEngineer,
		"llm engineer":              roleAIEngineer,
		"applied ai":                roleAIEngineer,
		"genai engineer":            roleAIEngineer,
		"ml engineer":               roleMLEngineer,
		"ml_engineer":               roleMLEngineer,
		"machine learning engineer": roleMLEngineer,
		"mlops":                     roleMLEngineer,
	}
	seniorityAliases = map[string]string{
		"":               "",
		"any":            seniorityAny,
		"all":            seniorityAny,
		"none":           seniorityAny,
		"junior":         seniorityJunior,
		"jr":             seniorityJunior,
		"entry":          seniorityJunior,
		"entry level":    seniorityJunior,
		"graduate":       seniorityJunior,
		"new grad":       seniorityJunior,
		"intern":         seniorityJunior,
		"associate":      seniorityJunior,
		"mid":            seniorityMid,
		"mid level":      seniorityMid,
		"middle":         seniorityMid,
		"intermediate":   seniorityMid,
		"regular":        seniorityMid,
		"senior":         senioritySenior,
		"sr":             senioritySenior,
		"staff":          seniorityStaff,
		"lead":           seniorityStaff,
		"tech lead":      seniorityStaff,
		"principal":      seniorityPrincipal,
		"architect":      seniorityPrincipal,
		"distinguished":  seniorityPrincipal,
		"director":       seniorityDirector,
		"head":           seniorityDirector,
		"vp":             seniorityDirector,
		"vice president": seniorityDirector,
		"chief":          seniorityDirector,
		"cto":            seniorityDirector,
	}
	comparablePhraseReplacer = strings.NewReplacer(
		"back end", "backend",
		"front end", "frontend",
		"full stack", "fullstack",
		"machine learning", "ml",
		"artificial intelligence", "ai",
		"mid level", "mid",
		"entry level", "entry",
		"site reliability", "sre",
	)
	rolePatterns = []struct {
		role    string
		phrases []string
	}{
		{role: roleAIEngineer, phrases: []string{"ai engineer", "applied ai", "llm engineer", "genai engineer", "generative ai engineer"}},
		{role: roleMLEngineer, phrases: []string{"ml engineer", "machine learning engineer", "mlops engineer", "mlops"}},
		{role: roleFullstack, phrases: []string{"fullstack", "fullstack engineer", "fullstack developer"}},
		{role: roleBackend, phrases: []string{"backend", "backend engineer", "backend developer", "server side", "api engineer"}},
		{role: roleFrontend, phrases: []string{"frontend", "frontend engineer", "frontend developer", "ui engineer", "web engineer"}},
		{role: roleSoftwareEngineer, phrases: []string{"software engineer", "software developer", "application engineer", "application developer"}},
	}
	hardMismatchPhrases = []string{
		"product manager",
		"technical product manager",
		"product owner",
		"program manager",
		"project manager",
		"engineering manager",
		"designer",
		"ux designer",
		"ui designer",
		"marketing",
		"growth",
		"sales",
		"account executive",
		"customer success",
		"support specialist",
		"support manager",
		"recruiter",
		"talent partner",
		"human resources",
		"finance",
		"legal counsel",
		"operations manager",
		"business analyst",
		"scrum master",
		"developer advocate",
		"developer relations",
	}
	softMismatchPhrases = []string{
		"security",
		"infosec",
		"cybersecurity",
		"cyber security",
		"compliance",
		"governance",
		"privacy",
	}
	fallbackEngineeringPhrases = []string{
		"engineer",
		"developer",
		"programmer",
		"devops",
		"sre",
		"platform engineer",
		"mobile engineer",
		"ios engineer",
		"android engineer",
		"data engineer",
		"qa automation",
		"test automation",
	}
	dedupeStopwords = map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
		"for": {}, "from": {}, "has": {}, "have": {}, "in": {}, "into": {}, "is": {}, "it": {},
		"job": {}, "of": {}, "on": {}, "or": {}, "our": {}, "position": {}, "remote": {},
		"team": {}, "that": {}, "the": {}, "this": {}, "to": {}, "using": {}, "we": {},
		"will": {}, "with": {}, "work": {}, "working": {}, "worldwide": {}, "years": {},
		"year": {}, "you": {},
	}
)

type roleProfile struct {
	Roles             []string
	PrimaryRole       string
	Engineering       bool
	HardMismatch      bool
	SoftMismatch      bool
	SpecificTitleRole bool
}

type roleEvaluation struct {
	MatchedAllowed []string
	PrimaryRole    string
	Boost          float64
	Penalty        float64
	Exclude        bool
}

func supportedAllowedRoles() []string {
	return append([]string(nil), defaultAllowedRoles...)
}

func normalizeRoleID(value string) string {
	value = normalizeComparableText(value)
	if value == "" {
		return ""
	}
	if canonical, ok := roleAliases[value]; ok {
		return canonical
	}
	return ""
}

func normalizeAllowedRoles(values []string) []string {
	if len(values) == 0 {
		return append([]string(nil), defaultAllowedRoles...)
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		role := normalizeRoleID(value)
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return append([]string(nil), defaultAllowedRoles...)
	}
	return out
}

func normalizeSeniorityLevel(value string) string {
	value = normalizeComparableText(value)
	if value == "" {
		return ""
	}
	if canonical, ok := seniorityAliases[value]; ok {
		return canonical
	}
	return ""
}

func seniorityRank(level string) int {
	switch normalizeSeniorityLevel(level) {
	case seniorityJunior:
		return 1
	case seniorityMid:
		return 2
	case senioritySenior:
		return 3
	case seniorityStaff:
		return 4
	case seniorityPrincipal:
		return 5
	case seniorityDirector:
		return 6
	default:
		return 0
	}
}

func detectSeniorityLevel(title, tags string) string {
	text := normalizeComparableText(strings.Join([]string{title, tags}, " "))
	switch {
	case hasAnyPhrase(text, "director", "head", "vice president", "vp", "chief", "cto"):
		return seniorityDirector
	case hasAnyPhrase(text, "principal", "architect", "distinguished"):
		return seniorityPrincipal
	case hasAnyPhrase(text, "staff", "lead", "tech lead"):
		return seniorityStaff
	case hasAnyPhrase(text, "senior", "sr"):
		return senioritySenior
	case hasAnyPhrase(text, "mid", "intermediate", "regular"):
		return seniorityMid
	case hasAnyPhrase(text, "junior", "jr", "entry", "graduate", "new grad", "intern", "associate"):
		return seniorityJunior
	default:
		return ""
	}
}

func evaluateRoleFit(allowedRoles []string, title, tags, body string) roleEvaluation {
	profile := classifyRole(title, tags, body)
	eval := roleEvaluation{PrimaryRole: profile.PrimaryRole}

	allowedSet := make(map[string]struct{}, len(allowedRoles))
	for _, role := range normalizeAllowedRoles(allowedRoles) {
		allowedSet[role] = struct{}{}
	}
	for _, role := range profile.Roles {
		if _, ok := allowedSet[role]; ok {
			eval.MatchedAllowed = append(eval.MatchedAllowed, role)
		}
	}
	if len(eval.MatchedAllowed) > 0 {
		eval.Boost = 1.8
		if profile.SpecificTitleRole {
			eval.Boost += 0.8
		}
		if profile.PrimaryRole == roleAIEngineer || profile.PrimaryRole == roleMLEngineer {
			eval.Boost += 0.2
		}
		if len(eval.MatchedAllowed) > 1 {
			eval.Boost += 0.2 * float64(len(eval.MatchedAllowed)-1)
		}
		return eval
	}
	if profile.HardMismatch {
		eval.Penalty = 6
		eval.Exclude = true
		return eval
	}
	if profile.SoftMismatch {
		eval.Penalty = 2.75
		return eval
	}
	if profile.Engineering {
		eval.Penalty = 0.75
		return eval
	}
	eval.Penalty = 1.75
	return eval
}

func classifyRole(title, tags, body string) roleProfile {
	titleNorm := normalizeComparableText(title)
	tagsNorm := normalizeComparableText(tags)
	bodyNorm := normalizeComparableText(body)

	profile := roleProfile{}
	if hasAnyPhrase(titleNorm, hardMismatchPhrases...) {
		profile.HardMismatch = true
		return profile
	}
	titleRoles := matchedRoles(titleNorm)
	if len(titleRoles) > 0 {
		profile.Roles = titleRoles
		profile.PrimaryRole = titleRoles[0]
		profile.Engineering = true
		profile.SpecificTitleRole = true
		return profile
	}
	if hasAnyPhrase(titleNorm, softMismatchPhrases...) {
		profile.SoftMismatch = true
		if hasAnyPhrase(titleNorm, fallbackEngineeringPhrases...) {
			profile.Engineering = true
		}
		return profile
	}

	if roles := matchedRoles(tagsNorm); len(roles) > 0 {
		profile.Roles = roles
		profile.PrimaryRole = roles[0]
		profile.Engineering = true
		return profile
	}
	if roles := matchedRoles(bodyNorm); len(roles) > 0 {
		profile.Roles = roles
		profile.PrimaryRole = roles[0]
		profile.Engineering = true
		return profile
	}
	if hasAnyPhrase(tagsNorm, softMismatchPhrases...) {
		profile.SoftMismatch = true
	}

	if hasAnyPhrase(titleNorm, fallbackEngineeringPhrases...) ||
		hasAnyPhrase(tagsNorm, fallbackEngineeringPhrases...) ||
		hasAnyPhrase(bodyNorm, fallbackEngineeringPhrases...) {
		profile.Engineering = true
		if !profile.SoftMismatch {
			profile.Roles = []string{roleSoftwareEngineer}
			profile.PrimaryRole = roleSoftwareEngineer
		}
	}

	return profile
}

func matchedRoles(normalizedText string) []string {
	if normalizedText == "" {
		return nil
	}
	seen := make(map[string]struct{}, len(rolePatterns))
	var out []string
	for _, pattern := range rolePatterns {
		for _, phrase := range pattern.phrases {
			if !containsPhrase(normalizedText, phrase) {
				continue
			}
			if _, ok := seen[pattern.role]; ok {
				break
			}
			seen[pattern.role] = struct{}{}
			out = append(out, pattern.role)
			break
		}
	}
	return out
}

func normalizeComparableText(value string) string {
	value = strings.ToLower(cleanText(value))
	var b strings.Builder
	lastSpace := true
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	value = strings.Join(strings.Fields(b.String()), " ")
	value = comparablePhraseReplacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func tokenizeComparableText(value string) []string {
	value = normalizeComparableText(value)
	if value == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, token := range strings.Fields(value) {
		if len(token) < 2 {
			continue
		}
		if _, skip := dedupeStopwords[token]; skip {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func contentTokensForJob(title, description string) []string {
	tokens := tokenizeComparableText(strings.Join([]string{title, trimText(description, 1400)}, " "))
	if len(tokens) > 64 {
		return append([]string(nil), tokens[:64]...)
	}
	return tokens
}

func normalizeTitleForDedupe(title string) string {
	return normalizeComparableText(title)
}

func normalizeCompanyForDedupe(company string) string {
	return normalizeComparableText(company)
}

func dedupeGroupKey(company, normalizedTitle string) string {
	return normalizeCompanyForDedupe(company) + "|" + normalizeComparableText(normalizedTitle)
}

func tokenOverlapScore(left, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}

	leftSet := make(map[string]struct{}, len(left))
	for _, token := range left {
		leftSet[token] = struct{}{}
	}
	union := len(leftSet)
	shared := 0
	seenRight := make(map[string]struct{}, len(right))
	for _, token := range right {
		if _, ok := seenRight[token]; ok {
			continue
		}
		seenRight[token] = struct{}{}
		if _, ok := leftSet[token]; ok {
			shared++
			continue
		}
		union++
	}
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

func sameDedupeCluster(left RankedJob, right RankedJob) bool {
	if left.JobHash == right.JobHash {
		return true
	}
	if dedupeGroupKey(left.Company, left.NormalizedTitle) != dedupeGroupKey(right.Company, right.NormalizedTitle) {
		return false
	}
	if normalizeSeniorityLevel(left.SeniorityLevel) != normalizeSeniorityLevel(right.SeniorityLevel) {
		return false
	}
	return tokenOverlapScore(left.ContentTokens, right.ContentTokens) >= dedupeTokenOverlapThreshold
}

func sameRecentSeenJob(candidate RankedJob, seen RecentSeenJob) bool {
	if candidate.JobHash == seen.JobHash {
		return true
	}
	if dedupeGroupKey(candidate.Company, candidate.NormalizedTitle) != dedupeGroupKey(seen.Company, seen.NormalizedTitle) {
		return false
	}
	if normalizeSeniorityLevel(candidate.SeniorityLevel) != normalizeSeniorityLevel(seen.SeniorityLevel) {
		return false
	}
	return tokenOverlapScore(candidate.ContentTokens, seen.ContentTokens) >= dedupeTokenOverlapThreshold
}

func containsPhrase(normalizedText, phrase string) bool {
	normalizedText = strings.TrimSpace(normalizedText)
	phrase = normalizeComparableText(phrase)
	if normalizedText == "" || phrase == "" {
		return false
	}
	return strings.Contains(" "+normalizedText+" ", " "+phrase+" ")
}

func hasAnyPhrase(normalizedText string, phrases ...string) bool {
	for _, phrase := range phrases {
		if containsPhrase(normalizedText, phrase) {
			return true
		}
	}
	return false
}
