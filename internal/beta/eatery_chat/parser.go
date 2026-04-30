package eaterychat

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	urlRe           = regexp.MustCompile(`https?://[^\s<>()]+`)
	districtRe      = regexp.MustCompile(`(?i)\b(?:q(?:uan|uận)?\.?\s*|district\s+)([0-9]{1,2}|[a-z]+)\b`)
	locationRe      = regexp.MustCompile(`(?i)\b(?:o|ở|tai|tại|near|gan|gần)\s+([^,;\n]+)`)
	groupSizeRe     = regexp.MustCompile(`(?i)\b(?:(?:team|nhom|nhóm|group|party(?: of)?)\s*)?([0-9]{1,2})\s*(?:nguoi|người|people|pax)\b`)
	rangeBudgetRe   = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:k|ngan|ngàn)?\s*[-–]\s*([0-9]+(?:[.,][0-9]+)?)\s*(k|ngan|ngàn|tr|m)?`)
	singleBudgetRe  = regexp.MustCompile(`(?i)(?:~|khoang|khoảng|tam|tầm|duoi|dưới|under|<=?|budget\s*)?\s*([0-9]+(?:[.,][0-9]+)?)\s*(k|ngan|ngàn|tr|m)\b`)
	placePathRe     = regexp.MustCompile(`(?i)/maps/place/([^/?#]+)`)
	leadingNameTrim = regexp.MustCompile(`(?i)^(?:quan|quán|tiem|tiệm|an|ăn|di|đi|thu|thử|recommend|goi y|gợi ý)\s+`)
)

var categoryKeywords = []struct {
	Category string
	Terms    []string
}{
	{"Thai", []string{"do thai", "đồ thái", "thai food", "thai"}},
	{"Korean", []string{"han quoc", "hàn quốc", "korean", "kimchi", "bbq han"}},
	{"Japanese", []string{"nhat", "nhật", "japanese", "sushi", "ramen"}},
	{"Vietnamese", []string{"viet nam", "việt nam", "bun", "bún", "pho", "phở", "com tam", "cơm tấm"}},
	{"Chinese", []string{"trung hoa", "hoa", "chinese", "dimsum", "dimsum"}},
	{"Seafood", []string{"hai san", "hải sản", "seafood"}},
	{"Hotpot", []string{"lau", "lẩu", "hotpot"}},
	{"Grill", []string{"nuong", "nướng", "bbq", "grill"}},
	{"Vegetarian", []string{"chay", "vegetarian", "vegan"}},
	{"Cafe", []string{"cafe", "coffee", "ca phe", "cà phê"}},
	{"Dessert", []string{"dessert", "banh ngot", "bánh ngọt", "che", "chè"}},
	{"Pizza", []string{"pizza"}},
	{"Buffet", []string{"buffet"}},
	{"Bar", []string{"bar", "cocktail", "beer", "bia"}},
}

func parseChatText(text string) ParsedEatery {
	source := cleanText(text)
	parsed := ParsedEatery{SourceText: source}
	if source == "" {
		parsed.Reasons = append(parsed.Reasons, "empty text")
		return parsed
	}

	links := urlRe.FindAllString(source, -1)
	for _, link := range links {
		cleaned := strings.TrimRight(link, ".,;)")
		if isMapLink(cleaned) {
			parsed.MapLink = cleaned
			if parsed.Name == "" {
				parsed.Name = nameFromMapLink(cleaned)
			}
			parsed.Reasons = append(parsed.Reasons, "map link found")
			break
		}
	}

	textWithoutLinks := cleanText(urlRe.ReplaceAllString(source, " "))
	parsed.District = extractDistrict(textWithoutLinks)
	parsed.Category = extractCategory(textWithoutLinks)
	parsed.PriceHint, parsed.BudgetMin, parsed.BudgetMax = extractBudget(textWithoutLinks)
	parsed.Tags = extractTags(textWithoutLinks, parsed.BudgetMax)
	if parsed.Name == "" {
		parsed.Name = extractName(textWithoutLinks)
	}
	parsed.Address = extractAddress(textWithoutLinks, parsed.District)
	parsed.Notes = textWithoutLinks
	parsed.Confidence = scoreParsed(parsed)
	return parsed
}

func isMapLink(link string) bool {
	lower := strings.ToLower(link)
	return strings.Contains(lower, "google.com/maps") ||
		strings.Contains(lower, "maps.app.goo.gl") ||
		strings.Contains(lower, "goo.gl/maps")
}

func nameFromMapLink(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if match := placePathRe.FindStringSubmatch(u.EscapedPath()); len(match) == 2 {
		value, _ := url.PathUnescape(match[1])
		value = strings.ReplaceAll(value, "+", " ")
		return cleanText(value)
	}
	return ""
}

func extractDistrict(text string) string {
	if match := districtRe.FindStringSubmatch(text); len(match) == 2 {
		value := strings.ToUpper(strings.TrimSpace(match[1]))
		if value == "" {
			return ""
		}
		return "Q" + value
	}
	return ""
}

func extractCategory(text string) string {
	key := normalizeComparableText(text)
	for _, item := range categoryKeywords {
		for _, term := range item.Terms {
			if strings.Contains(key, normalizeComparableText(term)) {
				return item.Category
			}
		}
	}
	return ""
}

func extractBudget(text string) (string, int, int) {
	if match := rangeBudgetRe.FindStringSubmatch(text); len(match) == 4 {
		minValue := parseMoney(match[1], match[3])
		maxValue := parseMoney(match[2], match[3])
		if minValue > 0 && maxValue > 0 {
			if minValue > maxValue {
				minValue, maxValue = maxValue, minValue
			}
			return cleanText(match[0]), minValue, maxValue
		}
	}
	if match := singleBudgetRe.FindStringSubmatch(text); len(match) == 3 {
		value := parseMoney(match[1], match[2])
		if value > 0 {
			return cleanText(match[0]), 0, value
		}
	}
	key := normalizeComparableText(text)
	if strings.Contains(key, "cheap") || strings.Contains(key, "re") || strings.Contains(key, "binh dan") {
		return "cheap", 0, 100000
	}
	if strings.Contains(key, "premium") || strings.Contains(key, "sang") || strings.Contains(key, "fine dining") {
		return "premium", 300000, 0
	}
	return "", 0, 0
}

func parseMoney(raw, unit string) int {
	raw = strings.ReplaceAll(raw, ",", ".")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 0
	}
	unit = normalizeComparableText(unit)
	switch unit {
	case "tr", "m":
		return int(value * 1000000)
	case "k", "ngan":
		return int(value * 1000)
	default:
		if value < 1000 {
			return int(value * 1000)
		}
		return int(value)
	}
}

