package audiofx

import "testing"

func TestTrendingFilterAndURLValidation(t *testing.T) {
	spec := resolveTrendingQuery("id_viral", "")
	raw := []TrendingSound{
		{ID: "mix1", Title: "Lagu TikTok 2026 Full Album Nonstop 1 Hour", SourceURL: "https://youtube.com/watch?v=mix1", Duration: 3900, ViewCount: 9999999},
		{ID: "s1", Title: "Sound Viral Jedag Jedug FYP", SourceURL: "https://youtube.com/watch?v=s1", Duration: 32, ViewCount: 250000},
		{ID: "s2", Title: "Roblox Ahh Viral TikTok SFX", SourceURL: "https://youtube.com/watch?v=s2", Duration: 9, ViewCount: 890000},
		{ID: "s1", Title: "Duplicate", SourceURL: "https://youtube.com/watch?v=s1", Duration: 32, ViewCount: 250000},
	}

	ranked := filterAndRankTrending(raw, spec, 10)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 valid unique non-album tracks, got %d (%+v)", len(ranked), ranked)
	}
	if ranked[0].ID != "s2" || ranked[0].SuggestedKind != "sfx" {
		t.Fatalf("expected top item s2 (890k views, short -> suggestedKind=sfx), got %+v", ranked[0])
	}
	if ranked[1].ID != "s1" || ranked[1].SuggestedKind != "bgm" {
		t.Fatalf("expected second item s1 (250k views, suggestedKind=bgm), got %+v", ranked[1])
	}

	if _, err := ValidateImportURL("https://www.tiktok.com/@user/video/123456"); err != nil {
		t.Fatalf("expected valid tiktok https URL, got %v", err)
	}
	if _, err := ValidateImportURL("file:///etc/passwd"); err == nil {
		t.Fatalf("expected file:// URL to be rejected")
	}
}
