package telemetry

import (
	"math"
	"sort"
	"time"
)

type aggregate struct {
	Requests         int     `json:"requests"`
	Success          int     `json:"success"`
	Failed           int     `json:"failed"`
	PromptTokens     float64 `json:"prompt_tokens"`
	CompletionTokens float64 `json:"completion_tokens"`
	Credit           float64 `json:"credit"`
}
type series struct {
	Time time.Time `json:"time"`
	aggregate
}
type model struct {
	Model string `json:"model"`
	aggregate
	P95TTFB       *float64 `json:"p95_ttfb_ms"`
	P95Latency    *float64 `json:"p95_latency_ms"`
	ttfb, latency []float64
}
type account struct {
	AccountUID string    `json:"account_uid"`
	Requests   int       `json:"requests"`
	Success    int       `json:"success"`
	Failed     int       `json:"failed"`
	LastSeen   time.Time `json:"last_seen"`
}
type errorCount struct {
	ErrorType string `json:"error_type"`
	Count     int    `json:"count"`
}
type Summary struct {
	Enabled          bool         `json:"enabled"`
	From             time.Time    `json:"from"`
	To               time.Time    `json:"to"`
	Total            int          `json:"total"`
	Success          int          `json:"success"`
	Failed           int          `json:"failed"`
	SuccessRate      *float64     `json:"success_rate"`
	P95TTFB          *float64     `json:"p95_ttfb_ms"`
	P95Latency       *float64     `json:"p95_latency_ms"`
	PromptTokens     float64      `json:"prompt_tokens"`
	CompletionTokens float64      `json:"completion_tokens"`
	CacheHitTokens   float64      `json:"cache_hit_tokens"`
	CacheMissTokens  float64      `json:"cache_miss_tokens"`
	Credit           float64      `json:"credit"`
	Series           []series     `json:"series"`
	Models           []*model     `json:"models"`
	Accounts         []*account   `json:"accounts"`
	Errors           []errorCount `json:"errors"`
	Storage          Storage      `json:"storage"`
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
func percentile(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	sort.Float64s(v)
	p := v[int(math.Ceil(float64(len(v))*.95))-1]
	return &p
}
func (a *aggregate) add(e Event) {
	a.Requests++
	if success(e) {
		a.Success++
	} else {
		a.Failed++
	}
	a.PromptTokens += val(e.PromptTokens)
	a.CompletionTokens += val(e.CompletionTokens)
	a.Credit += val(e.Credit)
}
func (s *Store) Summary(from, to time.Time, bucket time.Duration, f Filter) Summary {
	out := Summary{Enabled: s.Enabled(), From: from, To: to, Series: []series{}, Models: []*model{}, Accounts: []*account{}, Errors: []errorCount{}, Storage: s.Storage()}
	if s == nil {
		return out
	}
	if bucket <= 0 {
		bucket = time.Hour
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f.From = from
	f.To = to
	buckets := map[int64]*series{}
	models := map[string]*model{}
	accounts := map[string]*account{}
	errs := map[string]int{}
	var total aggregate
	var ttfb, latency []float64
	for at := from.Truncate(bucket); !at.After(to); at = at.Add(bucket) {
		buckets[at.UnixNano()] = &series{Time: at}
	}
	for _, entry := range s.events {
		e := entry.Event
		if !f.matches(e) || e.StartedAt.Before(time.Now().Add(-time.Duration(s.cfg.RetentionDays)*24*time.Hour)) {
			continue
		}
		total.add(e)
		out.CacheHitTokens += val(e.CacheHitTokens)
		out.CacheMissTokens += val(e.CacheMissTokens)
		b := buckets[e.StartedAt.Truncate(bucket).UnixNano()]
		if b != nil {
			b.add(e)
		}
		m := models[e.Model]
		if m == nil {
			m = &model{Model: e.Model}
			models[e.Model] = m
		}
		m.add(e)
		if e.TTFBMS != nil {
			ttfb = append(ttfb, *e.TTFBMS)
			m.ttfb = append(m.ttfb, *e.TTFBMS)
		}
		if e.LatencyMS != nil {
			latency = append(latency, *e.LatencyMS)
			m.latency = append(m.latency, *e.LatencyMS)
		}
		if e.AccountUID != "" {
			a := accounts[e.AccountUID]
			if a == nil {
				a = &account{AccountUID: e.AccountUID}
				accounts[e.AccountUID] = a
			}
			a.Requests++
			if success(e) {
				a.Success++
			} else {
				a.Failed++
			}
			if e.StartedAt.After(a.LastSeen) {
				a.LastSeen = e.StartedAt
			}
		}
		if !success(e) {
			kind := e.ErrorType
			if kind == "" {
				kind = "unknown"
			}
			errs[kind]++
		}
	}
	out.Total = total.Requests
	out.Success = total.Success
	out.Failed = total.Failed
	out.PromptTokens = total.PromptTokens
	out.CompletionTokens = total.CompletionTokens
	out.Credit = total.Credit
	if out.Total > 0 {
		v := float64(out.Success) / float64(out.Total)
		out.SuccessRate = &v
	}
	out.P95TTFB = percentile(ttfb)
	out.P95Latency = percentile(latency)
	for _, b := range buckets {
		out.Series = append(out.Series, *b)
	}
	sort.Slice(out.Series, func(i, j int) bool { return out.Series[i].Time.Before(out.Series[j].Time) })
	for _, m := range models {
		m.P95TTFB = percentile(m.ttfb)
		m.P95Latency = percentile(m.latency)
		out.Models = append(out.Models, m)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Model < out.Models[j].Model })
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, a)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].AccountUID < out.Accounts[j].AccountUID })
	for k, v := range errs {
		out.Errors = append(out.Errors, errorCount{k, v})
	}
	sort.Slice(out.Errors, func(i, j int) bool { return out.Errors[i].ErrorType < out.Errors[j].ErrorType })
	return out
}
