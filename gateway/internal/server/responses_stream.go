package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"workbuddy2api/internal/session"
	"workbuddy2api/internal/upstream"
)

type responseItem struct {
	value map[string]any
	index int
	added bool
	text  strings.Builder
}

type responsesCollector struct {
	response map[string]any
	items    []*responseItem
	texts    map[string]*responseItem
	tools    map[int]*responseItem
	lastTool int
	finish   string
	writer   http.ResponseWriter
	sequence int
	writeErr error
}

// Both output modes consume the same upstream stream and share validation.
func collectResponses(r io.Reader, options *responsesOptions, w http.ResponseWriter) (map[string]any, error) {
	c := &responsesCollector{
		response: map[string]any{
			"id": "resp_" + session.NewMessageID(), "object": "response",
			"created_at": time.Now().Unix(), "status": "in_progress",
			"error": nil, "incomplete_details": nil, "usage": nil,
			"model": options.model, "store": false, "background": false,
			"output": []any{}, "output_text": "",
		},
		texts: map[string]*responseItem{}, tools: map[int]*responseItem{},
		lastTool: -1, writer: w,
	}
	for k, v := range options.fields {
		c.response[k] = v
	}
	if w != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		c.emit("response.created", map[string]any{"response": c.response})
		c.emit("response.in_progress", map[string]any{"response": c.response})
	}
	err := c.read(r)
	if err == nil {
		err = c.complete()
	}
	if err != nil {
		c.response["status"] = "failed"
		var providerErr *responsesProviderError
		if errors.As(err, &providerErr) {
			c.response["error"] = providerErr.diagnostic.fields()
		} else {
			c.response["error"] = map[string]any{"code": "server_error", "message": err.Error()}
		}
		c.snapshot()
		c.emit("response.failed", map[string]any{"response": c.response})
	}
	if c.writeErr != nil {
		return nil, c.writeErr
	}
	return c.response, err
}

type responsesProviderError struct {
	diagnostic providerErrorDiagnostic
	kind       upstream.ErrKind
}

func (e *responsesProviderError) Error() string { return e.diagnostic.Message }

