// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import AccessPage, { clientBaseURL } from './pages/AccessPage'
import { api } from './api'
import type { AccessInfo, SessionInfo } from './types'

afterEach(() => { cleanup(); vi.restoreAllMocks() })
const session = { read_only: false, dangerous_ops: true } as SessionInfo
const access: AccessInfo = { api_key: 'test-secret', status: 'active', gateway_url: 'http://host.docker.internal:7863', managed: true }
function mount(currentSession = session) {
  vi.spyOn(api, 'access').mockResolvedValue(access)
  vi.spyOn(api, 'models').mockResolvedValue({ data: [{ id: 'cn:fast-model' }], count: 1 } as Awaited<ReturnType<typeof api.models>>)
  return render(<MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}><AccessPage session={currentSession} /></MemoryRouter>)
}

describe('API access', () => {
  it('uses the browser host instead of the docker-internal host, preserving the gateway port', () => {
    expect(clientBaseURL('http://host.docker.internal:7863', 'http://localhost:8787')).toBe('http://localhost:7863/v1')
    expect(clientBaseURL('http://host.docker.internal:7863', 'http://192.168.1.5:8787')).toBe('http://192.168.1.5:7863/v1')
    expect(clientBaseURL('https://api.example.test', 'https://console.example.test')).toBe('https://api.example.test/v1')
    expect(clientBaseURL('https://api.example.test/v1/', 'https://console.example.test')).toBe('https://api.example.test/v1')
    expect(clientBaseURL('https://api.example.test/proxy/', 'https://console.example.test')).toBe('https://api.example.test/proxy/v1')
  })
  it('masks the key, keeps secrets out of examples, and copies only on explicit click', async () => {
    const copy = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: copy } })
    mount()
    const input = await screen.findByLabelText('API Key')
    expect(input.getAttribute('type')).toBe('password')
    expect(screen.getByTestId('access-example').textContent).not.toContain('test-secret')
    fireEvent.click(screen.getByRole('button', { name: '复制 API Key' }))
    await waitFor(() => expect(copy).toHaveBeenCalledWith('test-secret'))
  })
  it('requires confirmation and leaves the existing key untouched when cancelled', async () => {
    const rotate = vi.spyOn(api, 'rotateAccessKey').mockResolvedValue({ ok: true, api_key: 'new-key', message: 'saved' })
    mount()
    fireEvent.click(await screen.findByRole('button', { name: '生成新 Key' }))
    expect(rotate).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(rotate).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '生成新 Key' }))
    fireEvent.click(screen.getByRole('button', { name: '确认生成并保存' }))
    await waitFor(() => expect(rotate).toHaveBeenCalledWith('test-secret'))
    expect(await screen.findByText('已保存，待重启')).toBeTruthy()
    expect((screen.getByLabelText('API Key') as HTMLInputElement).value).toBe('new-key')
  })
  it('reports clipboard failures instead of falsely claiming success', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockRejectedValue(new Error()) } })
    mount()
    fireEvent.click(await screen.findByRole('button', { name: '复制 API Key' }))
    expect(await screen.findByText(/复制失败/)).toBeTruthy()
  })
  it('disables key generation and restart for read-only sessions', async () => {
    mount({ ...session, read_only: true })
    expect((await screen.findByRole('button', { name: '生成新 Key' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '重启网关' }) as HTMLButtonElement).disabled).toBe(true)
  })
  it('requires separate confirmation for restart and refreshes verification afterwards', async () => {
    const restart = vi.spyOn(api, 'restart').mockResolvedValue({ ok: true, message: 'restarted' })
    mount()
    fireEvent.click(await screen.findByRole('button', { name: '重启网关' }))
    expect(restart).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '确认重启' }))
    await waitFor(() => expect(restart).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('重启操作已完成，请以 Key 验证状态为准。')).toBeTruthy()
    expect(api.access).toHaveBeenCalledTimes(2)
  })
})
