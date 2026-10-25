package server

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"strconv"
	"time"
	"workbuddy2api/internal/telemetry"
	"workbuddy2api/internal/upstream"
)

type telemetryKey struct{}

func eventOf(r *http.Request) *telemetry.Event {
	e, _ := r.Context().Value(telemetryKey{}).(*telemetry.Event)
	return e
}

type observedWriter struct {
	http.ResponseWriter
	status int
}

func (w *observedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *observedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type observedFlusher struct{ *observedWriter }

func (w *observedFlusher) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	w.ResponseWriter.(http.Flusher).Flush()
}
func (h *Handler) observeChat(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.cfg.Telemetry.Enabled() {
			next(w, r)
			return
		}
		start := time.Now()
		e := &telemetry.Event{StartedAt: start.UTC(), Model: "unknown", Attempts: []telemetry.Attempt{}}
		ow := &observedWriter{ResponseWriter: w}
		var out http.ResponseWriter = ow
		if _, ok := w.(http.Flusher); ok {
			out = &observedFlusher{ow}
		}
		defer func() {
			wroteStatus := ow.status
			e.Status = wroteStatus
			if e.Status == 0 {
				e.Status = 500
			}
			v := float64(time.Since(start).Microseconds()) / 1000
			e.LatencyMS = &v
			if r.Context().Err() != nil && (wroteStatus == 0 || e.Status < 200 || e.Status >= 400) {
				e.ErrorType = "cancelled"
			}
			if e.Status >= 400 && e.ErrorType == "" {
				switch e.Status {
				case 401:
					e.ErrorType = "invalid_api_key"
				case 400:
					e.ErrorType = "invalid_request"
				case 413:
					e.ErrorType = "request_body_too_large"
				default:
					e.ErrorType = "no_healthy_account"
				}
			}
			e.RetryCount = len(e.Attempts) - 1
			if e.RetryCount < 0 {
				e.RetryCount = 0
			}
			h.cfg.Telemetry.Record(*e)
		}()
		next(out, r.WithContext(context.WithValue(r.Context(), telemetryKey{}, e)))
	}
}
func (h *Handler) telemetryModel(raw string) string {
	_, bare := resolveModel(raw)
	known := false
	dynamicModelsCache.RLock()
	for _, mi := range dynamicModelsCache.ids {
		if mi.ID == bare {
			known = true
			break
		}
	}
	dynamicModelsCache.RUnlock()
	// The embedded catalog supplies known names without triggering network discovery.
	if !known {
		known = upstream.KnownCatalogModel(bare)
	}
	return telemetry.SafeModel(raw, known)
}
func telemetryAttempt(e *telemetry.Event, uid string, status int, kind string) {
	if e == nil {
		return
	}
	e.AccountUID = uid
	e.ErrorType = telemetry.ErrorType(kind)
	e.Attempts = append(e.Attempts, telemetry.Attempt{AccountUID: uid, Status: status, ErrorType: e.ErrorType})
}
func telemetryUsage(e *telemetry.Event, s *chatStatsReader) {
	if e == nil {
		return
	}
	if s.seen {
		v := float64(s.TTFB().Microseconds()) / 1000
		e.TTFBMS = &v
	}
	e.PromptTokens = s.telemetryPrompt
	e.CompletionTokens = s.telemetryCompletion
	e.CacheHitTokens = s.telemetryCacheHit
	e.CacheMissTokens = s.telemetryCacheMiss
	e.FinishReason = s.finishReason
	e.ReasoningTokens = s.telemetryReasoning
	e.UsageKnown = s.telemetryPrompt != nil || s.telemetryCompletion != nil
	if s.hasCredit {
		v := s.credit
		e.Credit = &v
		e.CreditKnown = true
	}
	if s.telemetryError {
		e.ErrorType = "stream_error"
	}
	if e.ErrorType == "" && s.finishReason == "length" {
		e.ErrorType = "output_truncated"
	}
}

func telemetryRequested(e *telemetry.Event, body []byte, options *responsesOptions) {
	if e == nil {
		return
	}
	if options != nil {
		if options.requestedMaxOutputTokens != nil {
			value := *options.requestedMaxOutputTokens
			e.RequestedMaxOutputTokens = &value
		}
		e.RequestedReasoningEffort = telemetry.ReasoningEffort(options.requestedReasoningEffort)
		return
	}
	var request struct {
		MaxTokens           json.RawMessage `json:"max_tokens"`
		MaxCompletionTokens json.RawMessage `json:"max_completion_tokens"`
		ReasoningEffort     json.RawMessage `json:"reasoning_effort"`
		ReasoningCamel      json.RawMessage `json:"reasoningEffort"`
	}
	if json.Unmarshal(body, &request) != nil {
		return
	}
	if len(request.MaxTokens) > 0 {
		e.RequestedMaxOutputTokens = positiveJSONInt64(request.MaxTokens)
	} else {
		e.RequestedMaxOutputTokens = positiveJSONInt64(request.MaxCompletionTokens)
	}
	if len(request.ReasoningEffort) > 0 {
		e.RequestedReasoningEffort = reasoningEffortJSON(request.ReasoningEffort)
	} else {
		e.RequestedReasoningEffort = reasoningEffortJSON(request.ReasoningCamel)
	}
}

