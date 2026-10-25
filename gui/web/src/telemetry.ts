export interface RequestEvent {
  request_id: string
  started_at: string
  model: string
  account_uid: string
  stream: boolean
  status: number
  error_type: string
  retry_count: number
  ttfb_ms: number | null
  latency_ms: number | null
  prompt_tokens: number | null
  completion_tokens: number | null
  cache_hit_tokens: number | null
  cache_miss_tokens: number | null
  credit: number | null
  usage_known: boolean
  credit_known: boolean
  attempts: { account_uid: string; status: number; error_type: string }[]
}
export interface RequestList { enabled: boolean; items: RequestEvent[]; next_cursor: string; total: number }
export interface ModelSummary {
  model: string; requests: number; success: number; failed: number; credit: number
  prompt_tokens: number; completion_tokens: number; p95_ttfb_ms: number | null; p95_latency_ms: number | null
}
export interface TelemetrySummary {
  enabled: boolean; from: string; to: string; total: number; success: number; failed: number; success_rate: number | null
  p95_ttfb_ms: number | null; p95_latency_ms: number | null
  prompt_tokens: number; completion_tokens: number; cache_hit_tokens: number; cache_miss_tokens: number; credit: number
  series: { time: string; requests: number; success: number; failed: number; prompt_tokens: number; completion_tokens: number; credit: number }[]
  models: ModelSummary[]
  accounts: { account_uid: string; requests: number; success: number; failed: number; last_seen: string }[]
  errors: { error_type: string; count: number }[]
  storage: { retention_days: number; max_events: number; events: number; dropped: number; write_errors: number }
}
export type TimeRange = '1h' | '24h' | '7d'
export const rangeLabels: Record<TimeRange, string> = { '1h': '最近 1 小时', '24h': '最近 24 小时', '7d': '最近 7 天' }
export const errorLabels: Record<string, string> = {
  invalid_request: '请求无效', unauthorized: '鉴权失败', no_account: '无可用账号', no_accounts: '无可用账号',
  soft_rate: '上游限流', hard_credit: '积分不足', upstream_error: '上游错误', timeout: '请求超时',
  cancelled: '客户端取消', canceled: '客户端取消', stream_error: '流中断', account_fault: '账号异常',
  session_dead: '会话失效', internal_error: '内部错误', unknown_model: '模型不可用',
}
export const errorLabel = (kind?: string) => kind ? errorLabels[kind] || kind : '—'
export const eventOK = (e: RequestEvent) => e.status >= 200 && e.status < 300 && !e.error_type
