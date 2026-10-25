import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Check, Copy, Eye, EyeOff, KeyRound, RefreshCw, RotateCw } from 'lucide-react'
import { api } from '../api'
import type { AccessInfo, SessionInfo } from '../types'
import { Alert, ConfirmDialog, Spinner } from '../ui'

// Docker's host alias is for server-to-server traffic, never a client address.
export function clientBaseURL(gatewayURL: string, browserURL: string): string {
  const url = new URL(gatewayURL)
  if (['host.docker.internal', 'gateway', 'wb2api', 'workbuddy2api', '0.0.0.0', 'localhost', '127.0.0.1', '[::1]'].includes(url.hostname)) {
    url.hostname = new URL(browserURL).hostname
  }
  url.username = ''
  url.password = ''
  url.search = ''
  url.hash = ''
  url.pathname = `${url.pathname.replace(/\/(v1\/?)?$/, '')}/v1`
  return url.toString().replace(/\/$/, '')
}

type ExampleMode = 'powershell' | 'curl' | 'python' | 'javascript'
function exampleFor(mode: ExampleMode, base: string, model: string) {
  if (mode === 'powershell') return `$body = @{
    model = '${model.replace(/'/g, "''")}'
    messages = @(@{ role = 'user'; content = 'Hi' })
    max_tokens = 256
} | ConvertTo-Json -Depth 5

$headers = @{
    Authorization = "Bearer $env:WORKBUDDY_API_KEY"
}
Invoke-RestMethod -Method Post \`
    -Uri '${base.replace(/'/g, "''")}/chat/completions' \`
    -Headers $headers \`
    -ContentType 'application/json' \`
    -Body $body`
  if (mode === 'python') return `import os
import json
from urllib.request import Request, urlopen

request = Request(
    ${JSON.stringify(base + '/chat/completions')},
    data=json.dumps({
        "model": ${JSON.stringify(model)},
        "messages": [{"role": "user", "content": "Hi"}],
        "max_tokens": 256
    }).encode(),
    headers={
        "Authorization": "Bearer " + os.environ["WORKBUDDY_API_KEY"],
        "Content-Type": "application/json"
    },
)
with urlopen(request, timeout=120) as response:
    print(json.load(response)["choices"][0]["message"]["content"])`
  if (mode === 'javascript') return `// Node.js
const response = await fetch(${JSON.stringify(base + '/chat/completions')}, {
  method: "POST",
  headers: {
    Authorization: \`Bearer \${process.env.WORKBUDDY_API_KEY}\`,
    "Content-Type": "application/json"
  },
  body: JSON.stringify({
    model: ${JSON.stringify(model)},
    messages: [{ role: "user", content: "Hi" }],
    max_tokens: 256
  })
});
if (!response.ok) throw new Error(\`HTTP \${response.status}\`);
console.log(await response.json());`
  return `curl '${base}/chat/completions' \\
  -H "Authorization: Bearer $WORKBUDDY_API_KEY" \\
  -H 'Content-Type: application/json' \\
  -d '${JSON.stringify({ model, messages: [{ role: 'user', content: 'Hi' }], max_tokens: 256 }).replace(/'/g, "'\\''")}'`
}

