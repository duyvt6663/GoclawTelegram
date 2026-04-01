package tools

import (
	"strings"
	"unicode"
)

var reactionMediaQueryPhrases = []string{
	"not impressed",
	"dead inside",
	"bro please",
	"side eye",
	"caught in 4k",
	"skill issue",
	"too easy",
	"calm down",
}

var reactionMediaVibeTerms = map[string]bool{
	"angry": true, "annoyed": true, "applause": true, "awkward": true, "begging": true,
	"bored": true, "bro": true, "calm": true, "chill": true, "clown": true,
	"confused": true, "cope": true, "coping": true, "crying": true, "cute": true,
	"dead": true, "desperate": true, "done": true, "evil": true, "facepalm": true,
	"gloating": true, "happy": true, "hyped": true, "impressed": true, "judge": true,
	"judging": true, "lol": true, "lmao": true, "menace": true, "mocking": true,
	"nah": true, "nope": true, "please": true, "pleading": true, "praying": true,
	"proud": true, "rage": true, "salty": true, "sarcastic": true, "serious": true,
	"shocked": true, "shy": true, "side": true, "skill": true, "smirk": true,
	"smug": true, "sorry": true, "suspicious": true, "teasing": true, "thinking": true,
	"tired": true, "triggered": true, "unbothered": true, "victory": true, "vibe": true,
	"wow": true, "yikes": true,
}

func normalizeReactionMediaQuery(query string) string {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return ""
	}

	var parts []string
	seen := make(map[string]bool)
	for _, phrase := range reactionMediaQueryPhrases {
		if strings.Contains(query, phrase) {
			parts = append(parts, phrase)
			seen[phrase] = true
		}
	}

	for _, token := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if token == "" || !reactionMediaVibeTerms[token] {
			continue
		}
		if token == "side" && seen["side eye"] {
			continue
		}
		if seen[token] {
			continue
		}
		parts = append(parts, token)
		seen[token] = true
	}

	if len(parts) == 0 {
		return query
	}
	return strings.Join(parts, " ")
}
