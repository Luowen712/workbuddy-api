package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTelemetrySummaryAcceptsFractionalMilliseconds(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/telemetry/summary" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"p95_ttfb_ms":12.5,"p95_latency_ms":34.75,"series":[],"models":[{"model":"glm-5.2","p95_ttfb_ms":1.25,"p95_latency_ms":2.5}],"accounts":[],"errors":[],"storage":{}}`))
	}))
	defer ts.Close()

	c := New(ts.URL, func() string { return "" }, time.Second)
	got, err := c.TelemetrySummary(context.Background(), "range=24h")
	if err != nil {
		t.Fatal(err)
	}
	if got.P95TTFBMS == nil || *got.P95TTFBMS != 12.5 || len(got.Models) != 1 || got.Models[0].P95LatencyMS == nil || *got.Models[0].P95LatencyMS != 2.5 {
		t.Fatalf("unexpected summary: %+v", got)
	}
}
