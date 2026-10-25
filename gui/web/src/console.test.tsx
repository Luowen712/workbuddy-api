// @vitest-environment jsdom
import { describe, expect, it, afterEach, vi } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { changeList, parseConfig, setConfigPath, validateConfig } from './configDocument'
import { requestQuery, duration, ThemeControl } from './console'

afterEach(cleanup)
describe('configuration drafts', () => {
  it('keeps unknown nested fields when editing known settings', () => {
    const original = { pool: { custom: { keep: true }, max_in_flight: 3 }, future: [1, 2] }
    const next = setConfigPath(original, 'pool.max_in_flight', 5)
    expect(next).toEqual({ pool: { custom: { keep: true }, max_in_flight: 5 }, future: [1, 2] })
    expect(original.pool.max_in_flight).toBe(3)
  })
  it('rejects invalid JSON roots and invalid hours without dropping the draft', () => {
    expect(() => parseConfig('[]')).toThrow()
    expect(() => parseConfig('{')).toThrow()
    expect(validateConfig({ schedule: { checkin_hours: [24] } })).not.toEqual([])
    expect(validateConfig({ telemetry: { max_events: -1 } })).not.toEqual([])
    expect(validateConfig({ schedule: { checkin_hours: [0, 23] } })).toEqual([])
  })
  it('redacts secrets in diff including unknown nested keys', () => {
    const changes = changeList({ api_key: 'old-secret' }, { api_key: 'new-secret', extension: { password: 'hidden' } })
    expect(JSON.stringify(changes)).not.toMatch(/old-secret|new-secret|hidden/)
    expect(changes.map(c => c.path)).toContain('api_key')
  })
})
describe('console controls', () => {
  it('encodes combined filters and omits empty ones', () => {
    const query = new URLSearchParams(requestQuery({ model: 'global:a/b', status: 'error', cursor: '', account_uid: 'u1' }))
    expect(query.get('model')).toBe('global:a/b')
    expect(query.get('status')).toBe('error')
    expect(query.has('cursor')).toBe(false)
  })
  it('distinguishes unknown latency from zero', () => {
    expect(duration(null)).toBe('—')
    expect(duration(0)).toBe('0 ms')
  })
  it('persists theme and applies it to the root', () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    render(<ThemeControl />)
    fireEvent.change(screen.getByLabelText('外观主题'), { target: { value: 'dark' } })
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('wb-theme')).toBe('dark')
  })
})
