package store

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"klipforge/internal/model"
)

const viewsLedgerScopeNote = "PERINGATAN DATA SCOPE: Metrik IN-SAMPLE hanya observasi historis/internal dan TIDAK BOLEH dianggap sebagai bukti performa HELD-OUT (warning: never treat IN-SAMPLE stats as HELD-OUT proof)."

var viewsLedgerSeq atomic.Uint64

// AddViewsLedgerEntry validates, normalizes, and atomically persists a manual post performance record to data/views_ledger.json.
func (s *Store) AddViewsLedgerEntry(entry model.ViewsLedgerEntry) (*model.ViewsLedgerEntry, error) {
	entry.JobID = strings.TrimSpace(entry.JobID)
	if entry.JobID == "" {
		return nil, fmt.Errorf("jobId is required")
	}

	entry.Platform = strings.ToLower(strings.TrimSpace(entry.Platform))
	switch entry.Platform {
	case "tiktok", "youtube", "instagram":
	default:
		return nil, fmt.Errorf("invalid platform %q: must be tiktok, youtube, or instagram", entry.Platform)
	}

	if entry.Views < 0 {
		return nil, fmt.Errorf("views must be >= 0")
	}
	if entry.Likes < 0 {
		return nil, fmt.Errorf("likes must be >= 0")
	}
	if entry.Comments < 0 {
		return nil, fmt.Errorf("comments must be >= 0")
	}
	if entry.Shares < 0 {
		return nil, fmt.Errorf("shares must be >= 0")
	}
	if entry.WatchSec < 0 {
		return nil, fmt.Errorf("watchSec must be >= 0")
	}

	split := strings.ToUpper(strings.TrimSpace(entry.SampleSplit))
	if split == "" {
		split = "IN-SAMPLE"
	}
	if split != "IN-SAMPLE" && split != "HELD-OUT" {
		return nil, fmt.Errorf("invalid sampleSplit %q: must be IN-SAMPLE or HELD-OUT", entry.SampleSplit)
	}
	entry.SampleSplit = split

	entry.ProjectID = strings.TrimSpace(entry.ProjectID)
	entry.Title = strings.TrimSpace(entry.Title)
	entry.PostURL = strings.TrimSpace(entry.PostURL)
	entry.Format = strings.TrimSpace(entry.Format)
	entry.Note = strings.TrimSpace(entry.Note)
	entry.RecordedBy = strings.TrimSpace(entry.RecordedBy)

	now := time.Now()
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = now
	}
	if entry.PostedAt.IsZero() {
		entry.PostedAt = entry.RecordedAt
	}
	entry.ID = strings.TrimSpace(entry.ID)
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("vl-%d-%d", now.UnixNano(), viewsLedgerSeq.Add(1))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	prevLen := len(s.viewsLedger)
	stored := entry
	s.viewsLedger = append(s.viewsLedger, &stored)
	if err := writeJSONAtomic(filepath.Join(s.DataDir, "views_ledger.json"), s.viewsLedger); err != nil {
		s.viewsLedger = s.viewsLedger[:prevLen]
		return nil, err
	}
	cp := stored
	return &cp, nil
}

// ListViewsLedger returns defensive copies of ledger entries sorted newest RecordedAt first,
// optionally filtered by jobID, platform (case-insensitive), and sampleSplit (case-insensitive).
func (s *Store) ListViewsLedger(jobID, platform, sampleSplit string) []*model.ViewsLedgerEntry {
	jobFilter := strings.TrimSpace(jobID)
	platFilter := strings.ToLower(strings.TrimSpace(platform))
	splitFilter := strings.ToUpper(strings.TrimSpace(sampleSplit))

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*model.ViewsLedgerEntry, 0, len(s.viewsLedger))
	for i := len(s.viewsLedger) - 1; i >= 0; i-- {
		e := s.viewsLedger[i]
		if e == nil {
			continue
		}
		if jobFilter != "" && e.JobID != jobFilter {
			continue
		}
		if platFilter != "" && strings.ToLower(e.Platform) != platFilter {
			continue
		}
		if splitFilter != "" && strings.ToUpper(e.SampleSplit) != splitFilter {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].RecordedAt.After(out[j].RecordedAt)
	})
	return out
}

// SummarizeViewsLedger computes totals, median views, and platform/sampleSplit breakdowns
// along with an explicit warning never to conflate IN-SAMPLE observations with HELD-OUT proof.
func SummarizeViewsLedger(entries []*model.ViewsLedgerEntry) model.ViewsLedgerSummary {
	summary := model.ViewsLedgerSummary{
		ByPlatform:    make(map[string]model.ViewsBucketStats),
		BySplit:       make(map[string]model.ViewsBucketStats),
		DataScopeNote: viewsLedgerScopeNote,
	}

	type bucketAcc struct {
		stats model.ViewsBucketStats
		views []int64
	}

	allViews := make([]int64, 0, len(entries))
	platAcc := make(map[string]*bucketAcc)
	splitAcc := make(map[string]*bucketAcc)

	addToBucket := func(m map[string]*bucketAcc, key string, e *model.ViewsLedgerEntry) {
		acc, ok := m[key]
		if !ok {
			acc = &bucketAcc{}
			m[key] = acc
		}
		acc.stats.Entries++
		acc.stats.TotalViews += e.Views
		acc.stats.TotalLikes += e.Likes
		acc.stats.TotalComments += e.Comments
		acc.stats.TotalShares += e.Shares
		acc.views = append(acc.views, e.Views)
	}

	for _, e := range entries {
		if e == nil {
			continue
		}
		summary.TotalEntries++
		summary.TotalViews += e.Views
		summary.TotalLikes += e.Likes
		summary.TotalComments += e.Comments
		summary.TotalShares += e.Shares
		allViews = append(allViews, e.Views)

		plat := strings.ToLower(strings.TrimSpace(e.Platform))
		if plat == "" {
			plat = "unknown"
		}
		addToBucket(platAcc, plat, e)

		split := strings.ToUpper(strings.TrimSpace(e.SampleSplit))
		if split == "" {
			split = "IN-SAMPLE"
		}
		addToBucket(splitAcc, split, e)
	}

	summary.MedianViews = computeMedianInt64(allViews)
	for k, acc := range platAcc {
		acc.stats.MedianViews = computeMedianInt64(acc.views)
		summary.ByPlatform[k] = acc.stats
	}
	for k, acc := range splitAcc {
		acc.stats.MedianViews = computeMedianInt64(acc.views)
		summary.BySplit[k] = acc.stats
	}

	return summary
}

func computeMedianInt64(vals []int64) int64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	sorted := make([]int64, n)
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	mid := n / 2
	if n%2 == 1 {
		return sorted[mid]
	}
	return sorted[mid-1] + (sorted[mid]-sorted[mid-1])/2
}