func extractTags(text string, budgetMax int) []string {
	key := normalizeComparableText(text)
	var tags []string
	if strings.Contains(key, "cheap") || strings.Contains(key, "re") || strings.Contains(key, "binh dan") || (budgetMax > 0 && budgetMax <= 120000) {
		tags = append(tags, "cheap")
	}
	if strings.Contains(key, "date") || strings.Contains(key, "hen ho") || strings.Contains(key, "nguoi yeu") || strings.Contains(key, "couple") {
		tags = append(tags, "date")
	}
	if strings.Contains(key, "chill") || strings.Contains(key, "yen tinh") || strings.Contains(key, "cozy") {
		tags = append(tags, "chill")
	}
	if strings.Contains(key, "group") || strings.Contains(key, "team") || strings.Contains(key, "nhom") || extractGroupSize(text) >= 4 {
		tags = append(tags, "group")
	}
	if strings.Contains(key, "local") || strings.Contains(key, "dia phuong") || strings.Contains(key, "quan quen") {
		tags = append(tags, "local")
	}
	if strings.Contains(key, "premium") || strings.Contains(key, "sang") || strings.Contains(key, "fine dining") {
		tags = append(tags, "premium")
	}
	return normalizeTags(tags)
}

func extractName(text string) string {
	for _, part := range splitLoose(text) {
		part = cleanText(part)
		if part == "" {
			continue
		}
		part = leadingNameTrim.ReplaceAllString(part, "")
		if idx := locationWordIndex(part); idx > 0 {
			part = strings.TrimSpace(part[:idx])
		}
		if looksLikeMetadata(part) {
			continue
		}
		return cleanText(part)
	}
	return ""
}

