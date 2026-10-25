import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { AlertTriangle, ArrowRight, CheckCircle2 } from 'lucide-react'
import { api } from '../api'
import type { Account, Overview } from '../types'
import type { TimeRange } from '../telemetry'
import { errorLabel } from '../telemetry'
import { DataState, Metric, PageHeader, RangeControl, RefreshButton, Section, TrendChart, duration, number, percent, useResource } from '../console'
import { Badge, displayName, fmtDuration, fmtISO, statusBadge } from '../ui'

export default function Dashboard() {
  const [range, setRange] = useState<TimeRange>('24h')
  const load = useCallback(async () => {
    const [overview, accountResult, summary, recent] = await Promise.all([
      api.overview(), api.accounts(), api.telemetrySummary(range), api.requests({ status: 'error', limit: '6' }),
    ])
    return { overview, accounts: accountResult.accounts ?? [], summary, recent: recent.items ?? [] }
  }, [range])
  const { data, error, loading, updated, reload } = useResource(load, 20_000)
  const problems = useMemo(() => (data?.accounts ?? []).filter(a => a.status !== 'healthy'), [data])
  return <>
    <PageHeader title="运行总览" subtitle={updated ? `账号池、请求质量与成本 · 更新于 ${updated.toLocaleTimeString('zh-CN', { hour12: false })}` : '账号池、请求质量与成本'}>
      <RangeControl value={range} onChange={setRange} /><RefreshButton loading={loading} onClick={() => void reload()} />
    </PageHeader>
    <DataState error={error} loading={loading && !data} onRetry={() => void reload()}>
      {data && <DashboardContent {...data} problems={problems} />}
    </DataState>
  </>
}

function DashboardContent({ overview: ov, accounts, summary: s, recent, problems }: { overview: Overview; accounts: Account[]; summary: Awaited<ReturnType<typeof api.telemetrySummary>>; recent: Awaited<ReturnType<typeof api.requests>>['items']; problems: Account[] }) {
  return <>
    {!ov.gateway_ok && <div className="critical-banner"><AlertTriangle size={20} /><div><strong>网关连接异常</strong><span>{ov.gateway_error || ov.gateway_url}</span></div></div>}
    {ov.warnings?.map(w => <div className="inline-notice" key={w}><AlertTriangle size={16} />{w}</div>)}
    <div className="metrics-row">
      <Metric label="成功率" value={s.enabled ? percent(s.success_rate) : '未启用'} detail={s.enabled ? `${number(s.total)} 次请求` : '开启 telemetry 后记录'} tone={s.success_rate != null && s.success_rate < .95 ? 'danger' : 'ok'} />
      <Metric label="P95 首字" value={duration(s.p95_ttfb_ms)} detail="越低越好" />
      <Metric label="P95 总耗时" value={duration(s.p95_latency_ms)} detail="端到端" />
      <Metric label="可用账号" value={ov.gateway_ok ? `${ov.healthy} / ${ov.total}` : '—'} detail={ov.gateway_ok ? `${ov.cooling} 冷却 · ${ov.disabled} 禁用` : '网关不可达'} tone={!ov.gateway_ok || ov.healthy === 0 ? 'danger' : ov.healthy < ov.total ? 'warn' : 'ok'} />
      <Metric label="积分消耗" value={s.enabled ? number(s.credit) : '—'} detail={s.enabled ? `${number(s.prompt_tokens + s.completion_tokens)} tokens` : '暂无观测'} />
    </div>
    <div className="dashboard-columns">
      <Section title="请求趋势" action={<Link className="section-link" to="/stats">查看分析 <ArrowRight size={14} /></Link>}><TrendChart summary={s} /></Section>
      <Section title="异常分布" action={<Link className="section-link" to="/requests?status=error">查看日志 <ArrowRight size={14} /></Link>}>
        {!s.errors.length ? <div className="positive-state"><CheckCircle2 size={24} /><strong>当前时段没有失败</strong></div> : <div className="error-bars">{s.errors.slice(0, 6).map(e => <div key={e.error_type}><span>{errorLabel(e.error_type)}</span><div><i style={{ width: `${Math.max(4, e.count / Math.max(...s.errors.map(v => v.count)) * 100)}%` }} /></div><strong>{e.count}</strong></div>)}</div>}
      </Section>
    </div>
    <div className="dashboard-columns">
      <Section title="账号健康" action={<Link className="section-link" to="/accounts">管理账号池 <ArrowRight size={14} /></Link>}>
        {!accounts.length ? <DataState empty="尚未添加账号" /> : <div className="account-matrix">{accounts.slice(0, 12).map(a => { const badge = statusBadge(a.status); return <Link to="/accounts" key={a.uid} className="account-health"><span className={`health-dot ${badge.cls}`} /><div><strong>{displayName(a)}</strong><small>{a.cooling ? `冷却剩余 ${fmtDuration(a.cool_remaining_sec)}` : a.reason || `成功 ${a.success_count} · 失败 ${a.err_total}`}</small></div><Badge cls={badge.cls}>{badge.text}</Badge></Link> })}</div>}
        {problems.length > 0 && <div className="section-foot text-warn">{problems.length} 个账号需要处理</div>}
      </Section>
      <Section title="最近失败" action={<Link className="section-link" to="/requests">全部请求 <ArrowRight size={14} /></Link>}>
        {!recent.length ? <div className="positive-state"><CheckCircle2 size={24} /><strong>没有最近失败记录</strong></div> : <div className="recent-list">{recent.map(e => <Link key={e.request_id} to={`/requests?id=${encodeURIComponent(e.request_id)}`}><span className="status-mark error" /><div><strong>{e.model}</strong><small>{errorLabel(e.error_type)} · {fmtISO(e.started_at)}</small></div><span>{duration(e.latency_ms)}</span></Link>)}</div>}
      </Section>
    </div>
    <Section title="数据留存"><div className="storage-line"><span>请求摘要</span><strong>{number(s.storage.events)}</strong><span>保留 {s.storage.retention_days} 天，最多 {number(s.storage.max_events)} 条</span>{s.storage.write_errors > 0 && <span className="text-danger">{s.storage.write_errors} 次写入失败</span>}</div></Section>
  </>
}
