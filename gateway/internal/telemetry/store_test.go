package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRecoveryRetentionAndStableCursor(t *testing.T) {
	cfg := Config{Enabled: true, Dir: t.TempDir(), RetentionDays: 7, MaxEvents: 3}
	s := New(cfg)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		s.Record(Event{StartedAt: now.Add(time.Duration(i) * time.Millisecond), Model: "glm-5.2", Status: 200})
	}
	first, err := s.List(Filter{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("page: %+v %v", first, err)
	}
	s.Record(Event{StartedAt: now.Add(time.Second), Status: 400})
	next, err := s.List(Filter{Limit: 10, Cursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 {
		t.Fatalf("stable page: %+v %v", next, err)
	}
	f, err := os.OpenFile(filepath.Join(cfg.Dir, now.Format("2006-01-02")+".jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{broken")
	f.Close()
	recovered := New(cfg)
	page, _ := recovered.List(Filter{})
	if len(page.Items) != 3 {
		t.Fatalf("recovery count %d", len(page.Items))
	}
	recovered.Record(Event{StartedAt: now.Add(-8 * 24 * time.Hour), Status: 200})
	page, _ = recovered.List(Filter{})
	if len(page.Items) != 3 {
		t.Fatalf("expired event changed retained events")
	}
	recovered.Record(Event{StartedAt: now.Add(2 * time.Second), Status: 200})
	again := New(cfg)
	page, _ = again.List(Filter{})
	if len(page.Items) != 3 {
		t.Fatal("malformed tail prevented future recovery")
	}
}

func TestConcurrentRecordingFiltersAggregationAndSecrets(t *testing.T) {
	s := New(Config{Enabled: true, Dir: t.TempDir(), MaxEvents: 1000, RetentionDays: 7})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := 12.0
			s.Record(Event{StartedAt: time.Now(), Model: "glm-5.2", AccountUID: "uid1", Status: 200, TTFBMS: &v, LatencyMS: &v, PromptTokens: &v, UsageKnown: true})
		}()
	}
	wg.Wait()
	s.Record(Event{StartedAt: time.Now(), Model: "sk-secret-body", ErrorType: "secret error", Status: 500, Attempts: []Attempt{{ErrorType: "secret upstream"}}})
	p, err := s.List(Filter{Status: "success", AccountUID: "uid1", Limit: 200})
	if err != nil || p.Total != 100 {
		t.Fatalf("filter: %+v %v", p, err)
	}
	b, _ := json.Marshal(s.Summary(time.Now().Add(-time.Hour), time.Now(), time.Minute, Filter{}))
	if !strings.Contains(string(b), `"total":101`) || !strings.Contains(string(b), `"p95_ttfb_ms":12`) {
		t.Fatalf("summary %s", b)
	}
	files, _ := filepath.Glob(filepath.Join(s.cfg.Dir, "*.jsonl"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "secret") {
			t.Fatal("secret persisted")
		}
	}
	if _, err := s.List(Filter{Cursor: "bad"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestDisabledAndWriteFailures(t *testing.T) {
	s := New(Config{})
	s.Record(Event{Status: 200})
	p, _ := s.List(Filter{})
	if p.Enabled || len(p.Items) != 0 {
		t.Fatal("disabled store recorded")
	}
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path, []byte("x"), 0600)
	s = New(Config{Enabled: true, Dir: path})
	s.Record(Event{StartedAt: time.Now(), Status: 200})
	if s.Storage().WriteErrors == 0 {
		t.Fatal("write failure not visible")
	}
}

func TestTruncationMetadataPersistenceAndValidation(t *testing.T) {
	negative, budget, reasoning := int64(-1), int64(128000), int64(30000)
	e := Event{
		RequestedMaxOutputTokens: &budget,
		ForwardedMaxOutputTokens: &budget,
		RequestedReasoningEffort: "high",
		ForwardedReasoningEffort: "medium",
		FinishReason:             "length",
		ReasoningTokens:          &reasoning,
		ErrorType:                "output_truncated",
	}
	sanitize(&e)
	if e.ErrorType != "output_truncated" || e.FinishReason != "length" || e.RequestedReasoningEffort != "high" || e.ForwardedReasoningEffort != "medium" {
		t.Fatalf("valid metadata changed: %+v", e)
	}
	e.RequestedMaxOutputTokens = &negative
	e.ForwardedMaxOutputTokens = new(int64)
	e.ReasoningTokens = new(int64)
	e.RequestedReasoningEffort = "unbounded-private-value"
	e.ForwardedReasoningEffort = "ultra"
	e.FinishReason = "private terminal text"
	sanitize(&e)
	if e.RequestedMaxOutputTokens != nil || e.ForwardedMaxOutputTokens != nil || e.ReasoningTokens != nil || e.RequestedReasoningEffort != "" || e.ForwardedReasoningEffort != "" || e.FinishReason != "" {
		t.Fatalf("invalid metadata retained: %+v", e)
	}

	s := New(Config{Enabled: true, Dir: t.TempDir()})
	s.Record(Event{StartedAt: time.Now(), Status: 200, ErrorType: "output_truncated",
		RequestedMaxOutputTokens: &budget, ForwardedMaxOutputTokens: &budget,
		RequestedReasoningEffort: "high", ForwardedReasoningEffort: "high",
		FinishReason: "length", ReasoningTokens: &reasoning})
	reloaded := New(s.cfg)
	page, err := reloaded.List(Filter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	got := page.Items[0]
	if got.ErrorType != "output_truncated" || got.ReasoningTokens == nil || *got.ReasoningTokens != reasoning || got.RequestedMaxOutputTokens == nil || *got.RequestedMaxOutputTokens != budget {
		t.Fatalf("metadata did not survive persistence: %+v", got)
	}
	success, _ := reloaded.List(Filter{Status: "success"})
	failed, _ := reloaded.List(Filter{Status: "error"})
	if success.Total != 0 || failed.Total != 1 || failed.Items[0].Status != 200 {
		t.Fatalf("truncation filtering success=%+v error=%+v", success, failed)
	}
}

func TestOutputTooLongPersistenceAndFilter(t *testing.T) {
	cfg := Config{Enabled: true, Dir: t.TempDir(), RetentionDays: 7}
	s := New(cfg)
	s.Record(Event{
		StartedAt: time.Now().UTC(),
		Status:    400,
		ErrorType: "output_too_long",
		Attempts:  []Attempt{{AccountUID: "uid1", Status: 404, ErrorType: "output_too_long"}},
	})

	reloaded := New(cfg)
	page, err := reloaded.List(Filter{ErrorType: "output_too_long"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("filter page=%+v err=%v", page, err)
	}
	got := page.Items[0]
	if got.ErrorType != "output_too_long" || len(got.Attempts) != 1 || got.Attempts[0].ErrorType != "output_too_long" {
		t.Fatalf("output_too_long did not survive persistence: %+v", got)
	}
}