func splitLoose(text string) []string {
	replacer := strings.NewReplacer("\n", ",", ";", ",", " - ", ",")
	return strings.Split(replacer.Replace(text), ",")
}

func locationWordIndex(text string) int {
	lower := strings.ToLower(text)
	for _, needle := range []string{" ở ", " o ", " tại ", " tai ", " near ", " gần ", " gan "} {
		if idx := strings.Index(lower, needle); idx > 0 {
			return idx
		}
	}
	return -1
}

func looksLikeMetadata(text string) bool {
	key := normalizeComparableText(text)
	if key == "" {
		return true
	}
	if districtRe.MatchString(text) || singleBudgetRe.MatchString(text) {
		return true
	}
	if extractCategory(text) != "" && len(strings.Fields(key)) <= 3 {
		return true
	}
	for _, tag := range []string{"cheap", "date", "hen ho", "group", "team", "chill", "local", "premium", "hop", "hợp"} {
		if strings.Contains(key, normalizeComparableText(tag)) {
			return true
		}
	}
	return false
}

func extractAddress(text, district string) string {
	for _, match := range locationRe.FindAllStringSubmatch(text, -1) {
		if len(match) != 2 {
			continue
		}
		value := cleanText(match[1])
		if value == "" {
			continue
		}
		if district != "" && normalizeComparableText(value) == normalizeComparableText(district) {
			continue
		}
		if districtRe.MatchString(value) && len(strings.Fields(value)) <= 2 {
			continue
		}
		return value
	}
	return ""
}

func scoreParsed(parsed ParsedEatery) float64 {
	score := 0.0
	if parsed.Name != "" {
		score += 0.35
	}
	if parsed.MapLink != "" {
		score += 0.2
	}
	if parsed.Address != "" {
		score += 0.2
	}
	if parsed.District != "" {
		score += 0.1
	}
	if parsed.Category != "" {
		score += 0.1
	}
	if parsed.PriceHint != "" {
		score += 0.08
	}
	if len(parsed.Tags) > 0 {
		score += 0.05
	}
	if score > 1 {
		return 1
	}
	return score
}

func parseRecommendationConstraints(req RecommendRequest) RecommendationConstraints {
	prompt := cleanText(req.Prompt)
	priceHint, _, maxBudget := extractBudget(prompt)
	_ = priceHint
	constraints := RecommendationConstraints{
		Prompt:    prompt,
		District:  cleanText(req.District),
		Category:  cleanText(req.Category),
		Tags:      normalizeTags(req.Tags),
		MaxBudget: req.MaxBudget,
		GroupSize: req.GroupSize,
		Search:    cleanText(req.Search),
		Limit:     normalizeLimit(req.Limit, defaultRecommendMax),
	}
	if constraints.District == "" {
		constraints.District = extractDistrict(prompt)
	}
	if constraints.Category == "" {
		constraints.Category = extractCategory(prompt)
	}
	if constraints.MaxBudget <= 0 {
		constraints.MaxBudget = maxBudget
	}
	if constraints.GroupSize <= 0 {
		constraints.GroupSize = extractGroupSize(prompt)
	}
	constraints.Tags = mergeTags(constraints.Tags, extractTags(prompt, constraints.MaxBudget))
	if constraints.GroupSize >= 4 {
		constraints.Tags = mergeTags(constraints.Tags, []string{"group"})
	}
	if constraints.Search == "" {
		constraints.Search = prompt
	}
	return constraints
}

func extractGroupSize(text string) int {
	if match := groupSizeRe.FindStringSubmatch(text); len(match) == 2 {
		value, _ := strconv.Atoi(match[1])
		return value
	}
	return 0
}
