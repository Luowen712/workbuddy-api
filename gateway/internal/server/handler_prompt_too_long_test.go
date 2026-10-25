package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// handler_prompt_too_long_test.go 11115 prompt too long 端到端验收（任务书
// ptl-passthrough-restore：只恢复透传语义，无出站预估——用户裁定移除预估拦截）：
// 上游真实 11115 → 分类不罚号不轮转、透传原文（非固定文案）；空 body → 兜底短文案。

// TestChatPromptTooLongPassesThroughUpstreamBody 上游 400 + 11115（含真实 token
// 数/上限值/requestId 原文）→ 不轮转（多账号池 calls=1，换号同样超限白扔配额）、
// 不罚号（无冷却/禁用/熔断计数）、error.message 逐字透传上游原文（上游原文是
// 最有价值的错误信息，客户端必须看到，禁止固定词覆盖）。
func TestChatPromptTooLongPassesThroughUpstreamBody(t *testing.T) {
	const raw = `{"code":11115,"msg":"prompt is too long: 120000 tokens > 65536 maximum","requestId":"req-11115-abc"}`
	calls := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls[authz]++
		return 400, raw, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s want 400", rec.Code, rec.Body)
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("resp not json: %v body=%s", err, rec.Body)
	}
	if e.Error.Code != "prompt_too_long" {
		t.Errorf("code=%q want prompt_too_long", e.Error.Code)
	}
	// message 必须逐字等于上游原文（透传，非固定文案）。
	if e.Error.Message != raw {
		t.Errorf("message=%q want raw upstream body passthrough %q", e.Error.Message, raw)
	}
	if !strings.Contains(e.Error.Message, "120000 tokens > 65536 maximum") || !strings.Contains(e.Error.Message, "req-11115-abc") {
		t.Errorf("message must preserve real token count/limit/requestId: %s", e.Error.Message)
	}
	if strings.Contains(e.Error.Message, "all accounts are temporarily unavailable") {
		t.Errorf("message must NOT be the fixed local scheduling text: %s", e.Error.Message)
	}
	// 不轮转：多账号池也只打第一个号（换号同样超限，白扔配额）。
	if calls["Bearer at1"]+calls["Bearer at2"] != 1 {
		t.Errorf("upstream calls=%v want exactly 1 (no rotation on 11115)", calls)
	}
	// 不罚号：a1 无冷却/无禁用/无熔断/无连败计数。
	st, _ := p.Status("a1")
	if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.BreakerFails != 0 {
		t.Errorf("11115 must not penalize account (request-level error): %+v", st)
	}
}

// TestChatPromptTooLongSingleAccount 413/404 状态码上的 11115 同样走透传分支
// （promptTooLongRule 认请求级 4xx 家族）。
func TestChatPromptTooLongSingleAccount(t *testing.T) {
	for _, status := range []int{400, 404, 413} {
		raw := `{"code":11115,"msg":"prompt is too long: 120000 tokens > 65536 maximum","requestId":"r"}`
		up := newFakeUpstream(t, func(authz string) (int, string, bool) {
			return status, raw, false
		})
		h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}), Upstream: up})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatalf("status=%d: resp not json: %v body=%s", status, err, rec.Body)
		}
		if e.Error.Code != "prompt_too_long" {
			t.Errorf("status=%d: code=%q want prompt_too_long", status, e.Error.Code)
		}
		if e.Error.Message != raw {
			t.Errorf("status=%d: message=%q want passthrough %q", status, e.Error.Message, raw)
		}
	}
}

// TestPromptTooLongMessageFallback 空 body 兜底（单元级）：分类要求 body 命中
// 11115 marker，真走到透传分支时 body 必非空——兜底是防御分支（未来分类口径
// 变化等），无上游原文可透传时保留可读分类短文案（不编造原文、不用本地调度
// 固定词覆盖）；非空 body 原样返回。
func TestPromptTooLongMessageFallback(t *testing.T) {
	const raw = `{"code":11115,"msg":"prompt is too long: 120000 tokens > 65536 maximum","requestId":"r"}`
	for _, tc := range []struct {
		name, in, want string
	}{
		{"raw passthrough", raw, raw},
		{"empty", "", "prompt is too long"},
		{"whitespace only", " \n\t ", "prompt is too long"},
	} {
		if got := promptTooLongMessage(tc.in); got != tc.want {
			t.Errorf("%s: promptTooLongMessage(%q)=%q want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestChatOutputTooLongPreservesDiagnosticsWithoutRotation(t *testing.T) {
	const raw = `{"error":{"message":"max_tokens must be <= 32000","code":"invalid_max_tokens","param":"max_tokens","authorization":"Bearer provider-secret"},"request_id":"provider-request-123","request_body":"private prompt"}`
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return http.StatusBadRequest, raw, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "account-token-1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "account-token-2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"client prompt"}]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var envelope struct {
		Error struct {
			Type              string `json:"type"`
			Code              string `json:"code"`
			Message           string `json:"message"`
			Param             string `json:"param"`
			ProviderRequestID string `json:"provider_request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if got := envelope.Error; got.Type != "output_too_long" || got.Code != "invalid_max_tokens" ||
		got.Message != "max_tokens must be <= 32000" || got.Param != "max_tokens" ||
		got.ProviderRequestID != "provider-request-123" {
		t.Fatalf("diagnostics not preserved: %+v", got)
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d want 1", calls)
	}
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.BreakerFails != 0 {
			t.Fatalf("account %s penalized: %+v", uid, st)
		}
	}
	for _, secret := range []string{"provider-secret", "private prompt", "client prompt", "account-token"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, rec.Body)
		}
	}
}

func TestChatOutputTooLong422DoesNotRotateOrPenalize(t *testing.T) {
	const raw = `{"error":{"message":"max_output_tokens must be <= 32000","code":"invalid_max_output_tokens","param":"max_output_tokens"},"requestId":"provider-422"}`
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return http.StatusUnprocessableEntity, raw, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "account-token-1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "account-token-2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`)))

	if rec.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body)
	}
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.BreakerFails != 0 {
			t.Fatalf("account %s penalized: %+v", uid, st)
		}
	}
}

