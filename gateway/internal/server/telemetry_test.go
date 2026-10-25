package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/telemetry"
)

func TestTelemetryStatsCompatibility(t *testing.T) {
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	prompt, completion, latency, first, hit := 10.0, 5.0, 100.0, 20.0, 4.0
	store.Record(telemetry.Event{Model: "glm-5.2", Status: 200, Stream: true,
		PromptTokens: &prompt, CompletionTokens: &completion, LatencyMS: &latency,
		TTFBMS: &first, CacheHitTokens: &hit})
	h := NewHandler(Config{Telemetry: store, APIKey: "test"})
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("GET", "/v1/stats", nil))
	if unauth.Code != 401 {
		t.Fatalf("unauth status %d", unauth.Code)
	}
	req := httptest.NewRequest("GET", "/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out struct {
		Total struct {
			Requests int     `json:"requests"`
			Tokens   int     `json:"total_tokens"`
			Avg      float64 `json:"avg_latency_ms"`
			Miss     int     `json:"cache_miss_tokens"`
		} `json:"total"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Total.Requests != 1 || out.Total.Tokens != 15 || out.Total.Avg != 100 || out.Total.Miss != 6 {
		t.Fatalf("stats response %d %s", w.Code, w.Body.String())
	}
}

func TestTelemetryAuthValidationAndQueries(t *testing.T) {
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	h := NewHandler(Config{Pool: testPoolWith(), APIKey: "test-key", Telemetry: store, MaxBodyBytes: 4})
	for _, path := range []string{"/v1/telemetry/summary", "/v1/telemetry/requests", "/v1/telemetry/requests/missing"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("unauthorized %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("secret-body"))
	r.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
	p, _ := store.List(telemetry.Filter{})
	if len(p.Items) != 1 || p.Items[0].ErrorType != "request_body_too_large" || p.Items[0].Status != 413 {
		t.Fatalf("event %+v", p)
	}
	for _, path := range []string{"/v1/telemetry/requests?limit=201", "/v1/telemetry/requests?stream=no", "/v1/telemetry/requests?cursor=bad", "/v1/telemetry/summary?range=bad", "/v1/telemetry/requests?from=bad"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("bad query %s: %d", path, w.Code)
		}
	}
}

func TestTelemetryChatLifecycle(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
			p := testPoolWith(&auth.Auth{UID: "uid", AccessToken: "secret-key", ExpiresAt: time.Now().Add(time.Hour).Unix()})
			defer p.Close()
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				return 200, strings.Replace(sseOK, `"total_tokens":2`, `"total_tokens":2,"credit":0,"prompt_cache_hit_tokens":1`, 1), true
			})
			h := NewHandler(Config{Pool: p, Upstream: up, Telemetry: store})
			body := `{"model":"glm-5.2","messages":[{"role":"user","content":"secret message"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			page, _ := store.List(telemetry.Filter{})
			if len(page.Items) != 1 {
				t.Fatal("missing event")
			}
			e := page.Items[0]
			if e.Status != 200 || e.ErrorType != "" || !e.UsageKnown || !e.CreditKnown || e.TTFBMS == nil || e.PromptTokens == nil || *e.PromptTokens != 1 || len(e.Attempts) != 1 {
				t.Fatalf("lifecycle %+v", e)
			}
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestTelemetryCancellation(t *testing.T) {
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	p := testPoolWith()
	defer p.Close()
	h := NewHandler(Config{Pool: p, Telemetry: store})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"sk-secret"}`)).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), r)
	page, _ := store.List(telemetry.Filter{})
	if len(page.Items) != 1 || page.Items[0].ErrorType != "cancelled" {
		t.Fatalf("cancel: %+v", page)
	}
}

func TestTelemetryCompletedResponseIsNotCancelled(t *testing.T) {
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	h := &Handler{cfg: Config{Telemetry: store}}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	h.observeChat(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		cancel()
	})(w, r)

	page, _ := store.List(telemetry.Filter{})
	if len(page.Items) != 1 || page.Items[0].Status != http.StatusOK || page.Items[0].ErrorType != "" {
		t.Fatalf("completed response misclassified: %+v", page)
	}
}

func TestPositiveJSONInt64AcceptsExactJSONIntegerForms(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{raw: "1e3", want: 1000},
		{raw: "128000.0", want: 128000},
		{raw: "1.28000e5", want: 128000},
		{raw: "9223372036854775807.0", want: 9223372036854775807},
	} {
		got := positiveJSONInt64(json.RawMessage(tc.raw))
		if got == nil || *got != tc.want {
			t.Errorf("positiveJSONInt64(%s)=%v want %d", tc.raw, got, tc.want)
		}
	}
}

func TestPositiveJSONInt64RejectsNonPositiveFractionalAndOutOfRange(t *testing.T) {
	for _, raw := range []string{"0", "-1", "1.5", "9223372036854775808", "1e100", "null", `"1000"`} {
		if got := positiveJSONInt64(json.RawMessage(raw)); got != nil {
			t.Errorf("positiveJSONInt64(%s)=%d want nil", raw, *got)
		}
	}
}

func TestTelemetryStreamErrorFrameIsFailure(t *testing.T) {
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	p := testPoolWith(&auth.Auth{UID: "uid", AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	defer p.Close()
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return http.StatusOK, "data: {\"error\":{\"message\":\"upstream failed\"}}\n\ndata: [DONE]\n\n", true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, Telemetry: store})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[],"stream":true}`)))

	page, _ := store.List(telemetry.Filter{})
	if len(page.Items) != 1 || page.Items[0].Status != http.StatusOK || page.Items[0].ErrorType != "stream_error" {
		t.Fatalf("stream error not recorded: %+v", page)
	}
}

