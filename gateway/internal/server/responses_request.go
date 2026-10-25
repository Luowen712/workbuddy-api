package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type responsesKey struct{}
type responsesOptions struct {
	model                    string
	stream                   bool
	fields                   map[string]any
	requestedMaxOutputTokens *int64
	requestedReasoningEffort string
}

func responsesOf(r *http.Request) *responsesOptions {
	options, _ := r.Context().Value(responsesKey{}).(*responsesOptions)
	return options
}

func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, h.cfg.MaxBodyBytes+1))
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", "cannot read request body")
		return
	}
	if int64(len(body)) > h.cfg.MaxBodyBytes {
		writeOpenAIError(w, 413, "request_body_too_large", "request exceeds server.max_body_mb")
		return
	}
	chat, options, err := translateResponsesRequest(body)
	if err != nil {
		writeOpenAIError(w, 400, "invalid_request_error", err.Error())
		return
	}
	// Reuse routing, cancellation, account leases, retries and telemetry.
	next := r.Clone(context.WithValue(r.Context(), responsesKey{}, options))
	next.Body = io.NopCloser(bytes.NewReader(chat))
	next.ContentLength = int64(len(chat))
	h.chatCompletions(w, next)
}

func translateResponsesRequest(body []byte) ([]byte, *responsesOptions, error) {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		return nil, nil, fmt.Errorf("request body must be a JSON object")
	}
	model, _ := doc["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, nil, fmt.Errorf("model is required")
	}
	fields := map[string]any{
		"instructions": nil, "metadata": map[string]any{}, "temperature": nil, "top_p": nil,
		"tools": []any{}, "tool_choice": "auto", "parallel_tool_calls": true,
		"max_output_tokens": nil, "reasoning": nil,
		"text": map[string]any{"format": map[string]any{"type": "text"}}, "truncation": "disabled",
	}
	options := &responsesOptions{model: model, fields: fields}
	chat := map[string]any{"model": model}
	for key, value := range doc {
		if value == nil {
			continue
		}
		switch key {
		case "model", "input", "instructions", "tools", "tool_choice", "text", "reasoning":
			// Converted below.
		case "stream", "parallel_tool_calls":
			b, ok := value.(bool)
			if !ok {
				return nil, nil, fmt.Errorf("%s must be a boolean", key)
			}
			chat[key] = b
			if key == "stream" {
				options.stream = b
			} else {
				fields[key] = b
			}
		case "store", "background":
			if b, ok := value.(bool); !ok || b {
				return nil, nil, fmt.Errorf("%s=true is not supported; this endpoint is stateless", key)
			}
		case "previous_response_id", "conversation":
			return nil, nil, fmt.Errorf("%s is not supported; send the complete history in input", key)
		case "max_output_tokens", "temperature", "top_p":
			n, ok := value.(float64)
			if !ok || (key == "max_output_tokens" && (n < 1 || n > 2147483647 || n != float64(int64(n)))) ||
				(key == "temperature" && (n < 0 || n > 2)) || (key == "top_p" && (n < 0 || n > 1)) {
				return nil, nil, fmt.Errorf("%s is out of range", key)
			}
			fields[key] = n
			if key == "max_output_tokens" {
				value := int64(n)
				options.requestedMaxOutputTokens = &value
				chat["max_tokens"] = n
			} else {
				chat[key] = n
			}
		case "metadata":
			if _, ok := value.(map[string]any); !ok {
				return nil, nil, fmt.Errorf("metadata must be an object")
			}
			chat[key], fields[key] = value, value
		case "user", "safety_identifier", "prompt_cache_key":
			if _, ok := value.(string); !ok {
				return nil, nil, fmt.Errorf("%s must be a string", key)
			}
			chat[key] = value
		case "truncation":
			if value != "disabled" {
				return nil, nil, fmt.Errorf("automatic truncation is not supported")
			}
		case "include":
			list, ok := value.([]any)
			if !ok {
				return nil, nil, fmt.Errorf("include must be an array")
			}
			for _, entry := range list {
				if entry != "reasoning.encrypted_content" {
					return nil, nil, fmt.Errorf("unsupported include option")
				}
			}
		case "service_tier":
			if value != "auto" && value != "default" {
				return nil, nil, fmt.Errorf("unsupported service_tier")
			}
		default:
			return nil, nil, fmt.Errorf("unsupported Responses parameter: %s", key)
		}
	}
	var messages []any
	if v := doc["instructions"]; v != nil {
		text, ok := v.(string)
		if !ok {
			return nil, nil, fmt.Errorf("instructions must be a string")
		}
		fields["instructions"] = text
		if text != "" {
			messages = append(messages, map[string]any{"role": "developer", "content": text})
		}
	}
	input, err := responsesMessages(doc["input"])
	if err != nil {
		return nil, nil, err
	}
	messages = append(messages, input...)
	if len(messages) == 0 {
		return nil, nil, fmt.Errorf("input must contain at least one message")
	}
	chat["messages"] = messages

	if v := doc["tools"]; v != nil {
		list, ok := v.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("tools must be an array")
		}
		tools := make([]any, 0, len(list))
		for _, v := range list {
			tool, ok := v.(map[string]any)
			if !ok || tool["type"] != "function" {
				return nil, nil, fmt.Errorf("only function tools are supported")
			}
			name, _ := tool["name"].(string)
			if name == "" {
				return nil, nil, fmt.Errorf("function tool name is required")
			}
			fn := map[string]any{"name": name}
			for key, value := range tool {
				switch key {
				case "type", "name":
				case "parameters":
					if _, ok := value.(map[string]any); !ok {
						return nil, nil, fmt.Errorf("tool parameters must be an object")
					}
					fn[key] = value
				case "description":
					if _, ok := value.(string); !ok {
						return nil, nil, fmt.Errorf("tool description must be a string")
					}
					fn[key] = value
				case "strict":
					if value != nil {
						if _, ok := value.(bool); !ok {
							return nil, nil, fmt.Errorf("tool strict must be a boolean")
						}
						fn[key] = value
					}
				default:
					return nil, nil, fmt.Errorf("unsupported function tool field: %s", key)
				}
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		chat["tools"], fields["tools"] = tools, list
	}
	if v := doc["tool_choice"]; v != nil {
		switch choice := v.(type) {
		case string:
			if choice != "auto" && choice != "none" && choice != "required" {
				return nil, nil, fmt.Errorf("invalid tool_choice")
			}
			chat["tool_choice"] = choice
		case map[string]any:
			name, _ := choice["name"].(string)
			if choice["type"] != "function" || name == "" {
				return nil, nil, fmt.Errorf("only named function tool_choice is supported")
			}
			chat["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": name}}
		default:
			return nil, nil, fmt.Errorf("invalid tool_choice")
		}
		fields["tool_choice"] = v
	}
	if v := doc["reasoning"]; v != nil {
		reasoning, ok := v.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("reasoning must be an object")
		}
		for key, value := range reasoning {
			text, ok := value.(string)
			if !ok {
				return nil, nil, fmt.Errorf("reasoning.%s must be a string", key)
			}
			switch key {
			case "effort":
				options.requestedReasoningEffort = text
				chat["reasoning_effort"] = text
				if text == "none" {
					chat["reasoning_effort"] = "off"
				}
			case "summary":
				if text != "auto" && text != "concise" && text != "detailed" {
					return nil, nil, fmt.Errorf("invalid reasoning.summary")
				}
			default:
				return nil, nil, fmt.Errorf("unsupported reasoning field: %s", key)
			}
		}
		fields["reasoning"] = reasoning
	}
	if v := doc["text"]; v != nil {
		text, ok := v.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("text must be an object")
		}
		for key := range text {
			if key != "format" {
				return nil, nil, fmt.Errorf("unsupported text field: %s", key)
			}
		}
		if v := text["format"]; v != nil {
			format, ok := v.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("text.format must be an object")
			}
			switch format["type"] {
			case "text":
			case "json_object":
				chat["response_format"] = map[string]any{"type": "json_object"}
			case "json_schema":
				name, _ := format["name"].(string)
				if _, ok := format["schema"].(map[string]any); !ok || name == "" {
					return nil, nil, fmt.Errorf("json_schema requires name and schema")
				}
				schema := map[string]any{}
				for k, v := range format {
					if k != "type" {
						schema[k] = v
					}
				}
				chat["response_format"] = map[string]any{"type": "json_schema", "json_schema": schema}
			default:
				return nil, nil, fmt.Errorf("unsupported text.format.type")
			}
		}
		fields["text"] = text
	}
	out, err := json.Marshal(chat)
	return out, options, err
}