func positiveJSONInt64(raw json.RawMessage) *int64 {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var parsed any
	if decoder.Decode(&parsed) != nil {
		return nil
	}
	number, ok := parsed.(json.Number)
	if !ok {
		return nil
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return nil
	}
	value := rational.Num().Int64()
	if value <= 0 {
		return nil
	}
	return &value
}

func reasoningEffortJSON(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return telemetry.ReasoningEffort(value)
}
func parseTelemetryFilter(r *http.Request) (telemetry.Filter, error) {
	q := r.URL.Query()
	f := telemetry.Filter{Model: q.Get("model"), AccountUID: q.Get("account_uid"), Status: q.Get("status"), ErrorType: q.Get("error_type"), Cursor: q.Get("cursor"), Limit: 50}
	var err error
	if v := q.Get("from"); v != "" {
		f.From, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return f, err
		}
	}
	if v := q.Get("to"); v != "" {
		f.To, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return f, err
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return f, strconv.ErrSyntax
	}
	if f.Status != "" && f.Status != "success" && f.Status != "error" {
		return f, strconv.ErrSyntax
	}
	if len(f.Model) > 160 || len(f.AccountUID) > 128 || len(f.Cursor) > 1024 {
		return f, strconv.ErrSyntax
	}
	if f.ErrorType != "" && telemetry.ErrorType(f.ErrorType) != f.ErrorType {
		return f, strconv.ErrSyntax
	}
	if v := q.Get("stream"); v != "" {
		if v != "true" && v != "false" {
			return f, strconv.ErrSyntax
		}
		b := v == "true"
		f.Stream = &b
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, err = strconv.Atoi(v)
		if err != nil || f.Limit < 1 || f.Limit > 200 {
			return f, strconv.ErrSyntax
		}
	}
	return f, nil
}
func (h *Handler) telemetryRequests(w http.ResponseWriter, r *http.Request) {
	f, err := parseTelemetryFilter(r)
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request", "invalid telemetry filter")
		return
	}
	p, err := h.cfg.Telemetry.List(f)
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request", "invalid telemetry cursor")
		return
	}
	writeJSON(w, 200, p)
}
func (h *Handler) telemetryRequest(w http.ResponseWriter, r *http.Request) {
	e, ok := h.cfg.Telemetry.Get(r.PathValue("id"))
	if !ok {
		writeOpenAIError(w, 404, "not_found", "request not found")
		return
	}
	writeJSON(w, 200, e)
}
func (h *Handler) telemetrySummary(w http.ResponseWriter, r *http.Request) {
	f, err := parseTelemetryFilter(r)
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request", "invalid telemetry filter")
		return
	}
	ranges := map[string]time.Duration{"": 24 * time.Hour, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}
	buckets := map[string]time.Duration{"": time.Hour, "5m": 5 * time.Minute, "1h": time.Hour, "1d": 24 * time.Hour}
	span, ok := ranges[r.URL.Query().Get("range")]
	bucket, ok2 := buckets[r.URL.Query().Get("bucket")]
	if !ok || !ok2 {
		writeOpenAIError(w, 400, "invalid_request", "invalid range or bucket")
		return
	}
	now := time.Now().UTC()
	writeJSON(w, 200, h.cfg.Telemetry.Summary(now.Add(-span), now, bucket, f))
}

// Decode only explicitly allowed measurements; no upstream strings are retained.
func (s *chatStatsReader) observeTelemetryChunk(payload string) {
	var frame struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			Prompt            *float64 `json:"prompt_tokens"`
			Completion        *float64 `json:"completion_tokens"`
			Hit               *float64 `json:"prompt_cache_hit_tokens"`
			Miss              *float64 `json:"prompt_cache_miss_tokens"`
			Reasoning         *int64   `json:"reasoning_tokens"`
			CompletionDetails *struct {
				Reasoning *int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			PromptDetails *struct {
				Cached *float64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &frame) != nil {
		return
	}
	if len(frame.Error) > 0 && string(frame.Error) != "null" {
		s.telemetryError = true
	}
	for _, choice := range frame.Choices {
		if finish := telemetry.FinishReason(choice.FinishReason); finish != "" {
			if finish == "length" || s.finishReason != "length" {
				s.finishReason = finish
			}
		}
	}
	if frame.Usage != nil {
		u := frame.Usage
		s.telemetryPrompt = u.Prompt
		s.telemetryCompletion = u.Completion
		s.telemetryCacheHit = u.Hit
		s.telemetryCacheMiss = u.Miss
		if u.Hit == nil && u.PromptDetails != nil {
			s.telemetryCacheHit = u.PromptDetails.Cached
		}
		reasoning := u.Reasoning
		if reasoning == nil && u.CompletionDetails != nil {
			reasoning = u.CompletionDetails.Reasoning
		}
		if reasoning != nil && *reasoning > 0 {
			value := *reasoning
			s.telemetryReasoning = &value
		}
	}
}