export default function AccessPage({ session }: { session: SessionInfo }) {
  const [info, setInfo] = useState<AccessInfo | null>(null)
  const [models, setModels] = useState<string[]>([])
  const [model, setModel] = useState('cn:fast-model')
  const [mode, setMode] = useState<ExampleMode>('powershell')
  const [shown, setShown] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState<'rotate' | 'restart' | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [copied, setCopied] = useState('')
  const [modelError, setModelError] = useState('')
  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const next = await api.access()
      setInfo(next)
      if (next.status === 'active') setPending(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : '接入信息加载失败')
    } finally { setLoading(false) }
    try {
      const result = await api.models()
      const ids = (result.data || []).map(m => m.id)
      setModels(ids)
      setModel(current => ids.includes(current) ? current : ids[0] || current)
      setModelError('')
    } catch { setModelError('模型列表暂不可用，请检查网关状态。') }
  }, [])
  useEffect(() => { void load() }, [load])
  useEffect(() => {
    if (!copied) return
    const timer = window.setTimeout(() => setCopied(''), 2000)
    return () => window.clearTimeout(timer)
  }, [copied])
  const copy = async (name: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(name)
    } catch { setError('复制失败：浏览器未授予剪贴板权限，请选中内容手动复制。') }
  }
  const rotate = async () => {
    if (!info) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const result = await api.rotateAccessKey(info.api_key)
      setInfo({ ...info, api_key: result.api_key, status: 'rejected' })
      setShown(false)
      setPending(true)
      setNotice(result.message)
      setConfirm(null)
    } catch (err) { setError(err instanceof Error ? err.message : '生成失败'); setConfirm(null) }
    finally { setBusy(false) }
  }
  const restart = async () => {
    setBusy(true)
    setNotice('')
    setError('')
    try {
      await api.restart()
      setConfirm(null)
      await load()
      setNotice('重启操作已完成，请以 Key 验证状态为准。')
    } catch (err) { setError(err instanceof Error ? err.message : '重启失败'); setConfirm(null) }
    finally { setBusy(false) }
  }
  const copyButton = (name: string, value: string) => <button className="btn icon-button" title={`复制 ${name}`} aria-label={`复制 ${name}`} disabled={!value || busy || loading} onClick={() => void copy(name, value)}>{copied === name ? <Check size={16} /> : <Copy size={16} />}</button>
  if (loading && !info) return <Spinner label="正在读取 API 接入信息…" />
  const base = info ? clientBaseURL(info.gateway_url, window.location.href) : ''
  const example = base ? exampleFor(mode, base, model) : ''
  const state = pending ? '已保存，待重启' : info?.status === 'active' ? '验证通过' : info?.status === 'unprotected' ? '未启用鉴权' : info?.status === 'rejected' ? 'Key 未通过验证' : '网关不可达'
  return <>
    <div className="page-head">
      <div><h1>API 接入</h1><p>OpenAI 兼容接口</p></div>
      <div className="page-actions">
        <button className="btn" disabled={busy || loading} onClick={() => { setNotice(''); void load() }}><RefreshCw size={16} className={loading ? 'spin' : ''} />刷新验证</button>
        <button className="btn btn-primary" disabled={session.read_only || !info?.managed || busy || loading || pending || info?.status === 'rejected'} onClick={() => setConfirm('rotate')}><KeyRound size={16} />生成新 Key</button>
      </div>
    </div>
    {error && <Alert kind="error" onClose={() => setError('')}>{error}</Alert>}
    {notice && <Alert kind="ok" onClose={() => setNotice('')}>{notice}</Alert>}
    {info && <>
      {!info.managed && <Alert kind="warn">当前 Key 来自部署配置，网页生成已禁用。请先移除 gateway_api_key / WBGUI_GATEWAY_API_KEY 覆盖项。</Alert>}
      {info.status === 'unprotected' && <Alert kind="warn">{info.api_key ? '运行中的网关仍允许无 Key 访问。已保存的 Key 尚未启用鉴权，请重启网关后重新验证。' : '网关当前没有 API Key 保护。请生成 Key 并重启网关。'}</Alert>}
      {(pending || info.status === 'rejected') && <Alert kind="warn">配置中的 Key 尚未通过运行中网关的验证。若刚更换 Key，请重启网关；否则请检查部署配置。</Alert>}
      <section className="access-section" aria-labelledby="connection-title">
        <div className="section-head"><h2 id="connection-title">连接信息</h2><span className="badge">Chat Completions</span></div>
        <div className="access-connection">
          <div className="field"><label htmlFor="access-url">Base URL</label><div className="access-value"><input id="access-url" className="mono" type="text" readOnly value={base} />{copyButton('Base URL', base)}</div><div className="desc">此地址供当前主机网络中的客户端使用；异机访问请替换为网关主机 IP 或已配置的反向代理地址。</div></div>
          <div className="field"><label htmlFor="access-model">模型</label><div className="access-value"><select id="access-model" value={model} onChange={e => setModel(e.target.value)}>{!models.includes(model) && <option value={model}>{model}</option>}{models.map(id => <option key={id} value={id}>{id}</option>)}</select>{copyButton('模型', model)}</div>{modelError && <div className="desc">{modelError}</div>}</div>
        </div>
        <dl className="access-facts"><div><dt>聊天端点</dt><dd><code>POST /v1/chat/completions</code></dd></div><div><dt>鉴权方式</dt><dd><code>Authorization: Bearer &lt;API Key&gt;</code></dd></div></dl>
      </section>
      <section className="access-section" aria-labelledby="key-title">
        <div className="section-head"><h2 id="key-title">API 密钥</h2><span className={`badge ${info.status === 'active' && !pending ? 'badge-ok' : 'badge-warn'}`}>{state}</span></div>
        <div className="access-key-row">
          <div className="field"><label htmlFor="access-key">API Key</label><div className="access-value"><input id="access-key" autoComplete="off" spellCheck={false} className="mono" type={shown ? 'text' : 'password'} readOnly value={info.api_key} placeholder="尚未设置" /><button className="btn icon-button" title={shown ? '隐藏 API Key' : '显示 API Key'} aria-label={shown ? '隐藏 API Key' : '显示 API Key'} onClick={() => setShown(v => !v)}>{shown ? <EyeOff size={16} /> : <Eye size={16} />}</button>{copyButton('API Key', info.api_key)}</div></div>
          <button className="btn" disabled={session.read_only || !session.dangerous_ops || busy || loading} onClick={() => setConfirm('restart')}><RotateCw size={16} />重启网关</button>
        </div>
        <p className="desc">单一全局 Key；更换并重启后旧 Key 失效，已接入的客户端需同步更新。密钥不写入调用示例。</p>
        {!session.dangerous_ops && <p className="desc">网页重启未开启，请在部署主机重启网关容器。</p>}
      </section>
      <section className="access-section" aria-labelledby="example-title">
        <div className="section-head"><h2 id="example-title">调用示例</h2><Link className="section-link" to="/playground">打开调试台</Link></div>
        <div className="access-code-head"><div className="tabs" role="tablist" aria-label="示例语言">{(['powershell', 'curl', 'python', 'javascript'] as const).map(value => <button key={value} role="tab" aria-selected={value === mode} className={`tab ${value === mode ? 'active' : ''}`} onClick={() => setMode(value)}>{value === 'powershell' ? 'PowerShell' : value === 'curl' ? 'curl (Bash)' : value === 'python' ? 'Python' : 'JavaScript'}</button>)}</div>{copyButton('调用示例', example)}</div>
        <pre className="access-code" data-testid="access-example"><code>{example}</code></pre>
        <p className="desc">环境变量：<code>WORKBUDDY_API_KEY</code> = 上方 API Key。管理界面端口不是 API 端口。</p>
      </section>
    </>}
    <span role="status" className="access-copy-status">{copied ? `${copied} 已复制` : ''}</span>
    {confirm === 'rotate' && <ConfirmDialog title="生成新的全局 API Key" message="新 Key 将替换配置中的现有 Key，但需重启网关后才生效。重启后所有使用旧 Key 的客户端都需要更新。此次操作不会自动重启。" confirmText="确认生成并保存" danger busy={busy} onConfirm={() => void rotate()} onCancel={() => { if (!busy) setConfirm(null) }} />}
    {confirm === 'restart' && <ConfirmDialog title="重启网关并加载配置" message="重启会中断正在进行的 API 请求，并加载所有已保存的网关配置。若已更换 Key，旧 Key 将失效。" confirmText="确认重启" danger busy={busy} onConfirm={() => void restart()} onCancel={() => { if (!busy) setConfirm(null) }} />}
  </>
}
