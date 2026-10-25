package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/telemetry"
)

func responsesTestHandler(t *testing.T, stream string, capture *map[string]any) *Handler {
	t.Helper()
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, stream, true })
	if capture != nil {
		original := up.HTTP.Transport
		up.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, capture); err != nil {
				t.Fatal(err)
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			return original.RoundTrip(r)
		})
	}
	return NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "responses-test", AccessToken: "test", ExpiresAt: 9999999999}),
		Upstream: up, APIKey: "test-key",
	})
}

func TestResponsesToolIdentityAndFraming(t *testing.T) {
	stream := ": keepalive\r\n\r\ndata: {\"choices\":[\r\ndata: {\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\"}}]}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_x\",\"function\":{\"name\":\"read\",\"arguments\":\"}\"}}]}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\r\n\r\ndata: [DONE]"
	events := responseEvents(t, callResponses(responsesTestHandler(t, stream, nil), `{"model":"m","input":"x","stream":true}`))
	var arguments string
	for _, event := range events {
		if event["type"] == "response.output_item.added" {
			item := event["item"].(map[string]any)
			if item["name"] != "read" || item["call_id"] != "call_x" || item["arguments"] != "" {
				t.Fatal("tool identity was emitted too early", item)
			}
		}
		if event["type"] == "response.function_call_arguments.delta" {
			arguments += event["delta"].(string)
		}
	}
	if arguments != "{}" || events[len(events)-1]["type"] != "response.completed" {
		t.Fatal(events)
	}
}

func TestResponsesMalformedToolsFail(t *testing.T) {
	for _, stream := range []string{
		strings.ReplaceAll(responseToolsSSE, `\"a\":`, `broken`),
		strings.ReplaceAll(responseToolsSSE, `"call_b"`, `"call_a"`),
		strings.ReplaceAll(responseToolsSSE, `"name":"sum"`, `"name":""`),
	} {
		events := responseEvents(t, callResponses(responsesTestHandler(t, stream, nil), `{"model":"m","input":"x","stream":true}`))
		if events[len(events)-1]["type"] != "response.failed" {
			t.Fatal(events)
		}
	}
}