func (c *responsesCollector) emit(kind string, fields map[string]any) {
	if c.writer == nil || c.writeErr != nil {
		return
	}
	fields["type"], fields["sequence_number"] = kind, c.sequence
	c.sequence++
	data, err := json.Marshal(fields)
	if err == nil {
		_, err = fmt.Fprintf(c.writer, "event: %s\ndata: %s\n\n", kind, data)
	}
	c.writeErr = err
	if err == nil {
		if f, ok := c.writer.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (c *responsesCollector) read(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var data strings.Builder
	total := 0
	done := false
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		frame := strings.TrimSpace(data.String())
		data.Reset()
		if frame == "[DONE]" {
			done = true
			return nil
		}
		return c.chunk([]byte(frame))
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line)
		if total > 32<<20 {
			return fmt.Errorf("upstream response exceeds adapter size limit")
		}
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			if c.writeErr != nil {
				return c.writeErr
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("upstream stream read failed")
	}
	if err := dispatch(); err != nil {
		return err
	}
	if c.finish == "" {
		return fmt.Errorf("upstream stream ended without a finish reason")
	}
	return nil
}

func (c *responsesCollector) chunk(data []byte) error {
	type delta struct {
		Content   string `json:"content"`
		Reasoning string `json:"reasoning_content"`
		Refusal   string `json:"refusal"`
		Tools     []struct {
			Index    *int   `json:"index"`
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	var chunk struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Index   int    `json:"index"`
			Delta   delta  `json:"delta"`
			Message delta  `json:"message"`
			Finish  string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("invalid upstream stream JSON")
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		diagnostic, ok := parseProviderErrorDiagnostic(data, "upstream_error", "server_error", "upstream returned an error event")
		if !ok {
			return fmt.Errorf("upstream returned an error event")
		}
		kind := upstream.Classify(http.StatusBadRequest, string(data))
		if kind == upstream.ErrOutputTooLong {
			diagnostic.Type = "output_too_long"
		}
		return &responsesProviderError{diagnostic: diagnostic, kind: kind}
	}
	if chunk.Usage != nil {
		u := chunk.Usage
		details := func(key, field string) map[string]any {
			value := any(0)
			if d, ok := u[key].(map[string]any); ok && d[field] != nil {
				value = d[field]
			}
			return map[string]any{field: value}
		}
		c.response["usage"] = map[string]any{
			"input_tokens": u["prompt_tokens"], "output_tokens": u["completion_tokens"], "total_tokens": u["total_tokens"],
			"input_tokens_details":  details("prompt_tokens_details", "cached_tokens"),
			"output_tokens_details": details("completion_tokens_details", "reasoning_tokens"),
		}
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return fmt.Errorf("multiple upstream choices are not supported")
		}
		d := choice.Delta
		// Some upstreams send a complete message, including alongside final deltas.
		// Only fill fields that have never streamed, avoiding duplicated output.
		if d.Content == "" && c.texts["output_text"] == nil {
			d.Content = choice.Message.Content
		}
		if d.Reasoning == "" && c.texts["reasoning_text"] == nil {
			d.Reasoning = choice.Message.Reasoning
		}
		if d.Refusal == "" && c.texts["refusal"] == nil {
			d.Refusal = choice.Message.Refusal
		}
		if len(d.Tools) == 0 && len(c.tools) == 0 {
			d.Tools = choice.Message.Tools
		}
		if c.finish != "" && (d.Content != "" || d.Reasoning != "" || d.Refusal != "" || len(d.Tools) > 0) {
			return fmt.Errorf("upstream content received after finish")
		}
		for _, part := range []struct{ kind, text string }{
			{"reasoning_text", d.Reasoning}, {"output_text", d.Content}, {"refusal", d.Refusal},
		} {
			if part.text != "" {
				if err := c.textDelta(part.kind, part.text); err != nil {
					return err
				}
			}
		}
		for _, call := range d.Tools {
			index := c.lastTool
			if call.Index != nil {
				index = *call.Index
			} else if call.ID != "" {
				index = -1
				for i, item := range c.tools {
					if item.value["call_id"] == call.ID {
						index = i
						break
					}
				}
				if index < 0 {
					index = 0
					for c.tools[index] != nil {
						index++
					}
				}
			}
			if index < 0 || index > 1023 || (call.Type != "" && call.Type != "function") {
				return fmt.Errorf("invalid upstream function call")
			}
			c.lastTool = index
			item := c.tools[index]
			if item == nil {
				var err error
				item, err = c.newItem(map[string]any{
					"id": "fc_" + session.NewMessageID(), "type": "function_call",
					"status": "in_progress", "call_id": "", "name": "", "arguments": "",
				})
				if err != nil {
					return err
				}
				c.tools[index] = item
			}
			if call.ID != "" {
				if old := item.value["call_id"].(string); old != "" && old != call.ID {
					return fmt.Errorf("upstream function call ID changed")
				}
				item.value["call_id"] = call.ID
			}
			if name := call.Function.Name; name != "" && name != item.value["name"] {
				if item.added {
					return fmt.Errorf("upstream function name changed after arguments")
				}
				item.value["name"] = item.value["name"].(string) + name
			}
			item.text.WriteString(call.Function.Arguments)
			// Delay the added event until identity is known; clients capture it once.
			if !item.added && item.value["name"] != "" && item.value["call_id"] != "" && item.text.Len() > 0 {
				c.added(item)
				c.argumentsDelta(item, item.text.String())
			} else if item.added && call.Function.Arguments != "" {
				c.argumentsDelta(item, call.Function.Arguments)
			}
		}
		if choice.Finish != "" {
			if c.finish != "" && c.finish != choice.Finish {
				return fmt.Errorf("conflicting upstream finish reasons")
			}
			c.finish = choice.Finish
		}
	}
	return nil
}

func (c *responsesCollector) newItem(value map[string]any) (*responseItem, error) {
	if len(c.items) >= 1024 {
		return nil, fmt.Errorf("too many upstream output items")
	}
	item := &responseItem{value: value, index: len(c.items)}
	c.items = append(c.items, item)
	return item, nil
}

func (c *responsesCollector) added(item *responseItem) {
	item.added = true
	c.emit("response.output_item.added", map[string]any{"output_index": item.index, "item": item.value})
}

func (c *responsesCollector) textDelta(kind, text string) error {
	item := c.texts[kind]
	if item == nil {
		value := map[string]any{"id": "msg_" + session.NewMessageID(), "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
		if kind == "reasoning_text" {
			value = map[string]any{"id": "rs_" + session.NewMessageID(), "type": "reasoning", "status": "in_progress", "summary": []any{}, "content": []any{}}
		}
		var err error
		item, err = c.newItem(value)
		if err != nil {
			return err
		}
		c.texts[kind] = item
		c.added(item)
		if kind != "reasoning_text" {
			c.emit("response.content_part.added", map[string]any{"item_id": value["id"], "output_index": item.index, "content_index": 0, "part": responsePart(kind, "")})
		}
	}
	item.text.WriteString(text)
	c.emit("response."+kind+".delta", map[string]any{
		"item_id": item.value["id"], "output_index": item.index, "content_index": 0, "delta": text,
	})
	return nil
}

func responsePart(kind, text string) map[string]any {
	if kind == "refusal" {
		return map[string]any{"type": kind, "refusal": text}
	}
	part := map[string]any{"type": kind, "text": text}
	if kind == "output_text" {
		part["annotations"], part["logprobs"] = []any{}, []any{}
	}
	return part
}

func (c *responsesCollector) argumentsDelta(item *responseItem, delta string) {
	c.emit("response.function_call_arguments.delta", map[string]any{
		"item_id": item.value["id"], "output_index": item.index, "delta": delta,
	})
}

func (c *responsesCollector) snapshot() {
	output := make([]any, 0, len(c.items))
	for _, item := range c.items {
		output = append(output, item.value)
	}
	c.response["output"] = output
	if text := c.texts["output_text"]; text != nil {
		c.response["output_text"] = text.text.String()
	}
}

func (c *responsesCollector) complete() error {
	status, reason := "completed", ""
	switch c.finish {
	case "stop", "tool_calls", "function_call":
	case "length":
		status, reason = "incomplete", "max_output_tokens"
	case "content_filter":
		status, reason = "incomplete", "content_filter"
	default:
		return fmt.Errorf("unsupported upstream finish reason")
	}
	ids := map[string]bool{}
	// Validate all calls before emitting any terminal item events.
	for _, item := range c.tools {
		if item.value["name"] == "" {
			return fmt.Errorf("upstream function call is missing its name")
		}
		if status == "completed" {
			var args map[string]any
			if json.Unmarshal([]byte(item.text.String()), &args) != nil || args == nil {
				return fmt.Errorf("upstream function call arguments are not a JSON object")
			}
		}
		id := item.value["call_id"].(string)
		if id == "" {
			id = "call_" + session.NewMessageID()
			item.value["call_id"] = id
		}
		if ids[id] {
			return fmt.Errorf("duplicate upstream function call ID")
		}
		ids[id] = true
	}
	for _, item := range c.items {
		if item.value["type"] == "function_call" {
			if !item.added {
				c.added(item)
				c.argumentsDelta(item, item.text.String())
			}
			item.value["arguments"] = item.text.String()
			c.emit("response.function_call_arguments.done", map[string]any{
				"item_id": item.value["id"], "output_index": item.index, "name": item.value["name"], "arguments": item.text.String(),
			})
		} else {
			kind := "output_text"
			if item == c.texts["reasoning_text"] {
				kind = "reasoning_text"
			}
			if item == c.texts["refusal"] {
				kind = "refusal"
			}
			part := responsePart(kind, item.text.String())
			item.value["content"] = []any{part}
			done := map[string]any{"item_id": item.value["id"], "output_index": item.index, "content_index": 0, "text": item.text.String()}
			if kind == "refusal" {
				delete(done, "text")
				done["refusal"] = item.text.String()
			}
			c.emit("response."+kind+".done", done)
			if kind != "reasoning_text" {
				c.emit("response.content_part.done", map[string]any{"item_id": item.value["id"], "output_index": item.index, "content_index": 0, "part": part})
			}
		}
		item.value["status"] = status
		c.emit("response.output_item.done", map[string]any{"output_index": item.index, "item": item.value})
	}
	c.response["status"] = status
	if reason != "" {
		c.response["incomplete_details"] = map[string]any{"reason": reason}
	}
	c.snapshot()
	c.emit("response."+status, map[string]any{"response": c.response})
	return c.writeErr
}