func TestTelemetryChatTruncationModes(t *testing.T) {
	const truncated = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial output\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":12056,\"completion_tokens\":32000,\"total_tokens\":44056,\"completion_tokens_details\":{\"reasoning_tokens\":30000}}}\n\n" +
		"data: [DONE]\n\n"
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "stream"}[stream], func(t *testing.T) {
			calls := 0
			store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
			p := testPoolWith(&auth.Auth{UID: "uid", AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
			defer p.Close()
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				calls++
				return http.StatusOK, truncated, true
			})
			h := NewHandler(Config{Pool: p, Upstream: up, Telemetry: store})
			body := `{"model":"glm-5.2","messages":[{"role":"user","content":"x"}],"max_completion_tokens":128000,"reasoning_effort":"low","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "partial output") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if calls != 1 {
				t.Fatalf("upstream calls=%d want 1", calls)
			}
			page, err := store.List(telemetry.Filter{})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("events=%v err=%v", page.Items, err)
			}
			e := page.Items[0]
			if e.Status != http.StatusOK || e.ErrorType != "output_truncated" || e.FinishReason != "length" ||
				e.RequestedMaxOutputTokens == nil || *e.RequestedMaxOutputTokens != 128000 ||
				e.ForwardedMaxOutputTokens == nil || *e.ForwardedMaxOutputTokens != 128000 ||
				e.RequestedReasoningEffort != "low" || e.ForwardedReasoningEffort != "low" ||
				e.ReasoningTokens == nil || *e.ReasoningTokens != 30000 {
				t.Fatalf("truncation metadata missing: %+v", e)
			}
			state, _ := p.Status("uid")
			if state.Cooling || state.Disabled || state.ErrTotal != 0 {
				t.Fatalf("normal truncation penalized account: %+v", state)
			}
		})
	}
}

func TestTelemetryLengthWinsAcrossChoices(t *testing.T) {
	const mixedFinish = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial output\"},\"finish_reason\":\"length\"},{\"index\":1,\"delta\":{\"content\":\"other output\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\ndata: [DONE]\n\n"
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	p := testPoolWith(&auth.Auth{UID: "uid", AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	defer p.Close()
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return http.StatusOK, mixedFinish, true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, Telemetry: store})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"x"}],"stream":true}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "partial output") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	page, err := store.List(telemetry.Filter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("events=%v err=%v", page.Items, err)
	}
	e := page.Items[0]
	if e.ErrorType != "output_truncated" || e.FinishReason != "length" {
		t.Fatalf("length overwritten by another choice: %+v", e)
	}
}

func TestTruncatedToolArgumentsDoNotReplay(t *testing.T) {
	const truncatedTool = "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"dangerous\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return http.StatusOK, truncatedTool, true
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "uid", AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour).Unix()}),
		Upstream: up,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"x"}]}`)))
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body)
	}
	if strings.Contains(w.Body.String(), "dangerous") {
		t.Fatalf("incomplete tool call escaped truncation guard: %s", w.Body)
	}
}
