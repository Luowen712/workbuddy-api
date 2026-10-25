import { useCallback } from 'react'
import { Boxes, CircleDollarSign } from 'lucide-react'
import { api } from '../api'
import type { SessionInfo } from '../types'
import { DataState, Metric, PageHeader, RefreshButton, Section, number, useResource } from '../console'
import { Badge, fmtNum } from '../ui'

export default function ModelsPage({ session: _session }: { session: SessionInfo }) {
  const load = useCallback(async () => { const [models, stats] = await Promise.all([api.models(), api.stats('peak')]); return { models: models.data ?? [], stats } }, [])
  const { data, error, loading, reload } = useResource(load)
  return <><PageHeader title="模型与价格" subtitle="模型目录、性能与官方价格换算"><RefreshButton loading={loading} onClick={() => void reload()} /></PageHeader><DataState error={error} loading={loading && !data} onRetry={() => void reload()}>{data && <><div className="metrics-row"><Metric label="已发现模型" value={data.models.length} detail="来自网关模型目录" /><Metric label="已观测请求" value={number(data.stats.stats.total.requests)} detail="累计统计" /><Metric label="累计 Token" value={number(data.stats.stats.total.total_tokens)} detail="输入 + 输出" /><Metric label="官方价" value={data.stats.pricing.editable ? '可编辑' : '只读'} detail={data.stats.pricing.updated_at ? `更新于 ${data.stats.pricing.updated_at}` : '本地价格表'} /></div><Section title="模型目录"><div className="model-grid">{data.models.map(m => { const stat = data.stats.stats.models?.find(s => s.model === m.id); const price = data.stats.pricing.models?.[m.id]; return <div className="model-card" key={m.id}><div className="model-title"><Boxes size={17} /><strong>{m.id}</strong><Badge cls={stat?.failed ? 'badge-warn' : 'badge-ok'}>{stat ? `${stat.requests} 次请求` : '未观测'}</Badge></div><div className="model-meta">{m.owned_by || 'workbuddy'} · 上下文 {fmtNum(m.context_length)}</div><div className="model-stats"><span>成功率 {stat ? `${((stat.success / Math.max(1, stat.requests)) * 100).toFixed(1)}%` : '—'}</span><span>首字 {stat?.avg_ttfb_ms ? `${Math.round(stat.avg_ttfb_ms)} ms` : '—'}</span><span>{price ? <><CircleDollarSign size={14} />已配置价格</> : '待配置价格'}</span></div></div> })}</div>{!data.models.length && <DataState empty="网关没有返回模型" />}</Section></>}</DataState></>
}
