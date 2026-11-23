package analyzer

import "testing"

func TestClassifyMomentType(t *testing.T) {
	cases := []struct {
		name       string
		transcript string
		arcReason  string
		chats      []string
		want       string
	}{
		{"crash", "gua jago nyetir lihat nih nabrak tembok meledak", "setup -> nabrak -> ngakak", nil, "crash"},
		{"fail", "dikit lagi mati kena polisi dikepung baku tembak", "tembak -> mati", nil, "fail"},
		{"victory", "akhirnya menang clutch booyah mantap", "clutch -> payoff", nil, "victory"},
		{"twist", "ternyata plot twist kok bisa tiba-tiba begitu", "peak", nil, "twist"},
		{"meme", "wkwkwk ngakak kocak banget badut", "reaksi (wkwk)", []string{"wkwk"}, "meme"},
		{"hype", "gaspol let's go guys keren banget", "peak -> payoff", nil, "hype"},
	}
	for _, tc := range cases {
		got := ClassifyMomentType(tc.transcript, tc.arcReason, tc.chats)
		if got != tc.want {
			t.Fatalf("%s: expected momentType=%q, got %q", tc.name, tc.want, got)
		}
	}
}
