package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"klipforge/internal/model"
)

func TestViewsLedgerAddListFilterAndReloadPersistence(t *testing.T) {
	tmp := t.TempDir()
	st, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	t1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 22, 15, 30, 0, 0, time.UTC)
	postedCustom := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)

	e1, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:      "job-1",
		ProjectID:  "proj-1",
		Title:      "Red Hulk Smash",
		Platform:   " TikTok ",
		PostURL:    "https://tiktok.com/@test/video/1",
		PostedAt:   postedCustom,
		RecordedAt: t1,
		Views:      1200,
		Likes:      150,
		Comments:   12,
		Shares:     25,
		WatchSec:   18.5,
		Format:     "16:9",
		Note:       "first manual post",
		RecordedBy: "owner",
	})
	if err != nil {
		t.Fatalf("AddViewsLedgerEntry e1 failed: %v", err)
	}
	if !strings.HasPrefix(e1.ID, "vl-") {
		t.Fatalf("expected auto-filled ID starting with 'vl-', got %q", e1.ID)
	}
	if e1.Platform != "tiktok" {
		t.Fatalf("expected normalized platform 'tiktok', got %q", e1.Platform)
	}
	if e1.SampleSplit != "IN-SAMPLE" {
		t.Fatalf("expected default SampleSplit 'IN-SAMPLE', got %q", e1.SampleSplit)
	}
	if !e1.PostedAt.Equal(postedCustom) {
		t.Fatalf("expected custom PostedAt %v preserved, got %v", postedCustom, e1.PostedAt)
	}

	e2, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:       "job-1",
		Title:       "Red Hulk Smash YT",
		Platform:    "YouTube",
		RecordedAt:  t2,
		Views:       5400,
		Likes:       410,
		Comments:    33,
		Shares:      19,
		SampleSplit: " held-out ",
	})
	if err != nil {
		t.Fatalf("AddViewsLedgerEntry e2 failed: %v", err)
	}
	if e2.ID == e1.ID {
		t.Fatalf("expected unique IDs, got duplicate %q", e2.ID)
	}
	if e2.Platform != "youtube" {
		t.Fatalf("expected normalized platform 'youtube', got %q", e2.Platform)
	}
	if e2.SampleSplit != "HELD-OUT" {
		t.Fatalf("expected normalized SampleSplit 'HELD-OUT', got %q", e2.SampleSplit)
	}
	if !e2.PostedAt.Equal(e2.RecordedAt) {
		t.Fatalf("expected zero PostedAt to default to RecordedAt (%v), got %v", e2.RecordedAt, e2.PostedAt)
	}

	// Entry with zero RecordedAt and zero PostedAt should auto-fill both
	e3, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:       "job-2",
		Title:       "Sepeda Mental Ragdoll",
		Platform:    "INSTAGRAM",
		Views:       3100,
		Likes:       220,
		Comments:    14,
		Shares:      40,
		SampleSplit: "in-sample",
	})
	if err != nil {
		t.Fatalf("AddViewsLedgerEntry e3 failed: %v", err)
	}
	if e3.RecordedAt.IsZero() {
		t.Fatalf("expected RecordedAt auto-filled")
	}
	if !e3.PostedAt.Equal(e3.RecordedAt) {
		t.Fatalf("expected PostedAt to default to auto-filled RecordedAt")
	}

	// Verify explicit ordering when RecordedAt is set
	e4, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:       "job-2",
		Title:       "Older Historical Record",
		Platform:    "tiktok",
		RecordedAt:  t3,
		Views:       800,
		SampleSplit: "HELD-OUT",
	})
	if err != nil {
		t.Fatalf("AddViewsLedgerEntry e4 failed: %v", err)
	}

	// List all: newest RecordedAt first (e3 was time.Now() in 2026-11-25 > t3 > t2 > t1)
	all := st.ListViewsLedger("", "", "")
	if len(all) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(all))
	}
	if all[0].ID != e3.ID || all[1].ID != e4.ID || all[2].ID != e2.ID || all[3].ID != e1.ID {
		t.Fatalf("unexpected sort order: got [%s, %s, %s, %s], want [%s, %s, %s, %s]",
			all[0].ID, all[1].ID, all[2].ID, all[3].ID, e3.ID, e4.ID, e2.ID, e1.ID)
	}

	// Defensive copy check: mutating returned entry must not affect stored state
	all[0].Title = "MUTATED"
	all[0].Views = 999999
	fresh := st.ListViewsLedger("job-2", "instagram", "")
	if len(fresh) != 1 || fresh[0].Title != "Sepeda Mental Ragdoll" || fresh[0].Views != 3100 {
		t.Fatalf("defensive copy broken: got %+v", fresh)
	}

	// Filter by jobId
	byJob1 := st.ListViewsLedger("job-1", "", "")
	if len(byJob1) != 2 {
		t.Fatalf("expected 2 entries for job-1, got %d", len(byJob1))
	}

	// Filter by platform (case-insensitive)
	byTikTok := st.ListViewsLedger("", "TiKtOk", "")
	if len(byTikTok) != 2 {
		t.Fatalf("expected 2 tiktok entries, got %d", len(byTikTok))
	}

	// Filter by sampleSplit (case-insensitive)
	byHeldOut := st.ListViewsLedger("", "", "held-out")
	if len(byHeldOut) != 2 {
		t.Fatalf("expected 2 HELD-OUT entries, got %d", len(byHeldOut))
	}

	// Combined filters
	combo := st.ListViewsLedger("job-1", "YOUTUBE", "held-out")
	if len(combo) != 1 || combo[0].ID != e2.ID {
		t.Fatalf("expected 1 combined match (%s), got %+v", e2.ID, combo)
	}

	// Verify file persisted to data/views_ledger.json
	ledgerPath := filepath.Join(tmp, "data", "views_ledger.json")
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("expected %s to exist: %v", ledgerPath, err)
	}

	// Reload from disk via fresh NewStore
	st2, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore reload failed: %v", err)
	}
	reloaded := st2.ListViewsLedger("", "", "")
	if len(reloaded) != 4 {
		t.Fatalf("expected 4 entries after reload, got %d", len(reloaded))
	}
	if reloaded[3].ID != e1.ID || reloaded[3].Views != 1200 || reloaded[3].Format != "16:9" {
		t.Fatalf("unexpected reloaded entry: %+v", reloaded[3])
	}
}

