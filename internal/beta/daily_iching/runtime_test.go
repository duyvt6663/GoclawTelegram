package dailyiching

import (
	"strings"
	"testing"
)

func TestGroundingForHexagramPrefersCleanOverviewChunks(t *testing.T) {
	t.Parallel()

	index := prepareBookIndex(&bookIndex{
		Sections: []hexagramSection{
			{
				Number:        1,
				Name:          "Càn",
				Title:         "Thuần Càn",
				DisplaySource: "local.pdf",
				Chunks: []bookChunk{
					{Text: `1. BAT THUAN KIEN" LL D0, FE ae ee mm`},
					{Text: "Quẻ có 6 hào dương. Hào dương là nguyên lực rất mạnh và rất hoạt động."},
					{Text: "Ở bình diện hình nhi hạ, người quân tử giữ đúng vị thế và đúng thời trước khi hành động."},
					{Text: "Ở bình diện hình nhi thượng, quẻ này nói về nguyên lý sáng tạo và sự vận hành của trời đất."},
				},
			},
		},
	})

	feature := &DailyIChingFeature{index: index}
	grounding, err := feature.groundingForHexagram(1)
	if err != nil {
		t.Fatalf("groundingForHexagram() error = %v", err)
	}

	overview := normalizeComparableText(grounding.OverviewText)
	if strings.Contains(overview, "bat thuan kien") {
		t.Fatalf("grounding overview still contains noisy heading chunk: %q", grounding.OverviewText)
	}
	if !strings.Contains(overview, "hao duong la nguyen luc rat manh") {
		t.Fatalf("grounding overview = %q, want clean body text", grounding.OverviewText)
	}
}
