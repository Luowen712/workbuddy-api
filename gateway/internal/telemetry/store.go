// Package telemetry stores bounded, typed operational events, never request bodies.
package telemetry

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxBytes = 128 << 20
const maxLine = 16 << 10

type Config struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retention_days"`
	MaxEvents     int    `json:"max_events"`
	Dir           string `json:"dir"`
}

func DefaultConfig() Config { return Config{true, 7, 100000, "./data/telemetry"} }

type Attempt struct {
	AccountUID string `json:"account_uid"`
	Status     int    `json:"status"`
	ErrorType  string `json:"error_type"`
}
type Event struct {
	RequestID                string    `json:"request_id"`
	StartedAt                time.Time `json:"started_at"`
	Model                    string    `json:"model"`
	AccountUID               string    `json:"account_uid"`
	Stream                   bool      `json:"stream"`
	Status                   int       `json:"status"`
	ErrorType                string    `json:"error_type"`
	RetryCount               int       `json:"retry_count"`
	TTFBMS                   *float64  `json:"ttfb_ms"`
	LatencyMS                *float64  `json:"latency_ms"`
	PromptTokens             *float64  `json:"prompt_tokens"`
	CompletionTokens         *float64  `json:"completion_tokens"`
	CacheHitTokens           *float64  `json:"cache_hit_tokens"`
	CacheMissTokens          *float64  `json:"cache_miss_tokens"`
	Credit                   *float64  `json:"credit"`
	UsageKnown               bool      `json:"usage_known"`
	CreditKnown              bool      `json:"credit_known"`
	RequestedMaxOutputTokens *int64    `json:"requested_max_output_tokens,omitempty"`
	ForwardedMaxOutputTokens *int64    `json:"forwarded_max_output_tokens,omitempty"`
	RequestedReasoningEffort string    `json:"requested_reasoning_effort,omitempty"`
	ForwardedReasoningEffort string    `json:"forwarded_reasoning_effort,omitempty"`
	FinishReason             string    `json:"finish_reason,omitempty"`
	ReasoningTokens          *int64    `json:"reasoning_tokens,omitempty"`
	Attempts                 []Attempt `json:"attempts"`
}
type stored struct {
	Event
	Seq uint64 `json:"sequence"`
}
type Storage struct {
	RetentionDays int    `json:"retention_days"`
	MaxEvents     int    `json:"max_events"`
	Events        int    `json:"events"`
	Dropped       uint64 `json:"dropped"`
	WriteErrors   uint64 `json:"write_errors"`
	MaxBytes      int    `json:"max_bytes"`
}
type Store struct {
	mu                   sync.Mutex
	cfg                  Config
	events               []stored
	seq                  uint64
	bytes                int64
	dropped, writeErrors uint64
}

func New(cfg Config) *Store {
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = 7
	}
	if cfg.RetentionDays > 365 {
		cfg.RetentionDays = 365
	}
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = 100000
	}
	if cfg.MaxEvents > 100000 {
		cfg.MaxEvents = 100000
	}
	if cfg.Dir == "" {
		cfg.Dir = "./data/telemetry"
	}
	s := &Store{cfg: cfg}
	if !cfg.Enabled {
		return s
	}
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		s.writeErrors++
		return s
	}
	files, _ := filepath.Glob(filepath.Join(cfg.Dir, "????-??-??.jsonl"))
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			s.writeErrors++
			continue
		}
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 4096), maxLine)
		for scan.Scan() {
			var e stored
			if json.Unmarshal(scan.Bytes(), &e) != nil || e.RequestID == "" {
				s.dropped++
				continue
			}
			sanitize(&e.Event)
			s.events = append(s.events, e)
			if e.Seq > s.seq {
				s.seq = e.Seq
			}
			s.prune(time.Now())
		}
		if scan.Err() != nil {
			s.dropped++
		}
		f.Close()
	}
	sort.Slice(s.events, func(i, j int) bool { return s.events[i].Seq < s.events[j].Seq })
	s.prune(time.Now())
	s.rewrite()
	return s
}
func ErrorType(v string) string {
	switch v {
	case "", "none":
		return ""
	case "hard_credit", "soft_rate", "session_dead", "not_found", "server", "content_blocked", "bad_params", "account_fault", "model_blocked", "waf_block", "prompt_too_long", "client", "invalid_request", "request_body_too_large", "no_healthy_account", "transport", "upstream_parse", "stream_error", "output_truncated", "cancelled", "refresh_failed", "busy", "invalid_api_key", "output_too_long":
		return v
	default:
		return "unknown"
	}
}

