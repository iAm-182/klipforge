package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"klipforge/internal/model"
)

const maxActivityInMemory = 1000

var actSeq atomic.Uint64

func (s *Store) activityPath() string {
	return filepath.Join(s.DataDir, "activity.jsonl")
}

func (s *Store) loadActivity() {
	f, err := os.Open(s.activityPath())
	if err != nil {
		return
	}
	defer f.Close()

	var loaded []model.ActivityEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 32*1024), 512*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ent model.ActivityEntry
		if json.Unmarshal([]byte(line), &ent) == nil && ent.Summary != "" {
			loaded = append(loaded, ent)
		}
	}
	if len(loaded) > maxActivityInMemory {
		loaded = loaded[len(loaded)-maxActivityInMemory:]
	}
	s.activity = loaded
}

// AppendActivity appends an activity entry to both the in-memory ring and append-only data/activity.jsonl.
func (s *Store) AppendActivity(ent model.ActivityEntry) model.ActivityEntry {
	if ent.Timestamp.IsZero() {
		ent.Timestamp = time.Now()
	}
	if ent.ID == "" {
		ent.ID = fmt.Sprintf("act-%d-%d", time.Now().UnixNano(), actSeq.Add(1))
	}
	if ent.Level == "" {
		ent.Level = "info"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.activity = append(s.activity, ent)
	if len(s.activity) > maxActivityInMemory {
		s.activity = s.activity[len(s.activity)-maxActivityInMemory:]
	}

	p := s.activityPath()
	// Compact file atomically if it grows past 4 MB so disk stays clean on VPS without truncation risk
	if fi, err := os.Stat(p); err == nil && fi.Size() > 4*1024*1024 {
		var buf strings.Builder
		for _, item := range s.activity {
			if b, err := json.Marshal(item); err == nil {
				buf.Write(b)
				buf.WriteByte('\n')
			}
		}
		_ = writeBytesAtomic(p, []byte(buf.String()), 0o644)
	} else if b, err := json.Marshal(ent); err == nil {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	return ent
}

// ListActivity returns the newest activity entries (most recent first), optionally filtered by member ID or name.
func (s *Store) ListActivity(limit int, memberFilter string) []model.ActivityEntry {
	if limit <= 0 {
		limit = 50
	}
	if limit > 300 {
		limit = 300
	}
	mf := strings.TrimSpace(strings.ToLower(memberFilter))

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.ActivityEntry, 0, limit)
	for i := len(s.activity) - 1; i >= 0 && len(out) < limit; i-- {
		ent := s.activity[i]
		if mf != "" && mf != "all" {
			if strings.ToLower(ent.ActorID) != mf && strings.ToLower(ent.ActorName) != mf {
				continue
			}
		}
		out = append(out, ent)
	}
	return out
}
