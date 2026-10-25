# Responses Compatibility

`POST /v1/responses` uses the same API key, model names, account routing and
telemetry as `/v1/chat/completions`. Existing Chat Completions behavior is
unchanged. Client Base URL remains `http://localhost:7863/v1`.

Supported:

- String input, instructions and full message history.
- Text and image-URL input (actual vision support depends on the selected model).
- Function tool definitions, choices, parallel calls and function result replay.
- Non-streaming Response objects and incremental named Responses SSE events.
- Reasoning text, usage, incomplete output and stream failures.
- JSON output formats forwarded to the upstream model.

Example request:

```json
{
  "model": "cn:deepseek-v4.1-flash",
  "input": "Reply with OK only.",
  "stream": true,
  "store": false,
  "max_output_tokens": 256
}
```

This adapter is stateless. Supply the complete conversation in `input`,
including `function_call` and matching `function_call_output` items.
The endpoint does not execute tools; the client executes its own tools.

Unsupported requests return HTTP 400 instead of silently changing semantics:
response storage/retrieval, `previous_response_id`, `conversation`, background
execution, hosted tools, file IDs, item references and encrypted reasoning
replay. Omitted `store` is treated as false. Requesting encrypted reasoning via
`include` is accepted for client compatibility, but no encrypted data is
fabricated. Reasoning text is not advertised as an upstream-generated summary.

HTTP errors before streaming use the existing API error envelope. Errors after
streaming starts end with `response.failed`. The stream ends with a Responses
terminal event, not a Chat Completions `[DONE]` marker.

Protocol regression tests:

```sh
go test ./internal/server -run TestResponses -count=1
go test ./...
```