func ReasoningEffort(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "off", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return v
	default:
		return ""
	}
}

func FinishReason(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "stop", "length", "tool_calls", "content_filter", "function_call":
		return v
	default:
		return ""
	}
}

func positiveInt64(v *int64) *int64 {
	if v == nil || *v <= 0 {
		return nil
	}
	n := *v
	return &n
}

// SafeModel requires a catalog match at the integration boundary; unknown input is hashed.
func SafeModel(v string, known bool) string {
	if v == "" {
		return "unknown"
	}
	if known && len(v) <= 160 {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	return "unknown:" + hex.EncodeToString(sum[:8])
}
func sanitize(e *Event) {
	if e.Attempts == nil {
		e.Attempts = []Attempt{}
	}
	if len(e.Attempts) > 32 {
		e.Attempts = e.Attempts[:32]
	}
	e.ErrorType = ErrorType(e.ErrorType)
	e.RequestedMaxOutputTokens = positiveInt64(e.RequestedMaxOutputTokens)
	e.ForwardedMaxOutputTokens = positiveInt64(e.ForwardedMaxOutputTokens)
	e.RequestedReasoningEffort = ReasoningEffort(e.RequestedReasoningEffort)
	e.ForwardedReasoningEffort = ReasoningEffort(e.ForwardedReasoningEffort)
	e.FinishReason = FinishReason(e.FinishReason)
	e.ReasoningTokens = positiveInt64(e.ReasoningTokens)
	if len(e.AccountUID) > 128 {
		e.AccountUID = "unknown"
	}
	// Defense in depth for callers that omit catalog validation.
	if len(e.Model) > 160 || strings.ContainsAny(e.Model, " \r\n\t\"{}") || strings.HasPrefix(e.Model, "sk-") {
		e.Model = SafeModel(e.Model, false)
	}
	for i := range e.Attempts {
		e.Attempts[i].ErrorType = ErrorType(e.Attempts[i].ErrorType)
		if len(e.Attempts[i].AccountUID) > 128 {
			e.Attempts[i].AccountUID = "unknown"
		}
	}
}
func (s *Store) Record(e Event) {
	if s == nil || !s.cfg.Enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now().UTC()
	}
	if e.StartedAt.Before(time.Now().Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour)) {
		s.dropped++
		return
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		s.dropped++
		return
	}
	e.RequestID = hex.EncodeToString(id[:])
	sanitize(&e)
	// JSON roundtrip detaches caller-owned slices and measurement pointers.
	b, err := json.Marshal(e)
	if err != nil || len(b) > maxLine-128 {
		s.dropped++
		return
	}
	json.Unmarshal(b, &e)
	s.seq++
	entry := stored{e, s.seq}
	s.events = append(s.events, entry)
	changed := s.prune(time.Now())
	if changed || s.bytes+int64(len(b)+128) > MaxBytes {
		if s.bytes+int64(len(b)+128) > MaxBytes && len(s.events) > 1 {
			n := len(s.events) / 10
			if n < 1 {
				n = 1
			}
			s.dropped += uint64(n)
			s.events = s.events[n:]
		}
		s.rewrite()
		return
	}
	b, _ = json.Marshal(entry)
	b = append(b, '\n')
	path := filepath.Join(s.cfg.Dir, e.StartedAt.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		s.writeErrors++
		return
	}
	n, err := f.Write(b)
	closeErr := f.Close()
	s.bytes += int64(n)
	if err != nil || closeErr != nil {
		s.writeErrors++
	}
}
func (s *Store) prune(now time.Time) bool {
	cutoff := now.Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour)
	keep := s.events[:0]
	before := len(s.events)
	for _, e := range s.events {
		if !e.StartedAt.Before(cutoff) {
			keep = append(keep, e)
		}
	}
	s.events = keep
	if len(s.events) > s.cfg.MaxEvents {
		n := len(s.events) - s.cfg.MaxEvents
		if s.cfg.MaxEvents >= 1000 {
			n += s.cfg.MaxEvents / 20
		}
		s.events = s.events[n:]
	}
	s.dropped += uint64(before - len(s.events))
	return before != len(s.events)
}