func TestViewsLedgerValidation(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	cases := []struct {
		name  string
		entry model.ViewsLedgerEntry
	}{
		{
			name:  "empty jobId",
			entry: model.ViewsLedgerEntry{JobID: "", Platform: "tiktok", Views: 100},
		},
		{
			name:  "whitespace jobId",
			entry: model.ViewsLedgerEntry{JobID: "   ", Platform: "tiktok", Views: 100},
		},
		{
			name:  "empty platform",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "", Views: 100},
		},
		{
			name:  "unsupported platform",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "facebook", Views: 100},
		},
		{
			name:  "negative views",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "tiktok", Views: -1},
		},
		{
			name:  "negative likes",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "youtube", Views: 10, Likes: -2},
		},
		{
			name:  "negative comments",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "instagram", Views: 10, Comments: -3},
		},
		{
			name:  "negative shares",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "tiktok", Views: 10, Shares: -4},
		},
		{
			name:  "invalid sampleSplit",
			entry: model.ViewsLedgerEntry{JobID: "job-1", Platform: "tiktok", Views: 10, SampleSplit: "DUGAAN"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.AddViewsLedgerEntry(tc.entry)
			if err == nil {
				t.Fatalf("expected error for %s, got entry %+v", tc.name, got)
			}
		})
	}

	if entries := st.ListViewsLedger("", "", ""); len(entries) != 0 {
		t.Fatalf("expected 0 entries after failed validations, got %d", len(entries))
	}
}