func TestResponsesMessageFallback(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`,
		`{"choices":[{"delta":{"content":"Hello"},"message":{"content":"Hello"},"finish_reason":"stop"}]}`,
	} {
		stream := "data: " + body + "\n\ndata: [DONE]\n\n"
		result := decodeResponse(t, callResponses(responsesTestHandler(t, stream, nil), `{"model":"m","input":"x"}`))
		if result["output_text"] != "Hello" {
			t.Fatal(result)
		}
	}
	stream := `data: {"choices":[{"message":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	result := decodeResponse(t, callResponses(responsesTestHandler(t, stream, nil), `{"model":"m","input":"x"}`))
	if len(result["output"].([]any)) != 1 {
		t.Fatal("message tool lost", result)
	}
}

type canceledResponseReader struct{}

func (canceledResponseReader) Read([]byte) (int, error) { return 0, context.Canceled }

type disconnectedResponseWriter struct{ httptest.ResponseRecorder }

func (*disconnectedResponseWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResponsesCancellationAndMissingUsage(t *testing.T) {
	options := &responsesOptions{model: "m", fields: map[string]any{}}
	rec := httptest.NewRecorder()
	if _, err := collectResponses(canceledResponseReader{}, options, rec); err == nil {
		t.Fatal("cancellation swallowed")
	}
	events := responseEvents(t, rec)
	if events[len(events)-1]["type"] != "response.failed" {
		t.Fatal(events)
	}
	disconnected := &disconnectedResponseWriter{*httptest.NewRecorder()}
	if _, err := collectResponses(strings.NewReader(sseOK), options, disconnected); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("write error swallowed", err)
	}
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n"
	result, err := collectResponses(strings.NewReader(stream), options, nil)
	if err != nil || result["usage"] != nil {
		t.Fatal("missing usage fabricated", result, err)
	}
}

func TestResponsesTelemetryOnce(t *testing.T) {
	for _, failed := range []bool{false, true} {
		stream := sseOK
		if failed {
			stream = "data: [DONE]\n\n"
		}
		h := responsesTestHandler(t, stream, nil)
		store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
		h.cfg.Telemetry = store
		callResponses(h, `{"model":"m","input":"private prompt","stream":true}`)
		page, _ := store.List(telemetry.Filter{})
		if len(page.Items) != 1 {
			t.Fatalf("telemetry count=%d", len(page.Items))
		}
		if failed && page.Items[0].ErrorType != "stream_error" {
			t.Fatal(page.Items[0])
		}
	}
}

func TestResponsesTruncationIsVisibleInTelemetry(t *testing.T) {
	const stream = "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"x\"},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":12056,\"completion_tokens\":32000,\"total_tokens\":44056}}\n\ndata: [DONE]\n\n"
	h := responsesTestHandler(t, stream, nil)
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	h.cfg.Telemetry = store
	result := decodeResponse(t, callResponses(h,
		`{"model":"cn:deepseek-v4.1-flash","input":"x","max_output_tokens":128000}`))
	if result["status"] != "incomplete" {
		t.Fatal(result)
	}
	page, err := store.List(telemetry.Filter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("events=%v, err=%v", page.Items, err)
	}
	if page.Items[0].ErrorType != "output_truncated" {
		t.Fatalf("truncation hidden: %+v", page.Items[0])
	}
}

func TestResponsesSSETruncationKeepsPartialOutputAndMetadata(t *testing.T) {
	const stream = "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"partial reasoning\"},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":12056,\"completion_tokens\":32000,\"total_tokens\":44056,\"completion_tokens_details\":{\"reasoning_tokens\":30000}}}\n\ndata: [DONE]\n\n"
	h := responsesTestHandler(t, stream, nil)
	store := telemetry.New(telemetry.Config{Enabled: true, Dir: t.TempDir()})
	h.cfg.Telemetry = store
	rec := callResponses(h, `{"model":"cn:deepseek-v4.1-flash","input":"x","max_output_tokens":128000,"reasoning":{"effort":"high"},"stream":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "partial reasoning") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	events := responseEvents(t, rec)
	if events[len(events)-1]["type"] != "response.incomplete" {
		t.Fatalf("events=%v", events)
	}
	page, err := store.List(telemetry.Filter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("events=%v, err=%v", page.Items, err)
	}
	e := page.Items[0]
	if e.Status != http.StatusOK || e.ErrorType != "output_truncated" || e.FinishReason != "length" ||
		e.RequestedMaxOutputTokens == nil || *e.RequestedMaxOutputTokens != 128000 ||
		e.ForwardedMaxOutputTokens == nil || *e.ForwardedMaxOutputTokens != 128000 ||
		e.RequestedReasoningEffort != "high" || e.ForwardedReasoningEffort != "high" ||
		e.ReasoningTokens == nil || *e.ReasoningTokens != 30000 {
		t.Fatalf("truncation metadata missing: %+v", e)
	}
}

func callResponses(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func responseEvents(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("not a successful SSE response: status=%d body=%s", rec.Code, rec.Body)
	}
	var events []map[string]any
	for _, block := range strings.Split(rec.Body.String(), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("invalid Responses event: %q", block)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event["type"] != strings.TrimPrefix(lines[0], "event: ") {
			t.Fatal("event name/type mismatch")
		}
		if event["sequence_number"] != float64(len(events)) {
			t.Fatal("non-monotonic event sequence")
		}
		events = append(events, event)
	}
	return events
}

func TestResponsesTextAndRequestConversion(t *testing.T) {
	var sent map[string]any
	h := responsesTestHandler(t, sseOK, &sent)
	result := decodeResponse(t, callResponses(h, `{"model":"cn:glm-5.2","instructions":"Be brief","input":"Hello","max_output_tokens":256,"store":false}`))
	if result["object"] != "response" || result["status"] != "completed" || result["output_text"] != "你好" {
		t.Fatalf("invalid response: %#v", result)
	}
	if !strings.HasPrefix(result["id"].(string), "resp_") || result["store"] != false {
		t.Fatal("response identity/storage")
	}
	items := result["output"].([]any)
	msg := items[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "assistant" || msg["status"] != "completed" {
		t.Fatal(msg)
	}
	usage := result["usage"].(map[string]any)
	if usage["input_tokens"] != float64(1) || usage["output_tokens"] != float64(1) || usage["total_tokens"] != float64(2) {
		t.Fatal(usage)
	}
	if sent["model"] != "glm-5.2" || sent["max_tokens"] != float64(256) {
		t.Fatalf("incorrect upstream request: %#v", sent)
	}
	messages := sent["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["content"] != "Be brief" || messages[1].(map[string]any)["content"] != "Hello" {
		t.Fatalf("instructions/input lost: %#v", messages)
	}
}

func TestResponsesLargeBudgetForwarding(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, body := range []string{
			`{"model":"cn:deepseek-v4.1-flash","input":"x","max_output_tokens":128000}`,
			`{"model":"cn:deepseek-v4.1-flash","input":"x","max_output_tokens":128}`,
		} {
			var incoming, sent map[string]any
			if err := json.Unmarshal([]byte(body), &incoming); err != nil {
				t.Fatal(err)
			}
			if stream {
				body = strings.TrimSuffix(body, "}") + `,"stream":true}`
			}
			h := responsesTestHandler(t, sseOK, &sent)
			if stream {
				responseEvents(t, callResponses(h, body))
			} else {
				decodeResponse(t, callResponses(h, body))
			}
			if sent["max_tokens"] != incoming["max_output_tokens"] {
				t.Fatalf("stream=%v outbound budget=%v, requested=%v", stream, sent["max_tokens"], incoming["max_output_tokens"])
			}
			if _, exists := sent["max_output_tokens"]; exists {
				t.Fatal("Responses alias leaked upstream")
			}
		}
	}
}

func TestResponsesStreamingEvents(t *testing.T) {
	rec := callResponses(responsesTestHandler(t, sseOK, nil), `{"model":"glm-5.2","input":"Hi","stream":true}`)
	events := responseEvents(t, rec)
	types := []string{}
	for _, event := range events {
		types = append(types, event["type"].(string))
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v", types)
	}
	final := events[len(events)-1]["response"].(map[string]any)
	if final["output_text"] != "你好" || !rec.Flushed {
		t.Fatal("not flushed/finalized")
	}
	for _, e := range events {
		if response, ok := e["response"].(map[string]any); ok && response["id"] != final["id"] {
			t.Fatal("response id changed")
		}
	}
}

const responseToolsSSE = `data: {"choices":[{"index":0,"delta":{"reasoning_content":"Plan"}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"sum","arguments":"{\"a\":"}},{"index":1,"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"test.txt\"}"}},{"index":0,"function":{"arguments":"2}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}

data: [DONE]

`

func TestResponsesParallelToolsAndReasoning(t *testing.T) {
	rec := callResponses(responsesTestHandler(t, responseToolsSSE, nil), `{"model":"glm-5.2","input":"Hi","stream":true}`)
	events := responseEvents(t, rec)
	final := events[len(events)-1]["response"].(map[string]any)
	if final["status"] != "completed" {
		t.Fatal(final)
	}
	items := final["output"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["type"] != "reasoning" {
		t.Fatal(items)
	}
	a, b := items[1].(map[string]any), items[2].(map[string]any)
	if a["type"] != "function_call" || a["name"] != "sum" || a["arguments"] != `{"a":2}` || a["call_id"] != "call_a" {
		t.Fatal(a)
	}
	if b["name"] != "read" || b["arguments"] != `{"path":"test.txt"}` || b["call_id"] != "call_b" {
		t.Fatal(b)
	}
	usage := final["usage"].(map[string]any)
	if usage["input_tokens_details"].(map[string]any)["cached_tokens"] != float64(4) {
		t.Fatal(usage)
	}
	if usage["output_tokens_details"].(map[string]any)["reasoning_tokens"] != float64(3) {
		t.Fatal(usage)
	}
}

func TestResponsesToolResultsAndStructuredText(t *testing.T) {
	var sent map[string]any
	h := responsesTestHandler(t, sseOK, &sent)
	decodeResponse(t, callResponses(h, `{
		"model":"glm-5.2","input":[
			{"role":"user","content":[{"type":"input_text","text":"Read"}]},
			{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Plan"}]},
			{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":"file contents"},
			{"role":"user","content":"Summarize"}
		],
		"tools":[{"type":"function","name":"read","description":"Read a file","parameters":{"type":"object","properties":{}},"strict":true}],
		"tool_choice":"auto","parallel_tool_calls":false,
		"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object","properties":{"answer":{"type":"string"}}},"strict":true}}
	}`))
	messages := sent["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("wrong replay grouping: %#v", messages)
	}
	assistant := messages[1].(map[string]any)
	if assistant["reasoning_content"] != "Plan" || len(assistant["tool_calls"].([]any)) != 1 {
		t.Fatal(assistant)
	}
	result := messages[2].(map[string]any)
	if result["role"] != "tool" || result["tool_call_id"] != "call_a" || result["content"] != "file contents" {
		t.Fatal(result)
	}
	tool := sent["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tool["name"] != "read" || tool["strict"] != true {
		t.Fatal(tool)
	}
	format := sent["response_format"].(map[string]any)
	if format["type"] != "json_schema" || format["json_schema"].(map[string]any)["name"] != "answer" {
		t.Fatal(format)
	}
}

func TestResponsesRejectUnsupportedAndMalformedBeforeUpstream(t *testing.T) {
	cases := []string{
		`null`, `[]`, `{`, `{}`, `{"model":"m","input":123}`, `{"model":"m","input":"x","stream":"yes"}`,
		`{"model":"m","input":"x","previous_response_id":"resp_old"}`,
		`{"model":"m","input":"x","store":true}`, `{"model":"m","input":"x","background":true}`,
		`{"model":"m","input":"x","tools":[{"type":"web_search"}]}`,
		`{"model":"m","input":[{"type":"item_reference","id":"item"}]}`,
		`{"model":"m","input":[{"role":"user","content":[{"type":"input_file","file_id":"file"}]}]}`,
		`{"model":"m","input":[{"type":"function_call_output","call_id":"unknown","output":"x"}]}`,
		`{"model":"m","input":"x","max_output_tokens":-1}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			var sent map[string]any
			rec := callResponses(responsesTestHandler(t, sseOK, &sent), body)
			if rec.Code != 400 || sent != nil {
				t.Fatalf("status=%d upstream=%v body=%s", rec.Code, sent != nil, rec.Body)
			}
			assertJSONErrorCode(t, rec.Body.String(), "invalid_request_error")
		})
	}
	h := responsesTestHandler(t, sseOK, nil)
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"x"}`)))
	if unauth.Code != 401 {
		t.Fatal("Responses must use existing key auth")
	}
	h.cfg.MaxBodyBytes = 20
	if callResponses(h, `{"model":"m","input":"oversized"}`).Code != 413 {
		t.Fatal("body limit not enforced")
	}
}

func TestResponsesFailureAndIncomplete(t *testing.T) {
	for _, failure := range []string{
		"data: [DONE]\n\n",
		"data: {\"error\":{\"message\":\"upstream unavailable\",\"code\":\"bad\"}}\n\n",
		"data: not-json\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
	} {
		h := responsesTestHandler(t, failure, nil)
		plain := callResponses(h, `{"model":"glm-5.2","input":"Hi"}`)
		if plain.Code != 502 {
			t.Fatalf("false success: %d %s", plain.Code, plain.Body)
		}
		events := responseEvents(t, callResponses(h, `{"model":"glm-5.2","input":"Hi","stream":true}`))
		if events[len(events)-1]["type"] != "response.failed" {
			t.Fatalf("missing failure: %#v", events)
		}
	}
	incomplete := strings.ReplaceAll(sseOK, `"finish_reason":"stop"`, `"finish_reason":"length"`)
	result := decodeResponse(t, callResponses(responsesTestHandler(t, incomplete, nil), `{"model":"glm-5.2","input":"Hi"}`))
	if result["status"] != "incomplete" || result["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatal(result)
	}
}

func TestResponsesOutputTokenErrorPreservesStructuredDiagnostics(t *testing.T) {
	const failure = "data: {\"error\":{\"message\":\"max_output_tokens must be <= 32000\",\"code\":\"invalid_max_output_tokens\",\"param\":\"max_output_tokens\",\"request_id\":\"provider-response-123\",\"authorization\":\"Bearer provider-secret\",\"reasoning\":\"private reasoning\",\"tool_arguments\":\"private tool args\"}}\n\n"
	h := responsesTestHandler(t, failure, nil)

	plain := callResponses(h, `{"model":"m","input":"private prompt"}`)
	if plain.Code != http.StatusBadRequest {
		t.Fatalf("sync status=%d body=%s", plain.Code, plain.Body)
	}
	assertResponseDiagnostic := func(t *testing.T, got map[string]any) {
		t.Helper()
		if got["type"] != "output_too_long" || got["code"] != "invalid_max_output_tokens" ||
			got["message"] != "max_output_tokens must be <= 32000" || got["param"] != "max_output_tokens" ||
			got["provider_request_id"] != "provider-response-123" {
			t.Fatalf("diagnostics not preserved: %#v", got)
		}
		raw, _ := json.Marshal(got)
		for _, secret := range []string{"provider-secret", "private reasoning", "private tool args", "private prompt"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("diagnostics leaked %q: %s", secret, raw)
			}
		}
	}
	var plainBody map[string]any
	if err := json.Unmarshal(plain.Body.Bytes(), &plainBody); err != nil {
		t.Fatal(err)
	}
	assertResponseDiagnostic(t, plainBody["error"].(map[string]any))

	events := responseEvents(t, callResponses(h, `{"model":"m","input":"private prompt","stream":true}`))
	last := events[len(events)-1]
	if last["type"] != "response.failed" {
		t.Fatalf("missing response.failed: %#v", last)
	}
	response := last["response"].(map[string]any)
	assertResponseDiagnostic(t, response["error"].(map[string]any))
}
