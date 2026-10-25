package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// payload_max_completion_tokens_test.go max_completion_tokens → max_tokens 翻译
// （吸收 PR #116，Closes #117）。用例：PR 原 2 例（别名翻译 / 显式优先）+
// 边界补强（0/null/负数/浮尾不翻译、两字段并存删别名、int 形态）。

// TestPrepareBodyConvertsMaxCompletionTokens（PR #116 用例 1）：
// 别名 128000 → max_tokens=128000，别名删除。
func TestPrepareBodyConvertsMaxCompletionTokens(t *testing.T) {
	out := PrepareBodyOptWithEffortsAndDefault([]byte(`{"model":"deepseek-v4.1-flash","messages":[],"max_completion_tokens":128000}`), false, nil, nil)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(128000) {
		t.Fatalf("max_tokens=%v, want 128000", got["max_tokens"])
	}
	if _, ok := got["max_completion_tokens"]; ok {
		t.Fatal("alias remains")
	}
}

// TestPrepareBodyPrefersMaxTokens（PR #116 用例 2）：
// 两字段并存取显式 max_tokens（64000），别名删除。
func TestPrepareBodyPrefersMaxTokens(t *testing.T) {
	out := PrepareBodyOptWithEffortsAndDefault([]byte(`{"messages":[],"max_tokens":64000,"max_completion_tokens":128000}`), false, nil, nil)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(64000) {
		t.Fatalf("max_tokens=%v, want 64000", got["max_tokens"])
	}
	if _, ok := got["max_completion_tokens"]; ok {
		t.Fatal("alias remains")
	}
}

func TestPrepareBodyLeavesOmittedTokenBudgetOmitted(t *testing.T) {
	out := PrepareBodyOptWithEffortsAndDefault([]byte(`{"messages":[]}`), false, nil, nil)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["max_tokens"]; ok {
		t.Fatalf("omitted budget became max_tokens=%v", got["max_tokens"])
	}
	if _, ok := got["max_completion_tokens"]; ok {
		t.Fatal("omitted alias appeared")
	}
}

func TestPreparedRequestObserverSeesFinalBoundedMetadataOnly(t *testing.T) {
	const model = "deepseek-v4.1-flash"
	requestID := strings.Repeat("r", 256)
	var outbound map[string]any
	c := testClient(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &outbound); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}, nil
	})
	c.storeEfforts("cn", map[string][]string{model: {"high"}}, nil)
	var observed PreparedRequestObservation
	c.ObservePreparedRequest = func(got PreparedRequestObservation) { observed = got }
	var perCallObserved PreparedRequestObservation
	body := []byte(`{"model":"deepseek-v4.1-flash","max_completion_tokens":128000,"reasoning_effort":"max","api_key":"credential","messages":[{"role":"user","content":"private prompt"}],"tools":[{"type":"function","function":{"name":"secret_tool","arguments":"private arguments"}}]}`)
	rc, status, _, err := c.ChatStream(&auth.Auth{AccessToken: "account-token", UID: "uid"}, body, "", ChatMeta{
		ConversationRequestID: requestID,
		ObservePreparedRequest: func(got PreparedRequestObservation) {
			perCallObserved = got
		},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("chat status=%d err=%v", status, err)
	}
	rc.Close()
	if outbound["max_tokens"] != float64(128000) || outbound["reasoning_effort"] != "high" {
		t.Fatalf("prepared body was not normalized: %#v", outbound)
	}
	if observed.Model != model || observed.Realm != "cn" || observed.RequestID != strings.Repeat("r", 128) {
		t.Fatalf("unexpected identity metadata: %+v", observed)
	}
	if observed.MaxTokens == nil || *observed.MaxTokens != 128000 || observed.ReasoningEffort != "high" {
		t.Fatalf("unexpected prepared metadata: %+v", observed)
	}
	if perCallObserved != observed {
		t.Fatalf("per-call observer=%+v client observer=%+v", perCallObserved, observed)
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	var exposed map[string]any
	if err := json.Unmarshal(raw, &exposed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "realm", "request_id", "max_tokens", "reasoning_effort"} {
		if _, ok := exposed[key]; !ok {
			t.Fatalf("observer metadata missing %q: %s", key, raw)
		}
	}
	if len(exposed) != 5 {
		t.Fatalf("observer exposed unexpected fields: %s", raw)
	}
	for _, secret := range []string{"private prompt", "private arguments", "secret_tool", "credential", "account-token"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("observer exposed sensitive data %q: %s", secret, raw)
		}
	}
}

