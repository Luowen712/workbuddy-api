package telemetry

import (
	"sort"
	"time"
)

// ModelStat is the GUI's legacy statistics contract, computed from retained events.
type ModelStat struct {
	Model                     string     `json:"model"`
	Requests                  int        `json:"requests"`
	Success                   int        `json:"success"`
	Failed                    int        `json:"failed"`
	Streaming                 int        `json:"streaming"`
	AvgTTFBMS                 float64    `json:"avg_ttfb_ms"`
	AvgLatencyMS              float64    `json:"avg_latency_ms"`
	TokensPerSec              float64    `json:"tokens_per_sec"`
	PromptTokens              float64    `json:"prompt_tokens"`
	CompletionTokens          float64    `json:"completion_tokens"`
	TotalTokens               float64    `json:"total_tokens"`
	CacheHitTokens            float64    `json:"cache_hit_tokens"`
	CacheMissTokens           float64    `json:"cache_miss_tokens"`
	CacheWriteTokens          float64    `json:"cache_write_tokens"`
	CacheHitRate              float64    `json:"cache_hit_rate"`
	Credit                    float64    `json:"credit"`
	CreditPerReq              float64    `json:"credit_per_req"`
	LastSeen                  *time.Time `json:"last_seen"`
	ttfbCount, latencyCount   int
	generationMS, timedTokens float64
}

type Stats struct {
	Enabled        bool         `json:"enabled"`
	ResetSupported bool         `json:"reset_supported"`
	Message        string       `json:"message"`
	Since          time.Time    `json:"since"`
	Now            time.Time    `json:"now"`
	UptimeSec      int64        `json:"uptime_sec"`
	Total          ModelStat    `json:"total"`
	Models         []*ModelStat `json:"models"`
}

func (m *ModelStat) add(e Event) {
	m.Requests++
	if success(e) {
		m.Success++
	} else {
		m.Failed++
	}
	if e.Stream {
		m.Streaming++
	}
	m.PromptTokens += val(e.PromptTokens)
	m.CompletionTokens += val(e.CompletionTokens)
	m.TotalTokens = m.PromptTokens + m.CompletionTokens
	m.CacheHitTokens += val(e.CacheHitTokens)
	if e.CacheMissTokens != nil {
		m.CacheMissTokens += *e.CacheMissTokens
	} else if e.PromptTokens != nil {
		m.CacheMissTokens += max(0, *e.PromptTokens-val(e.CacheHitTokens))
	}
	m.Credit += val(e.Credit)
	if e.TTFBMS != nil {
		m.AvgTTFBMS += *e.TTFBMS
		m.ttfbCount++
	}
	if e.LatencyMS != nil {
		m.AvgLatencyMS += *e.LatencyMS
		m.latencyCount++
	}
	if e.LatencyMS != nil && e.TTFBMS != nil && *e.LatencyMS > *e.TTFBMS && e.CompletionTokens != nil {
		m.generationMS += *e.LatencyMS - *e.TTFBMS
		m.timedTokens += *e.CompletionTokens
	}
	if m.LastSeen == nil || e.StartedAt.After(*m.LastSeen) {
		at := e.StartedAt
		m.LastSeen = &at
	}
}

func (m *ModelStat) finish() {
	if m.ttfbCount > 0 {
		m.AvgTTFBMS /= float64(m.ttfbCount)
	}
	if m.latencyCount > 0 {
		m.AvgLatencyMS /= float64(m.latencyCount)
	}
	if m.generationMS > 0 {
		m.TokensPerSec = m.timedTokens * 1000 / m.generationMS
	}
	if m.Requests > 0 {
		m.CreditPerReq = m.Credit / float64(m.Requests)
	}
	if n := m.CacheHitTokens + m.CacheMissTokens; n > 0 {
		m.CacheHitRate = m.CacheHitTokens / n
	}
}

func (s *Store) Stats() Stats {
	now := time.Now().UTC()
	out := Stats{Enabled: s.Enabled(), Since: now, Now: now, Models: []*ModelStat{},
		Message: "Statistics cover retained telemetry only; request history is not resettable."}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour)
	models := map[string]*ModelStat{}
	for _, item := range s.events {
		e := item.Event
		if e.StartedAt.Before(cutoff) {
			continue
		}
		if e.StartedAt.Before(out.Since) {
			out.Since = e.StartedAt
		}
		out.Total.add(e)
		m := models[e.Model]
		if m == nil {
			m = &ModelStat{Model: e.Model}
			models[e.Model] = m
		}
		m.add(e)
	}
	out.Total.finish()
	for _, m := range models {
		m.finish()
		out.Models = append(out.Models, m)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Model < out.Models[j].Model })
	out.UptimeSec = int64(now.Sub(out.Since).Seconds())
	return out
}
