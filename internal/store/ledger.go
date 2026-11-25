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

// NormalizeCohortTag validates and normalizes a v1.2a cohortTag ("PRE-v1.2" | "POST-v1.2", default "POST-v1.2").
func NormalizeCohortTag(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "POST-v1.2", nil
	}
	if strings.EqualFold(trimmed, "PRE-v1.2") {
		return "PRE-v1.2", nil
	}
	if strings.EqualFold(trimmed, "POST-v1.2") {
		return "POST-v1.2", nil
	}
	return "", fmt.Errorf("invalid cohortTag %q: must be PRE-v1.2 or POST-v1.2", raw)
}

// NormalizeHookType validates and normalizes a v1.2a hookType ("" | "tableau" | "reaksi" | "aksi" | "text_on_screen").
func NormalizeHookType(raw string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(raw))
	switch norm {
	case "", "tableau", "reaksi", "aksi", "text_on_screen":
		return norm, nil
	default:
		return "", fmt.Errorf("invalid hookType %q: must be tableau, reaksi, aksi, or text_on_screen", raw)
	}
}

// NormalizeTriggerSource validates and normalizes a v1.2a triggerSource ("" | "manual" | "transcript" | "chat" | "audio" | "llm").
func NormalizeTriggerSource(raw string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(raw))
	switch norm {
	case "", "manual", "transcript", "chat", "audio", "llm":
		return norm, nil
	default:
		return "", fmt.Errorf("invalid triggerSource %q: must be manual, transcript, chat, audio, or llm", raw)
	}
}

func countH1Flags(tawa, visual, arc, mute bool) int {
	n := 0
	if tawa {
		n++
	}
	if visual {
		n++
	}
	if arc {
		n++
	}
	if mute {
		n++
	}
	return n
}

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
	if entry.Views24h < 0 {
		return nil, fmt.Errorf("views24h must be >= 0")
	}
	if entry.Views7d < 0 {
		return nil, fmt.Errorf("views7d must be >= 0")
	}
	if entry.Views == 0 {
		if entry.Views7d > 0 {
			entry.Views = entry.Views7d
		} else if entry.Views24h > 0 {
			entry.Views = entry.Views24h
		}
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

	cohort, err := NormalizeCohortTag(entry.CohortTag)
	if err != nil {
		return nil, err
	}
	entry.CohortTag = cohort

	hookType, err := NormalizeHookType(entry.HookType)
	if err != nil {
		return nil, err
	}
	entry.HookType = hookType

	triggerSource, err := NormalizeTriggerSource(entry.TriggerSource)
	if err != nil {
		return nil, err
	}
	entry.TriggerSource = triggerSource

	entry.H1PassCount = countH1Flags(entry.H1TawaFacecam, entry.H1VisualKejadian, entry.H1ArcUtuh, entry.H1HookMutePass)

	entry.ProjectID = strings.TrimSpace(entry.ProjectID)
	entry.Title = strings.TrimSpace(entry.Title)
	entry.PostURL = strings.TrimSpace(entry.PostURL)
	entry.Format = strings.TrimSpace(entry.Format)
	entry.ChannelState = strings.TrimSpace(entry.ChannelState)
	entry.PostedDayTime = strings.TrimSpace(entry.PostedDayTime)
	entry.ThumbnailTs = strings.TrimSpace(entry.ThumbnailTs)
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

// SummarizeViewsLedger computes totals, median views, adaptive Ramai threshold/status,
// and platform/sampleSplit/cohort/trigger/hookType breakdowns plus H1 4/4 pass count,
// along with an explicit warning never to conflate IN-SAMPLE observations with HELD-OUT proof.
func SummarizeViewsLedger(entries []*model.ViewsLedgerEntry) model.ViewsLedgerSummary {
	summary := model.ViewsLedgerSummary{
		ByPlatform:    make(map[string]model.ViewsBucketStats),
		BySplit:       make(map[string]model.ViewsBucketStats),
		ByCohort:      make(map[string]model.ViewsBucketStats),
		ByTrigger:     make(map[string]model.ViewsBucketStats),
		ByHookType:    make(map[string]model.ViewsBucketStats),
		DataScopeNote: viewsLedgerScopeNote,
	}

	type bucketAcc struct {
		stats model.ViewsBucketStats
		views []int64
	}

	allViews := make([]int64, 0, len(entries))
	platAcc := make(map[string]*bucketAcc)
	splitAcc := make(map[string]*bucketAcc)
	cohortAcc := make(map[string]*bucketAcc)
	triggerAcc := make(map[string]*bucketAcc)
	hookAcc := make(map[string]*bucketAcc)

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

		cohort, err := NormalizeCohortTag(e.CohortTag)
		if err != nil {
			cohort = strings.TrimSpace(e.CohortTag)
		}
		addToBucket(cohortAcc, cohort, e)

		if trig := strings.ToLower(strings.TrimSpace(e.TriggerSource)); trig != "" {
			addToBucket(triggerAcc, trig, e)
		}
		if hook := strings.ToLower(strings.TrimSpace(e.HookType)); hook != "" {
			addToBucket(hookAcc, hook, e)
		}

		if e.H1TawaFacecam && e.H1VisualKejadian && e.H1ArcUtuh && e.H1HookMutePass {
			summary.H1FullPassCount++
		}
	}

	summary.MedianViews = computeMedianInt64(allViews)
	switch {
	case summary.TotalEntries < 3:
		summary.RamaiThreshold = 0
		summary.RamaiStatus = "PLACEHOLDER_DUGAAN_N_LT_3"
	case summary.TotalEntries < 10:
		summary.RamaiThreshold = 2 * summary.MedianViews
		summary.RamaiStatus = "BASELINE_AWAL_DUGAAN_N_LT_10"
	default:
		summary.RamaiThreshold = 2 * summary.MedianViews
		summary.RamaiStatus = "AKTIF_2X_MEDIAN_N_GE_10"
	}

	finalizeBuckets := func(src map[string]*bucketAcc, dst map[string]model.ViewsBucketStats) {
		for k, acc := range src {
			acc.stats.MedianViews = computeMedianInt64(acc.views)
			dst[k] = acc.stats
		}
	}
	finalizeBuckets(platAcc, summary.ByPlatform)
	finalizeBuckets(splitAcc, summary.BySplit)
	finalizeBuckets(cohortAcc, summary.ByCohort)
	finalizeBuckets(triggerAcc, summary.ByTrigger)
	finalizeBuckets(hookAcc, summary.ByHookType)

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
