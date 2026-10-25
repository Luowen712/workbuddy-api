import { useCallback, useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { Activity, BarChart3, Boxes, ChevronRight, KeyRound, LayoutDashboard, LogOut, Menu, MessageSquare, Settings2, ShieldCheck, Users, X } from 'lucide-react'
import { api, setUnauthorizedHandler } from './api'
import type { SessionInfo } from './types'
import { Alert, Spinner } from './ui'
import { ThemeControl } from './console'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import StatsPage from './pages/StatsPage'
import Accounts from './pages/Accounts'
import LoginWizard from './pages/LoginWizard'
import Playground from './pages/Playground'
import ConfigPage from './pages/ConfigPage'
import System from './pages/System'
import RequestLogs from './pages/RequestLogs'
import ModelsPage from './pages/ModelsPage'
import AccessPage from './pages/AccessPage'

const NAV = [
  { to: '/', label: '总览', icon: LayoutDashboard, end: true, group: '监控' },
  { to: '/accounts', label: '账号池', icon: Users },
  { to: '/requests', label: '请求日志', icon: Activity },
  { to: '/stats', label: '统计分析', icon: BarChart3 },
  { to: '/access', label: 'API 接入', icon: KeyRound, group: '管理' },
  { to: '/models', label: '模型与价格', icon: Boxes },
  { to: '/playground', label: '调试台', icon: MessageSquare },
  { to: '/config', label: '网关设置', icon: Settings2 },
  { to: '/system', label: '系统', icon: ShieldCheck },
]

export default function App() {
  const [session, setSession] = useState<SessionInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [banner, setBanner] = useState<string | null>(null)
  const navigate = useNavigate()
  const refreshSession = useCallback(async () => {
    try { const s = await api.session(); setSession(s); return s }
    catch { setSession(null); return null }
    finally { setLoading(false) }
  }, [])
  useEffect(() => { void refreshSession() }, [refreshSession])
  useEffect(() => {
    setUnauthorizedHandler(() => { setSession(prev => prev ? { ...prev, authenticated: false } : prev); setBanner('会话已过期，请重新登录'); navigate('/') })
  }, [navigate])
  const logout = useCallback(async () => {
    await api.logout().catch(() => undefined)
    setSession(s => s ? { ...s, authenticated: false, username: '' } : s)
  }, [])
  if (loading) return <div className="login-wrap"><Spinner label="正在连接服务…" /></div>
  if (!session?.authenticated) return <><div className="login-theme"><ThemeControl /></div><Login info={session} banner={banner} onCloseBanner={() => setBanner(null)} onSuccess={refreshSession} /></>
  return <Shell session={session} onLogout={logout} onSessionRefresh={refreshSession} />
}

function Shell({ session, onLogout, onSessionRefresh }: { session: SessionInfo; onLogout: () => void; onSessionRefresh: () => Promise<SessionInfo | null> }) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const location = useLocation()
  useEffect(() => { setMobileOpen(false) }, [location.pathname])
  useEffect(() => {
    if (!mobileOpen) return
    const close = (e: KeyboardEvent) => { if (e.key === 'Escape') setMobileOpen(false) }
    document.addEventListener('keydown', close)
    return () => document.removeEventListener('keydown', close)
  }, [mobileOpen])
  const current = NAV.find(item => item.to === location.pathname)?.label || '添加账号'
  return <div className="app">
    <a className="skip-link" href="#main-content">跳转到内容</a>
    {mobileOpen && <button className="sidebar-overlay" aria-label="关闭导航" onClick={() => setMobileOpen(false)} />}
    <aside className={`sidebar ${mobileOpen ? 'sidebar-open' : ''}`} aria-label="主导航">
      <div className="brand"><img className="brand-image" src="/workbuddy.png" alt="WorkBuddy" /><div className="brand-text"><strong>WorkBuddy</strong><span>网关控制台</span></div></div>
      <nav>{NAV.map(item => <div key={item.to}>{item.group && <div className="nav-group">{item.group}</div>}<NavLink to={item.to} end={item.end} className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}><item.icon size={18} strokeWidth={1.75} />{item.label}</NavLink></div>)}</nav>
      <div className="nav-spacer" />
      <div className="nav-foot"><div className="operator"><span className="avatar">{session.username.slice(0, 1).toUpperCase()}</span><div><strong>{session.username}</strong><small>{session.read_only ? '只读访问' : '本机管理员'}</small></div><button className="btn icon-button btn-ghost" onClick={onLogout} title="退出登录" aria-label="退出登录"><LogOut size={16} /></button></div></div>
    </aside>
    <div className="workspace"><header className="topbar"><div className="breadcrumb"><button className="btn icon-button mobile-menu" aria-label={mobileOpen ? '关闭导航' : '打开导航'} aria-expanded={mobileOpen} onClick={() => setMobileOpen(v => !v)}>{mobileOpen ? <X size={18} /> : <Menu size={18} />}</button><span>控制台</span><ChevronRight size={14} /><strong>{current}</strong></div><div className="topbar-actions"><ThemeControl /></div></header>
    <main className="main" id="main-content">
      {session.using_default_password && <Alert kind="warn">当前使用默认口令，请在系统页面修改密码。</Alert>}
      {session.read_only && <Alert kind="warn">只读模式，写操作已禁用。</Alert>}
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/accounts" element={<Accounts session={session} />} />
        <Route path="/stats" element={<StatsPage session={session} />} />
        <Route path="/requests" element={<RequestLogs />} />
        <Route path="/models" element={<ModelsPage session={session} />} />
        <Route path="/access" element={<AccessPage session={session} />} />
        <Route path="/login" element={<LoginWizard session={session} onDone={onSessionRefresh} />} />
        <Route path="/playground" element={<Playground />} />
        <Route path="/config" element={<ConfigPage session={session} />} />
        <Route path="/system" element={<System session={session} onSessionRefresh={onSessionRefresh} />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </main></div>
  </div>
}