func TestSummarizeViewsLedger(t *testing.T) {
	// Empty summary
	empty := SummarizeViewsLedger(nil)
	if empty.TotalEntries != 0 || empty.TotalViews != 0 || empty.MedianViews != 0 {
		t.Fatalf("expected zeroed empty summary, got %+v", empty)
	}
	if empty.ByPlatform == nil || empty.BySplit == nil {
		t.Fatalf("expected non-nil maps on empty summary, got %+v", empty)
	}
	if !strings.Contains(empty.DataScopeNote, "IN-SAMPLE") || !strings.Contains(empty.DataScopeNote, "HELD-OUT") {
		t.Fatalf("expected DataScopeNote warning referencing IN-SAMPLE and HELD-OUT, got %q", empty.DataScopeNote)
	}

	// Odd total count (3 entries)
	entriesOdd := []*model.ViewsLedgerEntry{
		{ID: "1", JobID: "j1", Platform: "tiktok", SampleSplit: "IN-SAMPLE", Views: 1000, Likes: 100, Comments: 10, Shares: 20},
		{ID: "2", JobID: "j2", Platform: "tiktok", SampleSplit: "IN-SAMPLE", Views: 5000, Likes: 500, Comments: 50, Shares: 80},
		{ID: "3", JobID: "j3", Platform: "youtube", SampleSplit: "HELD-OUT", Views: 2000, Likes: 150, Comments: 15, Shares: 10},
	}
	sumOdd := SummarizeViewsLedger(entriesOdd)
	if sumOdd.TotalEntries != 3 {
		t.Fatalf("expected TotalEntries=3, got %d", sumOdd.TotalEntries)
	}
	if sumOdd.TotalViews != 8000 || sumOdd.TotalLikes != 750 || sumOdd.TotalComments != 75 || sumOdd.TotalShares != 110 {
		t.Fatalf("unexpected totals: %+v", sumOdd)
	}
	if sumOdd.MedianViews != 2000 {
		t.Fatalf("expected MedianViews=2000, got %d", sumOdd.MedianViews)
	}

	tt := sumOdd.ByPlatform["tiktok"]
	if tt.Entries != 2 || tt.TotalViews != 6000 || tt.MedianViews != 3000 || tt.TotalLikes != 600 || tt.TotalShares != 100 {
		t.Fatalf("unexpected tiktok bucket stats: %+v", tt)
	}
	yt := sumOdd.ByPlatform["youtube"]
	if yt.Entries != 1 || yt.TotalViews != 2000 || yt.MedianViews != 2000 {
		t.Fatalf("unexpected youtube bucket stats: %+v", yt)
	}
	inSample := sumOdd.BySplit["IN-SAMPLE"]
	if inSample.Entries != 2 || inSample.TotalViews != 6000 || inSample.MedianViews != 3000 {
		t.Fatalf("unexpected IN-SAMPLE split stats: %+v", inSample)
	}
	heldOut := sumOdd.BySplit["HELD-OUT"]
	if heldOut.Entries != 1 || heldOut.TotalViews != 2000 || heldOut.MedianViews != 2000 {
		t.Fatalf("unexpected HELD-OUT split stats: %+v", heldOut)
	}

	// Even total count (4 entries: sorted [100, 200, 400, 1000] -> median 300)
	entriesEven := []*model.ViewsLedgerEntry{
		{ID: "a", JobID: "ja", Platform: "instagram", SampleSplit: "IN-SAMPLE", Views: 1000},
		{ID: "b", JobID: "jb", Platform: "instagram", SampleSplit: "IN-SAMPLE", Views: 100},
		{ID: "c", JobID: "jc", Platform: "tiktok", SampleSplit: "HELD-OUT", Views: 400},
		{ID: "d", JobID: "jd", Platform: "youtube", SampleSplit: "HELD-OUT", Views: 200},
	}
	sumEven := SummarizeViewsLedger(entriesEven)
	if sumEven.MedianViews != 300 {
		t.Fatalf("expected even-count MedianViews=300, got %d", sumEven.MedianViews)
	}
}

func TestViewsLedgerBackupRecovery(t *testing.T) {
	tmp := t.TempDir()
	st, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	first, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:    "job-recover-1",
		Title:    "Clip Pertama",
		Platform: "tiktok",
		Views:    950,
	})
	if err != nil {
		t.Fatalf("AddViewsLedgerEntry 1 failed: %v", err)
	}
	if _, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:    "job-recover-2",
		Title:    "Clip Kedua",
		Platform: "youtube",
		Views:    2400,
	}); err != nil {
		t.Fatalf("AddViewsLedgerEntry 2 failed: %v", err)
	}

	ledgerPath := filepath.Join(tmp, "data", "views_ledger.json")
	if _, err := os.Stat(ledgerPath + ".bak.1"); err != nil {
		t.Fatalf("expected rolling backup .bak.1 to exist: %v", err)
	}

	// Corrupt primary file
	if err := os.WriteFile(ledgerPath, []byte("{broken json"), 0o644); err != nil {
		t.Fatalf("failed to corrupt ledger file: %v", err)
	}

	st2, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore reload failed: %v", err)
	}
	got := st2.ListViewsLedger("job-recover-1", "", "")
	if len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("expected job-recover-1 recovered from .bak.1, got %+v", got)
	}
}
