import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Filter, Search, X } from 'lucide-react'
import { api } from '../api'
import type { RequestEvent, RequestList } from '../telemetry'
import { errorLabel, eventOK } from '../telemetry'
import { DataState, PageHeader, RefreshButton, Section, duration, number, useResource } from '../console'
import { Badge, Modal, fmtISO, Spinner } from '../ui'

export default function RequestLogs() {
  const [searchParams] = useSearchParams()
  const [model, setModel] = useState(''); const [account, setAccount] = useState(''); const [status, setStatus] = useState(''); const [stream, setStream] = useState(''); const [cursor, setCursor] = useState(''); const [selected, setSelected] = useState<string | null>(null)
  const load = useCallback(() => api.requests({ model: model || undefined, account_uid: account || undefined, status: status || undefined, stream: stream || undefined, cursor: cursor || undefined, limit: '50' }), [model, account, status, stream, cursor])
  const { data, error, loading, reload } = useResource<RequestList>(load)
  const detail = useResource<RequestEvent | null>(useCallback(async () => selected ? api.requestDetail(selected) : null, [selected]))
  useEffect(() => { setSelected(searchParams.get('id')) }, [searchParams])
  return <>
    <PageHeader title="请求日志" subtitle="脱敏请求摘要 · 不保存提示词和回答正文"><RefreshButton loading={loading} onClick={() => void reload()} /></PageHeader>
    <Section title="筛选" action={<button className="btn btn-ghost btn-sm" onClick={() => { setModel(''); setAccount(''); setStatus(''); setStream(''); setCursor('') }}><X size={14} />清空</button>}>
      <div className="filter-grid"><label>模型<input value={model} onChange={e => { setModel(e.target.value); setCursor('') }} placeholder="模型 ID" /></label><label>账号 UID<input value={account} onChange={e => { setAccount(e.target.value); setCursor('') }} placeholder="账号前缀" /></label><label>结果<select value={status} onChange={e => { setStatus(e.target.value); setCursor('') }}><option value="">全部结果</option><option value="success">成功</option><option value="error">失败</option></select></label><label>类型<select value={stream} onChange={e => { setStream(e.target.value); setCursor('') }}><option value="">全部类型</option><option value="true">流式</option><option value="false">非流式</option></select></label><button className="btn btn-primary filter-submit" onClick={() => void reload()}><Search size={15} />应用筛选</button></div>
    </Section>
    <DataState error={error} loading={loading && !data} empty={data && !data.items.length ? '没有匹配的请求记录' : undefined} onRetry={() => void reload()}>
      {data && <Section title={`请求记录 · ${number(data.total)}`}><RequestTable data={data} onSelect={setSelected} onNext={() => setCursor(data.next_cursor)} /></Section>}
    </DataState>
    {selected && <RequestDetail event={detail.data} error={detail.error} loading={detail.loading} onClose={() => setSelected(null)} />}
  </>
}
function RequestTable({ data, onSelect, onNext }: { data: RequestList; onSelect: (id: string) => void; onNext: () => void }) {
  return <><div className="table-wrap"><table className="data-table"><thead><tr><th>时间</th><th>模型</th><th>账号</th><th>结果</th><th>类型</th><th className="num">TTFB</th><th className="num">耗时</th><th className="num">Token</th></tr></thead><tbody>{data.items.map(e => { const ok = eventOK(e); return <tr key={e.request_id} className="clickable-row" onClick={() => onSelect(e.request_id)}><td className="text-dim">{fmtISO(e.started_at)}</td><td><strong>{e.model}</strong><small className="mono table-sub">{e.request_id.slice(0, 12)}</small></td><td className="mono">{e.account_uid || '—'}</td><td><Badge cls={ok ? 'badge-ok' : 'badge-danger'}>{ok ? '成功' : errorLabel(e.error_type)}</Badge></td><td>{e.stream ? '流式' : '非流式'}</td><td className="num">{duration(e.ttfb_ms)}</td><td className="num">{duration(e.latency_ms)}</td><td className="num">{e.usage_known ? number((e.prompt_tokens || 0) + (e.completion_tokens || 0)) : '—'}</td></tr> })}</tbody></table></div>{data.next_cursor && <div className="pagination"><button className="btn" onClick={onNext}>加载更早记录</button></div>}</>
}
function RequestDetail({ event, error, loading, onClose }: { event: RequestEvent | null; error: string; loading: boolean; onClose: () => void }) {
  return <Modal title="请求详情" onClose={onClose} drawer wide>{loading && <Spinner label="正在加载详情…" />}{error && <div className="error-state"><Filter size={20} />{error}</div>}{event && <div className="detail-grid"><div><span>请求 ID</span><strong className="mono">{event.request_id}</strong></div><div><span>模型</span><strong>{event.model}</strong></div><div><span>账号</span><strong className="mono">{event.account_uid || '—'}</strong></div><div><span>时间</span><strong>{fmtISO(event.started_at)}</strong></div><div><span>结果</span><strong>{eventOK(event) ? '成功' : errorLabel(event.error_type)}</strong></div><div><span>重试次数</span><strong>{event.retry_count}</strong></div><div><span>首字延迟</span><strong>{duration(event.ttfb_ms)}</strong></div><div><span>总耗时</span><strong>{duration(event.latency_ms)}</strong></div><div><span>输入 Token</span><strong>{event.usage_known ? number(event.prompt_tokens) : '—'}</strong></div><div><span>输出 Token</span><strong>{event.usage_known ? number(event.completion_tokens) : '—'}</strong></div></div>}{event?.attempts?.length ? <div className="attempts"><h3>选号与重试</h3>{event.attempts.map((a, i) => <div key={i}><span>#{i + 1}</span><code>{a.account_uid}</code><Badge cls={a.error_type ? 'badge-danger' : 'badge-ok'}>{a.error_type ? errorLabel(a.error_type) : `${a.status} 成功`}</Badge></div>)}</div> : null}</Modal>
}
