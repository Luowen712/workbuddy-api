export type ConfigDoc = Record<string, unknown>

export function parseConfig(text: string): ConfigDoc {
  const result: unknown = JSON.parse(text)
  if (!result || typeof result !== 'object' || Array.isArray(result)) throw new Error('配置顶层必须是 JSON 对象')
  return result as ConfigDoc
}

export function getConfigPath(doc: ConfigDoc, path: string): unknown {
  return path.split('.').reduce<unknown>((value, key) => value && typeof value === 'object' ? (value as ConfigDoc)[key] : undefined, doc)
}

export function setConfigPath(doc: ConfigDoc, path: string, value: unknown): ConfigDoc {
  const keys = path.split('.')
  const clone = { ...doc }
  let node = clone
  for (const key of keys.slice(0, -1)) {
    node[key] = { ...(node[key] as ConfigDoc ?? {}) }
    node = node[key] as ConfigDoc
  }
  if (value === undefined) delete node[keys[keys.length - 1]]
  else node[keys[keys.length - 1]] = value
  return clone
}

export function validateConfig(doc: ConfigDoc): string[] {
  const errors: string[] = []
  for (const [key, value] of Object.entries((doc.schedule as ConfigDoc) ?? {})) {
    if (key.endsWith('_hours') && (!Array.isArray(value) || value.some(h => !Number.isInteger(h) || h < 0 || h > 23))) errors.push(`schedule.${key} 必须是 0–23 的整点数组`)
  }
  for (const path of ['pool.max_in_flight', 'pool.breaker_threshold', 'upstream.timeout_seconds', 'upstream.header_timeout_seconds', 'upstream.idle_timeout_seconds', 'telemetry.retention_days', 'telemetry.max_events']) {
    const n = getConfigPath(doc, path)
    if (n !== undefined && (typeof n !== 'number' || !Number.isInteger(n) || n < (path.startsWith('telemetry.') ? 1 : 0))) errors.push(`${path} 必须是有效的${path.startsWith('telemetry.') ? '正' : '非负'}整数`)
  }
  return errors
}

export interface ConfigChange { path: string; before: string; after: string }
const secret = /key|token|password|secret|credential|authorization|cookie|url/i
function flatten(doc: ConfigDoc, prefix = ''): ConfigDoc {
  const out: ConfigDoc = {}
  for (const [key, value] of Object.entries(doc)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length) Object.assign(out, flatten(value as ConfigDoc, path))
    else out[path] = value
  }
  return out
}
export function changeList(before: ConfigDoc, after: ConfigDoc): ConfigChange[] {
  const a = flatten(before), b = flatten(after)
  return [...new Set([...Object.keys(a), ...Object.keys(b)])].filter(key => JSON.stringify(a[key]) !== JSON.stringify(b[key])).map(path => ({
    path, before: secret.test(path) ? (a[path] === undefined ? '未设置' : '••••••') : JSON.stringify(a[path]) ?? '未设置',
    after: secret.test(path) ? (b[path] === undefined ? '已移除' : '••••••') : JSON.stringify(b[path]) ?? '已移除',
  }))
}