func TestPreparedRequestObserverKeepsFinalNoneReasoningEffort(t *testing.T) {
	for _, field := range []string{"reasoning_effort", "reasoningEffort"} {
		t.Run(field, func(t *testing.T) {
			c := &Client{}
			var observed PreparedRequestObservation
			prepared := []byte(`{"model":"m","` + field + `":"none"}`)
			c.observePreparedRequest(prepared, "cn", "request-id", func(got PreparedRequestObservation) {
				observed = got
			})
			if observed.ReasoningEffort != "none" {
				t.Fatalf("reasoning effort=%q want none", observed.ReasoningEffort)
			}
		})
	}
	c := &Client{}
	var observed PreparedRequestObservation
	c.observePreparedRequest([]byte(`{"model":"m","reasoning_effort":"private-value"}`), "cn", "request-id", func(got PreparedRequestObservation) {
		observed = got
	})
	if observed.ReasoningEffort != "" {
		t.Fatalf("unbounded reasoning effort retained: %q", observed.ReasoningEffort)
	}
}

// TestMaxCompletionTokensUnsetCompatibility 共享请求边界仍兼容 omitted/null/zero；
// payload 层不把 null/zero 变成转发预算，别名照常删除。
func TestMaxCompletionTokensUnsetCompatibility(t *testing.T) {
	cases := []struct {
		name      string
		aliasJSON string
	}{
		{"omitted", ``},
		{"zero", `"max_completion_tokens":0`},
		{"null", `"max_completion_tokens":null`},
	}
	for _, c := range cases {
		suffix := c.aliasJSON
		if suffix != "" {
			suffix = "," + suffix
		}
		src := []byte(`{"messages":[]` + suffix + `}`)
		out := PrepareBodyOptWithEffortsAndDefault(src, false, nil, nil)
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: unmarshal: %v", c.name, err)
		}
		if _, ok := got["max_tokens"]; ok {
			t.Errorf("%s: max_tokens must NOT be set from non-positive/invalid alias, got %v", c.name, got["max_tokens"])
		}
		if _, ok := got["max_completion_tokens"]; ok {
			t.Errorf("%s: alias must be deleted", c.name)
		}
	}
}

// TestMaxCompletionTokensExplicitZeroKept 显式 max_tokens=0 与别名并存：
// 显式字段原样保留（0 语义 = 上游默认，不覆盖成别名值），别名删除。
func TestMaxCompletionTokensExplicitZeroKept(t *testing.T) {
	out := PrepareBodyOptWithEffortsAndDefault([]byte(`{"messages":[],"max_tokens":0,"max_completion_tokens":128000}`), false, nil, nil)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["max_tokens"]; !ok || v != float64(0) {
		t.Fatalf("explicit max_tokens=0 must be preserved verbatim, got %v ok=%v", v, ok)
	}
	if _, ok := got["max_completion_tokens"]; ok {
		t.Error("alias remains")
	}
}

// TestTranslateMaxCompletionTokensIntForms 手构造 map 的 int 形态（非 JSON 解码路径）
// 防御性兼容：正 int/int64 翻译，非正不翻译。
func TestTranslateMaxCompletionTokensIntForms(t *testing.T) {
	obj := map[string]any{"max_completion_tokens": int(42)}
	translateMaxCompletionTokens(obj)
	if v, ok := obj["max_tokens"].(int64); !ok || v != 42 {
		t.Errorf("int alias: max_tokens=%v want 42", obj["max_tokens"])
	}
	obj = map[string]any{"max_completion_tokens": int64(-1)}
	translateMaxCompletionTokens(obj)
	if _, ok := obj["max_tokens"]; ok {
		t.Errorf("negative int64 must not translate, got %v", obj["max_tokens"])
	}
}
