package api

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleAccessGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	doc, _, err := s.svc.ReadUpstreamConfig()
	if err != nil {
		writeError(w, err)
		return
	}
	key, _ := doc["api_key"].(string)
	managed := s.cfg.GatewayAPIKey == ""
	if !managed {
		key = s.cfg.GatewayAPIKey
	}
	// Verify the saved key against the running gateway, not merely its config file.
	status := "unreachable"
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	probe := func(candidate string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cfg.GatewayURL, "/")+"/v1/models", nil)
		if err != nil {
			return 0
		}
		if candidate != "" {
			req.Header.Set("Authorization", "Bearer "+candidate)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	switch probe(key) {
	case http.StatusOK:
		if key == "" {
			status = "unprotected"
		} else {
			// An open gateway accepts every key. A positive probe alone is insufficient.
			switch probe("") {
			case http.StatusOK:
				status = "unprotected"
			case http.StatusUnauthorized, http.StatusForbidden:
				status = "active"
			}
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		status = "rejected"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_key": key, "status": status, "managed": managed, "gateway_url": s.cfg.GatewayURL,
	})
}

func (s *Server) handleAccessRotate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Confirm     bool   `json:"confirm"`
		ExpectedKey string `json:"expected_key"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "生成新 Key 需要明确确认"})
		return
	}
	key, fallback, err := s.svc.RotateAPIKey(input.ExpectedKey)
	if err != nil {
		writeError(w, err)
		return
	}
	message := "新 Key 已保存，重启网关后生效；生效后旧 Key 失效。"
	if fallback {
		message += " 当前为单文件挂载，已备份并原地写入。"
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "api_key": key, "message": message})
}
