import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { AlertCircle, Inbox, RefreshCw, Check, Copy, SunMoon } from 'lucide-react'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { TelemetrySummary, TimeRange } from './telemetry'
import { rangeLabels } from './telemetry'

export function requestQuery(filters: Record<string, string | undefined>): string {
  return new URLSearchParams(Object.entries(filters).filter((pair): pair is [string, string] => !!pair[1])).toString()
}
export const duration = (ms?: number | null) => ms == null ? '—' : ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${Math.round(ms)} ms`
export const number = (n?: number | null) => n == null ? '—' : new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(n)
export const percent = (n?: number | null) => n == null ? '—' : `${(n * 100).toFixed(1)}%`

export function useResource<T>(loader: () => Promise<T>, interval = 0) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [updated, setUpdated] = useState<Date | null>(null)
  const version = useRef(0)
  const reload = useCallback(async () => {
    const run = ++version.current
    setLoading(true)
    try { const value = await loader(); if (run === version.current) { setData(value); setUpdated(new Date()); setError('') } }
    catch (err) { if (run === version.current) setError(err instanceof Error ? err.message : '读取失败') }
    finally { if (run === version.current) setLoading(false) }
  }, [loader])
  useEffect(() => { setData(null); void reload(); const id = interval ? setInterval(() => { if (!document.hidden) void reload() }, interval) : undefined; return () => { version.current++; clearInterval(id) } }, [reload, interval])
  return { data, error, loading, updated, reload }
}

export function ThemeControl() {
  const [theme, setTheme] = useState(() => { const stored = localStorage.getItem('wb-theme'); return stored === 'dark' || stored === 'light' ? stored : 'system' })
  useEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => { document.documentElement.dataset.theme = theme === 'system' ? media.matches ? 'dark' : 'light' : theme }
    apply(); localStorage.setItem('wb-theme', theme); media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [theme])
  return <label className="theme-control"><SunMoon size={16} /><select aria-label="外观主题" value={theme} onChange={e => setTheme(e.target.value)}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></label>
}
export function PageHeader({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children?: ReactNode }) {
  return <div className="page-head"><div><h1>{title}</h1>{subtitle && <p>{subtitle}</p>}</div><div className="page-actions">{children}</div></div>
}
export function RefreshButton({ loading, onClick }: { loading?: boolean; onClick: () => void }) {
  return <button className="btn icon-button" title="刷新数据" aria-label="刷新数据" disabled={loading} onClick={onClick}><RefreshCw size={16} className={loading ? 'spin' : ''} /></button>
}
export function RangeControl({ value, onChange }: { value: TimeRange; onChange: (v: TimeRange) => void }) {
  return <select className="range-select" aria-label="统计时间范围" value={value} onChange={e => onChange(e.target.value as TimeRange)}>{Object.entries(rangeLabels).map(([v, label]) => <option key={v} value={v}>{label}</option>)}</select>
}
export function Metric({ label, value, detail, tone }: { label: string; value: ReactNode; detail?: ReactNode; tone?: string }) {
  return <div className="metric"><div className="stat-label">{label}</div><div className={`metric-value ${tone ? `text-${tone}` : ''}`}>{value}</div><div className="metric-detail">{detail || '\u00a0'}</div></div>
}
export function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <section className="section"><div className="section-head"><h2>{title}</h2>{action}</div>{children}</section>
}
export function DataState({ error, loading, empty, onRetry, children }: { error?: string; loading?: boolean; empty?: string; onRetry?: () => void; children?: ReactNode }) {
  if (error) return <div className="data-state error-state" role="alert"><AlertCircle size={24} /><strong>暂时无法读取数据</strong><p>{error}</p>{onRetry && <button className="btn" onClick={onRetry}>重试</button>}</div>
  if (loading) return <div className="skeleton-block" role="status" aria-label="正在加载"><span /><span /><span /></div>
  if (empty) return <div className="data-state"><Inbox size={26} /><strong>{empty}</strong></div>
  return <>{children}</>
}
export function CopyButton({ value, label = '复制' }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  useEffect(() => { if (!copied) return; const t = setTimeout(() => setCopied(false), 1800); return () => clearTimeout(t) }, [copied])
  return <button className="btn icon-button" title={failed ? '复制失败，请手动选择' : copied ? '已复制' : label} aria-label={copied ? '已复制' : label} onClick={() => { void navigator.clipboard.writeText(value).then(() => { setCopied(true); setFailed(false) }).catch(() => setFailed(true)) }}>{copied ? <Check size={15} /> : <Copy size={15} />}</button>
}
export function TrendChart({ summary }: { summary: TelemetrySummary }) {
  if (!summary.enabled) return <DataState empty="请求记录未启用" />
  if (!summary.total) return <DataState empty="此时段暂无请求" />
  return <div className="trend-chart" role="img" aria-label={`请求趋势，共 ${summary.total} 次，失败 ${summary.failed} 次`}><ResponsiveContainer width="100%" height="100%"><AreaChart data={summary.series} margin={{ left: 0, top: 10, right: 12, bottom: 0 }}><CartesianGrid vertical={false} stroke="var(--border-soft)" /><XAxis dataKey="time" tickFormatter={v => new Date(v).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })} minTickGap={55} tick={{ fontSize: 11, fill: 'var(--text-dim)' }} axisLine={false} tickLine={false} /><YAxis allowDecimals={false} width={36} tick={{ fontSize: 11, fill: 'var(--text-dim)' }} axisLine={false} tickLine={false} /><Tooltip contentStyle={{ background: 'var(--bg-elev)', border: '1px solid var(--border)', borderRadius: 6 }} labelFormatter={v => new Date(String(v)).toLocaleString('zh-CN')} /><Area isAnimationActive={false} name="请求" type="monotone" dataKey="requests" stroke="var(--chart-blue)" fill="var(--chart-blue)" fillOpacity={0.08} strokeWidth={2} /><Area isAnimationActive={false} name="失败" type="monotone" dataKey="failed" stroke="var(--danger)" fill="var(--danger)" fillOpacity={0.08} strokeWidth={2} /></AreaChart></ResponsiveContainer></div>
}