func TestGlobalChatOutputTooLong404UsesOneRequestAndNoAccountPenalty(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	const raw = `{"error":{"message":"max_output_tokens must be <= 32000","code":"invalid_max_output_tokens","param":"max_output_tokens"},"request_id":"provider-global-404"}`
	calls := 0
	up := &upstream.Client{
		GlobalEnabled: true,
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(raw)),
			}, nil
		})},
		ChatBaseCN:     "https://fake.example",
		ChatBaseGlobal: "https://fake.example",
		BillingBaseCN:  "https://fake.example",
	}
	p := testPoolWith(
		&auth.Auth{UID: "g1", AccessToken: "account-token-1", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999},
		&auth.Auth{UID: "g2", AccessToken: "account-token-2", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"global:gpt-5.4","messages":[]}`)))

	if rec.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body)
	}
	for _, uid := range []string{"g1", "g2"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.BreakerFails != 0 {
			t.Fatalf("account %s penalized: %+v", uid, st)
		}
	}
}

func TestMaxCompletionTokensMalformedRejectedBeforeAccountSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"string", `"32000"`},
		{"negative", `-1`},
		{"fraction", `1.5`},
		{"unsafe magnitude", `9007199254740992`},
		{"fraction beyond float precision", `1.0000000000000000000000000000000001`},
		{"negative exponent underflow", `-1e-325`},
		{"safe max with fractional tail", `9007199254740991.1`},
		{"exponent above safe max", `9.0071992547409911e15`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				calls++
				return http.StatusOK, "", false
			})
			p := testPoolWith(&auth.Auth{UID: "a1", AccessToken: "account-token", ExpiresAt: 9999999999})
			h := NewHandler(Config{Pool: p, Upstream: up})
			body := `{"model":"m","messages":[],"max_completion_tokens":` + tc.value + `}`
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rec.Code != http.StatusBadRequest || calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body)
			}
			var envelope struct {
				Error struct {
					Code  string `json:"code"`
					Param string `json:"param"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != "invalid_request_error" || envelope.Error.Param != "max_completion_tokens" {
				t.Fatalf("unexpected error: %+v", envelope.Error)
			}
		})
	}
}

func TestMaxCompletionTokensCompatibilityAtRequestBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want float64
	}{
		{"null", `{"model":"m","messages":[],"max_completion_tokens":null}`, 0},
		{"zero", `{"model":"m","messages":[],"max_completion_tokens":0}`, 0},
		{"safe max exponent", `{"model":"m","messages":[],"max_completion_tokens":9.007199254740991e15}`, 9007199254740991},
		{"explicit max_tokens wins", `{"model":"m","messages":[],"max_tokens":64,"max_completion_tokens":"invalid"}`, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outbound map[string]any
			up := newFakeUpstream(t, func(string) (int, string, bool) {
				return http.StatusOK, sseOK, true
			})
			original := up.HTTP.Transport
			up.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &outbound); err != nil {
					t.Fatal(err)
				}
				r.Body = io.NopCloser(strings.NewReader(string(raw)))
				return original.RoundTrip(r)
			})
			h := NewHandler(Config{
				Pool:     testPoolWith(&auth.Auth{UID: "a1", AccessToken: "account-token", ExpiresAt: 9999999999}),
				Upstream: up,
			})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body)))

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if _, present := outbound["max_completion_tokens"]; present {
				t.Fatalf("alias forwarded: %#v", outbound)
			}
			got, present := outbound["max_tokens"]
			if tc.want == 0 && present {
				t.Fatalf("unset alias created forwarded budget: %#v", outbound)
			}
			if tc.want != 0 && got != tc.want {
				t.Fatalf("max_tokens=%v want %v", got, tc.want)
			}
		})
	}
}

func TestProviderErrorDiagnosticsAreBoundedAndWhitelisted(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code": strings.Repeat("c", 130), "message": "  " + strings.Repeat("m", 2050),
			"param": strings.Repeat("p", 130), "request_id": strings.Repeat("r", 260),
			"authorization": "Bearer provider-secret", "reasoning": "private reasoning",
		},
		"request_body": "private prompt", "tool_arguments": "private tool args",
	})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, ok := parseProviderErrorDiagnostic(raw, "output_too_long", "output_too_long", "fallback")
	if !ok {
		t.Fatal("structured provider error not recognized")
	}
	code, _ := diagnostic.Code.(string)
	if len(code) != 128 || len(diagnostic.Message) != 2048 || len(diagnostic.Param) != 128 || len(diagnostic.ProviderRequestID) != 256 {
		t.Fatalf("diagnostic bounds not enforced: code=%d message=%d param=%d request_id=%d",
			len(code), len(diagnostic.Message), len(diagnostic.Param), len(diagnostic.ProviderRequestID))
	}
	if !strings.HasPrefix(diagnostic.Message, "  ") {
		t.Fatalf("message was rewritten instead of bounded: %q", diagnostic.Message[:8])
	}
	exposed, err := json.Marshal(diagnostic.fields())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"provider-secret", "private reasoning", "private prompt", "private tool args"} {
		if strings.Contains(string(exposed), secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, exposed)
		}
	}
}