func responsesMessages(input any) ([]any, error) {
	if text, ok := input.(string); ok {
		return []any{map[string]any{"role": "user", "content": text}}, nil
	}
	if input == nil {
		return nil, nil
	}
	items, ok := input.([]any)
	if !ok {
		return nil, fmt.Errorf("input must be a string or an array")
	}
	messages := []any{}
	calls := map[string]bool{}
	results := map[string]bool{}
	assistant := func() map[string]any {
		if len(messages) > 0 {
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] == "assistant" {
				return last
			}
		}
		m := map[string]any{"role": "assistant", "content": ""}
		messages = append(messages, m)
		return m
	}
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each input item must be an object")
		}
		switch item["type"] {
		case nil, "message":
			role, _ := item["role"].(string)
			if role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return nil, fmt.Errorf("unsupported message role")
			}
			content, err := responsesContent(item["content"])
			if err != nil {
				return nil, err
			}
			// A reasoning item precedes its assistant message/function calls.
			if role == "assistant" && len(messages) > 0 {
				last := messages[len(messages)-1].(map[string]any)
				if last["role"] == role && last["content"] == "" && last["reasoning_content"] != nil {
					last["content"] = content
					continue
				}
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		case "function_call":
			id, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			args, ok := item["arguments"].(string)
			if id == "" || name == "" || !ok || !json.Valid([]byte(args)) || calls[id] {
				return nil, fmt.Errorf("function_call requires unique call_id, name and JSON arguments")
			}
			calls[id] = true
			m := assistant()
			list, _ := m["tool_calls"].([]any)
			m["tool_calls"] = append(list, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}})
		case "function_call_output":
			id, _ := item["call_id"].(string)
			if !calls[id] || results[id] {
				return nil, fmt.Errorf("function_call_output requires a matching preceding function_call in input")
			}
			output, err := responsesContent(item["output"])
			if err != nil {
				return nil, err
			}
			results[id] = true
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": id, "content": output})
		case "reasoning":
			if encrypted, _ := item["encrypted_content"].(string); encrypted != "" {
				return nil, fmt.Errorf("encrypted reasoning cannot be replayed by this gateway")
			}
			var parts []string
			for _, key := range []string{"content", "summary"} {
				if value := item[key]; value != nil {
					list, ok := value.([]any)
					if !ok {
						return nil, fmt.Errorf("reasoning.%s must be an array", key)
					}
					for _, entry := range list {
						part, ok := entry.(map[string]any)
						if !ok {
							return nil, fmt.Errorf("invalid reasoning part")
						}
						text, ok := part["text"].(string)
						if !ok {
							return nil, fmt.Errorf("invalid reasoning text")
						}
						parts = append(parts, text)
					}
				}
				if len(parts) > 0 {
					break
				}
			}
			if len(parts) > 0 {
				m := assistant()
				previous, _ := m["reasoning_content"].(string)
				m["reasoning_content"] = previous + strings.Join(parts, "\n")
			}
		default:
			return nil, fmt.Errorf("unsupported Responses input item type")
		}
	}
	for id := range calls {
		if !results[id] {
			return nil, fmt.Errorf("function_call is missing its function_call_output in input")
		}
	}
	return messages, nil
}

func responsesContent(value any) (any, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("message content/tool output must be a string or an array")
	}
	out := make([]any, 0, len(parts))
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("content part must be an object")
		}
		switch part["type"] {
		case "input_text", "output_text":
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("text content must be a string")
			}
			out = append(out, map[string]any{"type": "text", "text": text})
		case "input_image":
			url, _ := part["image_url"].(string)
			if url == "" {
				return nil, fmt.Errorf("input_image requires image_url; file_id is not supported")
			}
			image := map[string]any{"url": url}
			if detail := part["detail"]; detail != nil {
				if detail != "auto" && detail != "high" && detail != "low" {
					return nil, fmt.Errorf("invalid image detail")
				}
				image["detail"] = detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": image})
		case "refusal":
			text, ok := part["refusal"].(string)
			if !ok {
				return nil, fmt.Errorf("refusal must be a string")
			}
			out = append(out, map[string]any{"type": "text", "text": text})
		default:
			return nil, fmt.Errorf("unsupported Responses content type")
		}
	}
	return out, nil
}
