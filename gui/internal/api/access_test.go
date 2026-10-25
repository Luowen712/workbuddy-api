package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workbuddy2api-gui/internal/ops"
)

func TestAccessKeyLifecycle(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer original-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer gw.Close()
	cfg := testConfig()
	cfg.GatewayURL = gw.URL
	cfg.UpstreamConfigFile = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg.UpstreamConfigFile, []byte(`{"api_key":"original-key","custom":{"keep":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, ops.New(cfg, nil, nil, nil), http.NotFoundHandler(), "test")
	token, _, _ := s.sessions.Create("admin", "secret123", cfg, "local")
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	for _, method := range []string{"GET", "POST"} {
		if w := call(method, "/api/access", `{}`, false); w.Code != 401 {
			t.Fatalf("unauthenticated access: %d", w.Code)
		}
	}
	crossSite := httptest.NewRequest("POST", "/api/access", strings.NewReader(`{"confirm":true,"expected_key":"original-key"}`))
	crossSite.Header.Set("Authorization", "Bearer "+token)
	crossSite.Header.Set("Origin", "https://untrusted.example")
	crossResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(crossResponse, crossSite)
	if crossResponse.Code != http.StatusForbidden {
		t.Fatal("cross-site rotation must be forbidden")
	}
	w := call("GET", "/api/access", "", true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("access must be available and uncached: %d", w.Code)
	}
	var info struct {
		APIKey string `json:"api_key"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.APIKey != "original-key" || info.Status != "active" {
		t.Fatal("expected original active key")
	}
	for _, body := range []string{`{}`, `{"confirm":true,"expected_key":"stale"}`} {
		if w := call("POST", "/api/access", body, true); w.Code < 400 {
			t.Fatal("rotation must require confirmation and current revision")
		}
	}
	cfg.ReadOnly = true
	if w := call("POST", "/api/access", `{"confirm":true,"expected_key":"original-key"}`, true); w.Code != 403 {
		t.Fatal("read only rotation must be forbidden")
	}
	cfg.ReadOnly = false
	w = call("POST", "/api/access", `{"confirm":true,"expected_key":"original-key"}`, true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotation failed: %d", w.Code)
	}
	raw, _ := os.ReadFile(cfg.UpstreamConfigFile)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	key, _ := doc["api_key"].(string)
	if !strings.HasPrefix(key, "sk-wb-") || len(key) != 70 || doc["custom"] == nil {
		t.Fatal("rotation must generate a strong key and preserve unknown config")
	}
	if retry := call("POST", "/api/access", `{"confirm":true,"expected_key":"original-key"}`, true); retry.Code < 400 {
		t.Fatal("replaying the same rotation must not rotate a second time")
	}
	backup, err := os.ReadFile(cfg.UpstreamConfigFile + ".gui.bak")
	if err != nil || !strings.Contains(string(backup), "original-key") {
		t.Fatal("original config must be backed up")
	}
	w = call("GET", "/api/access", "", true)
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Status != "rejected" {
		t.Fatal("saved key must not be reported active before gateway reload")
	}
	cfg.GatewayAPIKey = "explicit-override"
	if w := call("POST", "/api/access", `{"confirm":true,"expected_key":"`+key+`"}`, true); w.Code < 400 {
		t.Fatal("explicit deployment override must prevent ineffective rotation")
	}
}

func TestFirstKeyIsNotActiveWhileGatewayStillAllowsAnonymousAccess(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer gw.Close()
	cfg := testConfig()
	cfg.GatewayURL = gw.URL
	cfg.UpstreamConfigFile = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg.UpstreamConfigFile, []byte(`{"api_key":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, ops.New(cfg, nil, nil, nil), http.NotFoundHandler(), "test")
	getStatus := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleAccessGet(w, httptest.NewRequest("GET", "/api/access", nil))
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Status
	}
	if getStatus() != "unprotected" {
		t.Fatal("empty key must be reported unprotected")
	}
	if _, _, err := s.svc.RotateAPIKey(""); err != nil {
		t.Fatal(err)
	}
	if getStatus() != "unprotected" {
		t.Fatal("saving first key must not imply authentication is enabled before restart")
	}
}