// Rewrite on recovery and compaction repairs truncated tails. Atomic per-day replacement
// leaves valid files intact on write failures; the next successful compaction retries.
func (s *Store) rewrite() {
	days := map[string][]byte{}
	for _, e := range s.events {
		b, err := json.Marshal(e)
		if err != nil {
			s.dropped++
			continue
		}
		day := e.StartedAt.UTC().Format("2006-01-02")
		days[day] = append(days[day], append(b, '\n')...)
	}
	total := 0
	for _, b := range days {
		total += len(b)
	}
	if total > MaxBytes && len(s.events) > 0 {
		n := len(s.events) / 10
		if n < 1 {
			n = 1
		}
		s.dropped += uint64(n)
		s.events = s.events[n:]
		s.rewrite()
		return
	}
	for day, b := range days {
		p := filepath.Join(s.cfg.Dir, day+".jsonl")
		if err := os.WriteFile(p+".tmp", b, 0600); err != nil {
			s.writeErrors++
			return
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			s.writeErrors++
			return
		}
	}
	files, _ := filepath.Glob(filepath.Join(s.cfg.Dir, "????-??-??.jsonl"))
	for _, p := range files {
		if _, ok := days[strings.TrimSuffix(filepath.Base(p), ".jsonl")]; !ok {
			if err := os.Remove(p); err != nil {
				s.writeErrors++
			}
		}
	}
	s.bytes = int64(total)
}
func (s *Store) Storage() Storage {
	if s == nil {
		return Storage{RetentionDays: 7, MaxEvents: 100000, MaxBytes: MaxBytes}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Storage{s.cfg.RetentionDays, s.cfg.MaxEvents, len(s.events), s.dropped, s.writeErrors, MaxBytes}
}

type Filter struct {
	From, To                                     time.Time
	Model, AccountUID, Status, ErrorType, Cursor string
	Stream                                       *bool
	Limit                                        int
}
type Page struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"next_cursor"`
	Total      int     `json:"total"`
	Enabled    bool    `json:"enabled"`
}
type cursor struct {
	Snapshot, Before uint64
	Filter           string
}

func (f Filter) matches(e Event) bool {
	return (f.From.IsZero() || !e.StartedAt.Before(f.From)) && (f.To.IsZero() || !e.StartedAt.After(f.To)) && (f.Model == "" || f.Model == e.Model) && (f.AccountUID == "" || f.AccountUID == e.AccountUID) && (f.ErrorType == "" || f.ErrorType == e.ErrorType) && (f.Stream == nil || *f.Stream == e.Stream) && (f.Status == "" || (f.Status == "success" && success(e)) || (f.Status == "error" && !success(e)))
}
func success(e Event) bool { return e.Status >= 200 && e.Status < 400 && e.ErrorType == "" }
func (s *Store) List(f Filter) (Page, error) {
	p := Page{Items: []Event{}}
	if s == nil {
		return p, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Enabled = s.cfg.Enabled
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	finger := f
	finger.Cursor = ""
	finger.Limit = 0
	b, _ := json.Marshal(finger)
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:8])
	c := cursor{Snapshot: s.seq, Before: s.seq + 1, Filter: hash}
	if f.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Filter != hash {
			return p, errors.New("invalid cursor")
		}
	}
	cutoff := time.Now().Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour)
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.Seq > c.Snapshot || e.StartedAt.Before(cutoff) || !f.matches(e.Event) {
			continue
		}
		p.Total++
		if e.Seq < c.Before {
			if len(p.Items) < f.Limit {
				p.Items = append(p.Items, e.Event)
			} else if p.NextCursor == "" {
				last := p.Items[len(p.Items)-1]
				var seq uint64
				for _, x := range s.events {
					if x.RequestID == last.RequestID {
						seq = x.Seq
						break
					}
				}
				b, _ := json.Marshal(cursor{c.Snapshot, seq, hash})
				p.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			}
		}
	}
	// Return detached values so callers cannot mutate stored events.
	b, _ = json.Marshal(p)
	json.Unmarshal(b, &p)
	return p, nil
}
func (s *Store) Get(id string) (Event, bool) {
	if s == nil {
		return Event{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.RequestID == id && !e.StartedAt.Before(time.Now().Add(-time.Duration(s.cfg.RetentionDays)*24*time.Hour)) {
			b, _ := json.Marshal(e.Event)
			var out Event
			json.Unmarshal(b, &out)
			return out, true
		}
	}
	return Event{}, false
}
func (s *Store) Enabled() bool  { return s != nil && s.cfg.Enabled }
func (f Filter) String() string { return fmt.Sprintf("%s/%s", f.Model, f.AccountUID) }
