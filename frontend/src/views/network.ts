import { injectAppShell, renderChrome } from '../layout'
import { apiPath } from '../api'
import { authHeaders } from '../auth'

type RangeKey = '24h' | '72h'

interface ServerItem {
  ID?: number | string
  id?: number | string
  Name?: string
  name?: string
  Host?: {
    CountryCode?: string
    country_code?: string
  } | null
  host?: {
    CountryCode?: string
    country_code?: string
  } | null
  CountryCode?: string
  country_code?: string
}

interface MonitorPoint {
  MonitorID?: number | string
  monitor_id?: number | string
  MonitorName?: string
  monitor_name?: string
  Name?: string
  name?: string
  CreatedAt?: string
  created_at?: string
  AvgDelay?: number | string
  avg_delay?: number | string
  Up?: number | string
  up?: number | string
  Down?: number | string
  down?: number | string
}

interface MonitorConfig {
  ID?: number | string
  id?: number | string
  Name?: string
  name?: string
  Type?: number | string
  type?: number | string
}

interface MonitorConfigsPayload {
  monitors?: unknown
  server_ids?: unknown
  serverIds?: unknown
}

interface MonitorHistoryPayload {
  history: MonitorPoint[]
  summary: NetworkSummary | null
}

interface MonitorHistoryCacheEntry {
  payload: MonitorHistoryPayload
  cachedAt: number
}

type SeriesPoint = {
  time: number
  value: number
  hasLatency: boolean
  up: number
  down: number
}

type Series = {
  id: string
  name: string
  color: string
  points: SeriesPoint[]
}

type NetworkSummary = {
  max: number | null
  min: number | null
  avg: number | null
  p95: number | null
  loss: number | null
  count: number
  latencyCount: number
}

type ChartModel = {
  width: number
  height: number
  padding: { top: number; right: number; bottom: number; left: number }
  minTime: number
  maxTime: number
  timeSpan: number
  maxValue: number
  series: Series[]
  serverName: string
  range: RangeKey
}

type HoverPoint = {
  name: string
  color: string
  point: SeriesPoint
}

const RANGE_LABELS: Record<RangeKey, string> = {
  '24h': '24小时',
  '72h': '72小时',
}

const RANGE_ORDER: RangeKey[] = ['24h', '72h']
const HISTORY_CACHE_TTL_MS = 180_000
const HISTORY_CACHE_LIMIT = 80
const HISTORY_PREFETCH_CONCURRENCY = 2
const INVALID_LATENCY_VALUES = [0, 450]
const NETWORK_COLORS = [
  '#03a9f4',
  '#4caf50',
  '#ff8840',
  '#9c27b0',
  '#ff5722',
  '#2185d0',
  '#e91e63',
  '#00b5ad',
  '#b5cc18',
  '#fbbd08',
  '#a333c8',
  '#db2828',
  '#767676',
  '#21ba45',
  '#6435c9',
  '#f2711c',
]

let resizeHandler: (() => void) | undefined
let abortController: AbortController | undefined

export async function initNetwork(container: HTMLDivElement) {
  abortController?.abort()
  abortController = new AbortController()
  const signal = abortController.signal

  const { contentArea } = injectAppShell(container, 'network')

  contentArea.innerHTML = `
    <section class="network-view">
      <div class="network-server-bar" id="server-buttons"></div>
      <div class="network-chart-panel">
        <div class="network-chart-head">
          <div>
            <div class="network-chart-title" id="chart-title">24小时网络延迟</div>
            <div class="network-chart-subtitle" id="chart-subtitle">请选择服务器</div>
          </div>
          <div class="network-chart-actions">
            <div class="network-range-switch" id="range-switch" aria-label="网络延迟范围">
              <button class="network-range-btn active" type="button" data-range="24h">24h</button>
              <button class="network-range-btn" type="button" data-range="72h">72h</button>
            </div>
            <div class="network-chart-unit">ms</div>
          </div>
        </div>
        <div class="network-stats" id="network-stats" hidden></div>
        <div class="network-chart-stage" id="chart-container" aria-live="polite"></div>
      </div>
    </section>
  `

  const buttonsContainer = contentArea.querySelector<HTMLDivElement>('#server-buttons')
  const chartContainer = contentArea.querySelector<HTMLDivElement>('#chart-container')
  const subtitle = contentArea.querySelector<HTMLDivElement>('#chart-subtitle')
  const title = contentArea.querySelector<HTMLDivElement>('#chart-title')
  const rangeSwitch = contentArea.querySelector<HTMLDivElement>('#range-switch')
  const statsContainer = contentArea.querySelector<HTMLDivElement>('#network-stats')
  if (!buttonsContainer || !chartContainer || !subtitle || !title || !rangeSwitch || !statsContainer) return cleanupNetwork

  fetch(apiPath('/profile'), { credentials: 'include', headers: authHeaders(), signal })
    .then((response) => response.json())
    .then((data) => renderChrome(data.data || data))
    .catch((error) => {
      if (!signal.aborted) console.warn('Failed to load profile', error)
    })

  setChartMessage(chartContainer, '加载服务器列表...')

  try {
    const [serverPayload, monitorConfigPayload] = await Promise.all([
      fetchJson<unknown>(apiPath('/server/list'), signal),
      fetchJson<unknown>(apiPath('/monitor/configs'), signal).catch((error) => {
        if (!signal.aborted) console.warn('Failed to load monitor configs', error)
        return null
      }),
    ])
    let servers = normalizeServers(serverPayload)
    const monitorNames = new Map<string, string>()
    if (monitorConfigPayload !== null) {
      for (const [id, name] of normalizeMonitorNames(monitorConfigPayload)) {
        monitorNames.set(id, name)
      }
      const monitoredIds = normalizeMonitoredServerIds(monitorConfigPayload)
      servers = filterMonitoredServers(servers, monitoredIds)
    }
    if (servers.length === 0) {
      buttonsContainer.textContent = ''
      subtitle.textContent = monitorConfigPayload === null ? '暂无服务器' : '暂无参与检测的服务器'
      statsContainer.hidden = true
      setChartMessage(chartContainer, monitorConfigPayload === null ? '暂无服务器' : '暂无参与检测的服务器')
      return cleanupNetwork
    }

    let currentServerId = serverId(servers[0])
    let currentRange: RangeKey = '24h'
    let currentSeries: Series[] = []
    let currentServerName = serverName(servers[0])
    let chartModel: ChartModel | null = null
    let loadToken = 0
    let prefetchTimer: number | undefined
    let prefetchRun = 0
    const historyCache = new Map<string, MonitorHistoryCacheEntry>()
    const historyRequests = new Map<string, Promise<MonitorHistoryPayload>>()

    const updateActiveButton = () => {
      for (const button of buttonsContainer.querySelectorAll<HTMLButtonElement>('.network-btn')) {
        button.classList.toggle('active', button.dataset.id === currentServerId)
      }
    }

    const renderServerButtons = () => {
      buttonsContainer.innerHTML = servers.map((server) => `
        <button class="network-btn" type="button" data-id="${escapeAttribute(serverId(server))}">
          ${serverFlagMarkup(server)}
          <span>${escapeHtml(serverName(server))}</span>
        </button>
      `).join('')
      updateActiveButton()
    }

    const updateActiveRange = () => {
      for (const button of rangeSwitch.querySelectorAll<HTMLButtonElement>('.network-range-btn')) {
        button.classList.toggle('active', button.dataset.range === currentRange)
      }
      title.textContent = `${RANGE_LABELS[currentRange]}网络延迟`
    }

    const renderCurrentChart = () => {
      if (currentSeries.length === 0) {
        chartModel = null
        return
      }
      chartModel = renderSvgChart(chartContainer, currentSeries, currentServerName, currentRange)
    }

    const cacheKeyFor = (id: string, range: RangeKey) => `${id}:${range}`

    const isFreshCache = (entry: MonitorHistoryCacheEntry) => {
      return Date.now() - entry.cachedAt <= HISTORY_CACHE_TTL_MS
    }

    const putHistoryCache = (key: string, payload: MonitorHistoryPayload) => {
      historyCache.set(key, { payload, cachedAt: Date.now() })
      while (historyCache.size > HISTORY_CACHE_LIMIT) {
        const oldestKey = historyCache.keys().next().value
        if (!oldestKey) break
        historyCache.delete(oldestKey)
      }
    }

    const fetchHistoryPayload = (id: string, range: RangeKey, force = false) => {
      const cacheKey = cacheKeyFor(id, range)
      const cached = historyCache.get(cacheKey)
      if (!force && cached && isFreshCache(cached)) return Promise.resolve(cached.payload)

      const pending = historyRequests.get(cacheKey)
      if (!force && pending) return pending

      const request = fetchJson<unknown>(`${apiPath(`/monitor/${encodeURIComponent(id)}`)}?range=${range}`, signal)
        .then((payload) => {
          const normalized = normalizeHistoryPayload(payload, monitorNames, range)
          putHistoryCache(cacheKey, normalized)
          return normalized
        })
        .finally(() => {
          if (historyRequests.get(cacheKey) === request) {
            historyRequests.delete(cacheKey)
          }
        })

      historyRequests.set(cacheKey, request)
      return request
    }

    const renderHistoryPayload = (payload: MonitorHistoryPayload) => {
      const history = payload.history

      if (history.length === 0) {
        currentSeries = []
        chartModel = null
        statsContainer.hidden = true
        setChartMessage(chartContainer, '该服务器暂无网络监控数据')
        return
      }

      currentSeries = buildSeries(history)
      if (currentSeries.length === 0) {
        chartModel = null
        statsContainer.hidden = true
        setChartMessage(chartContainer, '该服务器暂无网络监控数据')
        return
      }

      renderSummary(statsContainer, payload.summary || summarizeSeries(currentSeries), currentRange)
      renderCurrentChart()
    }

    const orderedPrefetchIds = () => {
      const ids = servers.map(serverId).filter(Boolean)
      const currentIndex = ids.indexOf(currentServerId)
      if (currentIndex < 0) return ids.filter((id) => id !== currentServerId)
      return [
        ...ids.slice(currentIndex + 1),
        ...ids.slice(0, currentIndex),
      ].filter((id) => id !== currentServerId)
    }

    const prefetchHistory = async (runId: number, range: RangeKey) => {
      const jobs: Array<{ id: string; range: RangeKey }> = orderedPrefetchIds().map((id) => ({ id, range }))
      const alternateRange = RANGE_ORDER.find((item) => item !== range)
      if (alternateRange) jobs.push({ id: currentServerId, range: alternateRange })

      let index = 0
      const workers = Array.from({ length: Math.min(HISTORY_PREFETCH_CONCURRENCY, jobs.length) }, async () => {
        while (!signal.aborted && runId === prefetchRun) {
          const job = jobs[index++]
          if (!job) break

          const cached = historyCache.get(cacheKeyFor(job.id, job.range))
          if (cached && isFreshCache(cached)) continue

          try {
            await fetchHistoryPayload(job.id, job.range)
          } catch (error) {
            if (!signal.aborted) console.warn('Failed to prefetch network history', error)
          }
        }
      })
      await Promise.all(workers)
    }

    const scheduleHistoryPrefetch = (range: RangeKey) => {
      window.clearTimeout(prefetchTimer)
      const runId = ++prefetchRun
      prefetchTimer = window.setTimeout(() => {
        void prefetchHistory(runId, range)
      }, 160)
    }

    const loadChartData = async (id: string, force = false) => {
      const token = ++loadToken
      const range = currentRange
      const server = servers.find((item) => serverId(item) === id)
      currentServerName = server ? serverName(server) : `#${id}`
      subtitle.innerHTML = server
        ? `${serverFlagMarkup(server)}<span>${escapeHtml(currentServerName)}</span>`
        : `<span>#${escapeHtml(id)}</span>`
      hideChartHover(chartContainer)

      const cacheKey = cacheKeyFor(id, range)
      const cached = !force ? historyCache.get(cacheKey) : undefined
      if (cached && isFreshCache(cached)) {
        renderHistoryPayload(cached.payload)
        scheduleHistoryPrefetch(range)
        return
      }

      statsContainer.hidden = true
      chartModel = null
      setChartMessage(chartContainer, '加载监控数据...')

      try {
        const payload = await fetchHistoryPayload(id, range, force)
        if (token !== loadToken || id !== currentServerId || range !== currentRange) return
        renderHistoryPayload(payload)
        scheduleHistoryPrefetch(range)
      } catch (error) {
        if (!signal.aborted) {
          console.warn('Failed to load network history', error)
          currentSeries = []
          chartModel = null
          statsContainer.hidden = true
          setChartMessage(chartContainer, '加载数据失败')
        }
      }
    }

    const applyMonitorConfigs = (payload: unknown) => {
      if (signal.aborted) return

      monitorNames.clear()
      for (const [id, name] of normalizeMonitorNames(payload)) {
        monitorNames.set(id, name)
      }
      historyCache.clear()

      const monitoredIds = normalizeMonitoredServerIds(payload)
      const filteredServers = filterMonitoredServers(servers, monitoredIds)
      if (filteredServers.length === 0) {
        servers = []
        buttonsContainer.textContent = ''
        subtitle.textContent = '暂无参与检测的服务器'
        statsContainer.hidden = true
        currentSeries = []
        chartModel = null
        setChartMessage(chartContainer, '暂无参与检测的服务器')
        return
      }

      if (filteredServers.length === servers.length) return

      servers = filteredServers
      renderServerButtons()
      if (!servers.some((server) => serverId(server) === currentServerId)) {
        currentServerId = serverId(servers[0])
        void loadChartData(currentServerId, true)
      }
    }

    buttonsContainer.addEventListener('click', (event) => {
      const button = (event.target as HTMLElement).closest<HTMLButtonElement>('.network-btn')
      if (!button || !button.dataset.id || button.dataset.id === currentServerId) return
      currentServerId = button.dataset.id
      updateActiveButton()
      void loadChartData(currentServerId)
    }, { signal })

    rangeSwitch.addEventListener('click', (event) => {
      const button = (event.target as HTMLElement).closest<HTMLButtonElement>('.network-range-btn')
      const range = button?.dataset.range
      if (!button || !isRangeKey(range) || range === currentRange) return
      currentRange = range
      updateActiveRange()
      void loadChartData(currentServerId)
    }, { signal })

    chartContainer.addEventListener('pointermove', (event) => {
      if (!chartModel) return
      updateChartHover(chartContainer, chartModel, event)
    }, { signal })

    chartContainer.addEventListener('pointerdown', (event) => {
      if (!chartModel) return
      if (event.pointerType !== 'mouse') event.preventDefault()
      updateChartHover(chartContainer, chartModel, event)
    }, { signal })

    chartContainer.addEventListener('pointercancel', () => {
      hideChartHover(chartContainer)
    }, { signal })

    chartContainer.addEventListener('pointerleave', () => {
      hideChartHover(chartContainer)
    }, { signal })

    resizeHandler = throttleFrame(() => {
      if (currentSeries.length > 0) {
        renderCurrentChart()
      }
    })
    window.addEventListener('resize', resizeHandler, { signal })

    renderServerButtons()
    updateActiveRange()
    void loadChartData(currentServerId)
    if (monitorConfigPayload === null) {
      fetchJson<unknown>(apiPath('/monitor/configs'), signal)
        .then(applyMonitorConfigs)
        .catch((error) => {
          if (!signal.aborted) console.warn('Failed to load monitor configs', error)
        })
    }
  } catch (error) {
    if (!signal.aborted) {
      console.warn('Failed to load network servers', error)
      setChartMessage(chartContainer, '加载服务器列表失败')
    }
  }

  return cleanupNetwork
}

function cleanupNetwork() {
  abortController?.abort()
  abortController = undefined
  resizeHandler = undefined
}

async function fetchJson<T>(url: string, signal: AbortSignal): Promise<T> {
  const response = await fetch(url, { credentials: 'include', headers: authHeaders(), signal })
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`)
  return response.json() as Promise<T>
}

function normalizeServers(payload: unknown): ServerItem[] {
  if (Array.isArray(payload)) return payload as ServerItem[]
  if (!payload || typeof payload !== 'object') return []

  const source = payload as { result?: unknown; servers?: unknown; data?: unknown }
  if (Array.isArray(source.result)) return source.result as ServerItem[]
  if (Array.isArray(source.servers)) return source.servers as ServerItem[]
  if (Array.isArray(source.data)) return source.data as ServerItem[]
  if (source.result && typeof source.result === 'object') {
    const result = source.result as { servers?: unknown }
    if (Array.isArray(result.servers)) return result.servers as ServerItem[]
  }
  return []
}

function normalizeMonitorNames(payload: unknown): Map<string, string> {
  const names = new Map<string, string>()
  const configs = normalizeMonitorConfigs(payload)

  for (const item of configs) {
    const id = String(item.ID ?? item.id ?? '').trim()
    const name = String(item.Name ?? item.name ?? '').trim()
    if (id && name) names.set(id, name)
  }
  return names
}

function normalizeMonitorConfigs(payload: unknown): MonitorConfig[] {
  if (Array.isArray(payload)) return payload as MonitorConfig[]
  if (!payload || typeof payload !== 'object') return []

  const source = payload as MonitorConfigsPayload & { result?: unknown; data?: unknown }
  if (Array.isArray(source.result)) return source.result as MonitorConfig[]
  if (Array.isArray(source.data)) return source.data as MonitorConfig[]
  if (Array.isArray(source.monitors)) return source.monitors as MonitorConfig[]
  if (source.result && typeof source.result === 'object') {
    const result = source.result as MonitorConfigsPayload
    if (Array.isArray(result.monitors)) return result.monitors as MonitorConfig[]
  }
  if (source.data && typeof source.data === 'object') {
    const data = source.data as MonitorConfigsPayload
    if (Array.isArray(data.monitors)) return data.monitors as MonitorConfig[]
  }
  return []
}

function normalizeMonitoredServerIds(payload: unknown): Set<string> | null {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null

  const source = payload as MonitorConfigsPayload & { result?: unknown; data?: unknown }
  const ids = source.server_ids ?? source.serverIds
  if (Array.isArray(ids)) return new Set(ids.map((id) => String(id)))
  if (source.result && typeof source.result === 'object') {
    const result = source.result as MonitorConfigsPayload
    const resultIds = result.server_ids ?? result.serverIds
    if (Array.isArray(resultIds)) return new Set(resultIds.map((id) => String(id)))
  }
  if (source.data && typeof source.data === 'object') {
    const data = source.data as MonitorConfigsPayload
    const dataIds = data.server_ids ?? data.serverIds
    if (Array.isArray(dataIds)) return new Set(dataIds.map((id) => String(id)))
  }
  return null
}

function filterMonitoredServers(servers: ServerItem[], monitoredIds: Set<string> | null) {
  if (!monitoredIds) return servers
  return servers.filter((server) => monitoredIds.has(serverId(server)))
}

function normalizeHistoryPayload(payload: unknown, monitorNames: Map<string, string>, range: RangeKey): MonitorHistoryPayload {
  let listSource: unknown = payload
  let summary: NetworkSummary | null = null
  if (payload && typeof payload === 'object' && !Array.isArray(payload)) {
    const source = payload as { data?: unknown; result?: unknown; summary?: unknown }
    if (source.summary !== undefined) summary = normalizeSummary(source.summary)
    listSource = source.data ?? source.result ?? payload
  }
  return {
    history: filterHistoryByRange(normalizeHistory(listSource, monitorNames), range),
    summary,
  }
}

function normalizeHistory(payload: unknown, monitorNames: Map<string, string>): MonitorPoint[] {
  let list: MonitorPoint[] = []
  if (Array.isArray(payload)) {
    list = payload as MonitorPoint[]
  } else if (payload && typeof payload === 'object') {
    const source = payload as { result?: unknown; data?: unknown }
    if (Array.isArray(source.result)) list = source.result as MonitorPoint[]
    else if (Array.isArray(source.data)) list = source.data as MonitorPoint[]
  }

  return list.map((point) => {
    const id = monitorPointId(point)
    const configuredName = monitorNames.get(id)
    if (!configuredName || monitorPointName(point, id) !== monitorLabel(id)) return point
    return { ...point, monitor_name: configuredName }
  })
}

function normalizeSummary(payload: unknown): NetworkSummary | null {
  if (!payload || typeof payload !== 'object') return null
  const source = payload as Record<string, unknown>
  const count = Math.max(0, finiteNumber(source.count, 0))
  return {
    max: nullableNumber(source.max),
    min: nullableNumber(source.min),
    avg: nullableNumber(source.avg),
    p95: nullableNumber(source.p95),
    loss: nullableNumber(source.loss),
    count,
    latencyCount: Math.max(0, finiteNumber(source.latency_count ?? source.latencyCount, 0)),
  }
}

function filterHistoryByRange(history: MonitorPoint[], range: RangeKey): MonitorPoint[] {
  const duration = range === '24h' ? 24 * 60 * 60 * 1000 : 72 * 60 * 60 * 1000
  const timestamped = history
    .map((point) => ({
      point,
      time: Date.parse(point.CreatedAt || point.created_at || ''),
    }))
    .filter((item) => Number.isFinite(item.time))

  if (timestamped.length === 0) return []

  const maxTime = Math.max(...timestamped.map((item) => item.time))
  const cutoff = maxTime - duration - 60_000
  return timestamped.filter((item) => item.time >= cutoff).map((item) => item.point)
}

function isValidLatency(value: number | undefined, up: number, down: number) {
  if (value === undefined || !Number.isFinite(value) || value < 0) return false
  if (INVALID_LATENCY_VALUES.some((invalid) => Math.abs(value - invalid) < 0.0001)) return false
  if (up > 0) return true
  return up === 0 && down === 0
}

function colorForSeriesKey(key: string) {
  let hash = 0
  for (let index = 0; index < key.length; index++) {
    hash = ((hash << 5) - hash + key.charCodeAt(index)) | 0
  }
  return NETWORK_COLORS[Math.abs(hash) % NETWORK_COLORS.length]
}

function displaySeriesNames(series: Series[]): Series[] {
  const counts = new Map<string, number>()
  for (const item of series) {
    counts.set(item.name, (counts.get(item.name) || 0) + 1)
  }
  return series.map((item) => ({
    ...item,
    name: (counts.get(item.name) || 0) > 1 ? `${item.name} #${item.id}` : item.name,
  }))
}

function monitorSeriesKey(point: MonitorPoint) {
  const id = monitorPointId(point)
  if (id && id !== '0') return id
  return `name:${monitorPointName(point, id)}`
}

function monitorSeriesLabel(point: MonitorPoint, key: string) {
  const id = monitorPointId(point)
  return monitorPointName(point, id && id !== '0' ? id : key)
}

function sortSeriesByLatestPoint(a: Series, b: Series) {
  const aLatest = a.points[a.points.length - 1]?.time || 0
  const bLatest = b.points[b.points.length - 1]?.time || 0
  if (aLatest !== bLatest) return bLatest - aLatest
  return a.name.localeCompare(b.name)
}

function sortPointsByTime(a: SeriesPoint, b: SeriesPoint) {
  return a.time - b.time
}

function ensureSeriesGroup(groups: Map<string, { name: string; points: SeriesPoint[] }>, key: string, name: string) {
  let group = groups.get(key)
  if (!group) {
    group = { name, points: [] }
    groups.set(key, group)
  } else if (group.name === monitorLabel(key) && name !== group.name) {
    group.name = name
  }
  return group
}

function buildSeries(history: MonitorPoint[]): Series[] {
  const groups = new Map<string, { name: string; points: SeriesPoint[] }>()

  for (const point of history) {
    const time = Date.parse(point.CreatedAt || point.created_at || '')
    if (!Number.isFinite(time)) continue

    const value = optionalNumber(point.AvgDelay ?? point.avg_delay)
    if (value !== undefined && value < 0) continue

    const key = monitorSeriesKey(point)
    const name = monitorSeriesLabel(point, key)
    const group = ensureSeriesGroup(groups, key, name)
    const up = Math.max(0, finiteNumber(point.Up ?? point.up, 0))
    const down = Math.max(0, finiteNumber(point.Down ?? point.down, 0))
    const hasLatency = isValidLatency(value, up, down)
    group.points.push({
      time,
      value: hasLatency && value !== undefined ? value : 0,
      hasLatency,
      up,
      down,
    })
  }

  const series = Array.from(groups.entries()).map(([id, group]) => ({
    id,
    name: group.name,
    color: colorForSeriesKey(id),
    points: group.points.sort(sortPointsByTime),
  }))
  return displaySeriesNames(series.sort(sortSeriesByLatestPoint))
}

function summarizeSeries(series: Series[]): NetworkSummary | null {
  const points = series.flatMap((item) => item.points)
  if (points.length === 0) return null
  const values = points
    .filter((point) => point.hasLatency && Number.isFinite(point.value))
    .map((point) => point.value)

  const up = points.reduce((sum, point) => sum + point.up, 0)
  const down = points.reduce((sum, point) => sum + point.down, 0)
  const totalChecks = up + down

  return {
    max: values.length > 0 ? Math.max(...values) : null,
    min: values.length > 0 ? Math.min(...values) : null,
    avg: values.length > 0 ? values.reduce((sum, value) => sum + value, 0) / values.length : null,
    p95: percentile(values, 0.95),
    loss: totalChecks > 0 ? (down / totalChecks) * 100 : null,
    count: points.length,
    latencyCount: values.length,
  }
}

function renderSummary(container: HTMLElement, summary: NetworkSummary | null, range: RangeKey) {
  if (!summary) {
    container.hidden = true
    container.textContent = ''
    return
  }

  container.hidden = false
  container.innerHTML = `
    <div class="network-stat-item" data-network-stat="avg">
      <span>${RANGE_LABELS[range]}平均</span>
      <strong>${formatNullableMs(summary.avg)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="p95">
      <span>${RANGE_LABELS[range]}P95</span>
      <strong>${formatNullableMs(summary.p95)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="max">
      <span>${RANGE_LABELS[range]}最高</span>
      <strong>${formatNullableMs(summary.max)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="min">
      <span>${RANGE_LABELS[range]}最低</span>
      <strong>${formatNullableMs(summary.min)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="loss">
      <span>${RANGE_LABELS[range]}丢包</span>
      <strong>${formatLoss(summary.loss)}</strong>
    </div>
  `
}

function renderSvgChart(container: HTMLElement, series: Series[], serverNameValue: string, range: RangeKey): ChartModel | null {
  const latencySeries = series
    .map((item) => ({ ...item, points: item.points.filter((point) => point.hasLatency) }))
    .filter((item) => item.points.length > 0)
  const width = Math.max(container.clientWidth, 320)
  const compact = width <= 520
  const height = compact ? 360 : 420
  const padding = compact
    ? { top: 18, right: 10, bottom: 36, left: 34 }
    : { top: 24, right: 22, bottom: 42, left: 48 }
  const plotWidth = width - padding.left - padding.right
  const plotHeight = height - padding.top - padding.bottom

  const allPoints = latencySeries.flatMap((item) => item.points)
  if (allPoints.length === 0) {
    setChartMessage(container, '该服务器暂无有效延迟数据')
    return null
  }

  const minTime = Math.min(...allPoints.map((point) => point.time))
  const maxTime = Math.max(...allPoints.map((point) => point.time))
  const rawMaxValue = Math.max(10, ...allPoints.map((point) => point.value))
  const maxValue = niceCeil(rawMaxValue * 1.08)
  const timeSpan = Math.max(1, maxTime - minTime)
  const showDateLabel = range === '72h' || timeSpan >= 23 * 60 * 60 * 1000

  const x = (time: number) => padding.left + ((time - minTime) / timeSpan) * plotWidth
  const y = (value: number) => padding.top + plotHeight - (value / maxValue) * plotHeight
  const yTicks = [0, 0.25, 0.5, 0.75, 1].map((ratio) => Math.round(maxValue * ratio))
  const xTicks = buildTimeTicks(minTime, maxTime, compact ? 3 : 5)

  const paths = latencySeries.map((item) => {
    const d = item.points
      .map((point, index) => `${index === 0 ? 'M' : 'L'} ${x(point.time).toFixed(1)} ${y(point.value).toFixed(1)}`)
      .join(' ')
    const latest = item.points[item.points.length - 1]
    const latestX = x(latest.time).toFixed(1)
    const latestY = y(latest.value).toFixed(1)
    return `
      <path class="network-line-shadow" d="${d}" stroke="${item.color}"></path>
      <path class="network-line" d="${d}" stroke="${item.color}"></path>
      <circle class="network-line-end" cx="${latestX}" cy="${latestY}" r="3.5" fill="${item.color}"></circle>
    `
  }).join('')

  const legend = latencySeries.map((item) => `
    <span class="network-legend-item">
      <span class="network-legend-swatch" style="background:${item.color}"></span>${escapeHtml(item.name)}
    </span>
  `).join('')

  const grid = yTicks.map((tick) => {
    const yy = y(tick)
    return `
      <line class="network-grid-line" x1="${padding.left}" y1="${yy.toFixed(1)}" x2="${(width - padding.right).toFixed(1)}" y2="${yy.toFixed(1)}"></line>
      <text class="network-axis-label" x="${padding.left - 10}" y="${(yy + 4).toFixed(1)}" text-anchor="end">${tick}ms</text>
    `
  }).join('')

  const timeGrid = xTicks.map((tick, index) => {
    const xx = x(tick)
    const textAnchor = index === 0 ? 'start' : index === xTicks.length - 1 ? 'end' : 'middle'
    return `
      <line class="network-grid-line is-vertical" x1="${xx.toFixed(1)}" y1="${padding.top}" x2="${xx.toFixed(1)}" y2="${height - padding.bottom}"></line>
      <text class="network-axis-label network-time-label" x="${xx.toFixed(1)}" y="${height - 14}" text-anchor="${textAnchor}">${formatTime(tick, showDateLabel)}</text>
    `
  }).join('')

  container.innerHTML = `
    <div class="network-chart-canvas">
      <svg class="network-chart-svg" style="height:${height}px" viewBox="0 0 ${width} ${height}" role="img" aria-label="${RANGE_LABELS[range]}网络延迟折线图">
        <rect class="network-plot-bg" x="${padding.left}" y="${padding.top}" width="${plotWidth}" height="${plotHeight}" rx="10"></rect>
        ${grid}
        ${timeGrid}
        <line class="network-axis-line" x1="${padding.left}" y1="${padding.top}" x2="${padding.left}" y2="${height - padding.bottom}"></line>
        <line class="network-axis-line" x1="${padding.left}" y1="${height - padding.bottom}" x2="${width - padding.right}" y2="${height - padding.bottom}"></line>
        ${paths}
        <g class="network-hover-layer"></g>
      </svg>
      <div class="network-hover-tooltip" hidden></div>
    </div>
    <div class="network-legend">${legend}</div>
  `

  return {
    width,
    height,
    padding,
    minTime,
    maxTime,
    timeSpan,
    maxValue,
    series: latencySeries,
    serverName: serverNameValue,
    range,
  }
}

function updateChartHover(container: HTMLElement, model: ChartModel, event: PointerEvent) {
  const svg = container.querySelector<SVGSVGElement>('.network-chart-svg')
  const canvas = container.querySelector<HTMLDivElement>('.network-chart-canvas')
  const layer = container.querySelector<SVGGElement>('.network-hover-layer')
  const tooltip = container.querySelector<HTMLDivElement>('.network-hover-tooltip')
  if (!svg || !canvas || !layer || !tooltip) return

  const svgRect = svg.getBoundingClientRect()
  if (svgRect.width <= 0 || svgRect.height <= 0) return

  const rawX = ((event.clientX - svgRect.left) / svgRect.width) * model.width
  const rawY = ((event.clientY - svgRect.top) / svgRect.height) * model.height
  const plotLeft = model.padding.left
  const plotRight = model.width - model.padding.right
  const plotTop = model.padding.top
  const plotBottom = model.height - model.padding.bottom

  if (rawX < plotLeft || rawX > plotRight || rawY < plotTop - 12 || rawY > plotBottom + 28) {
    hideChartHover(container)
    return
  }

  const targetTime = model.minTime + ((rawX - plotLeft) / Math.max(1, plotRight - plotLeft)) * model.timeSpan
  const hoverPoints = nearestSeriesPoints(model.series, targetTime)
  if (hoverPoints.length === 0) {
    hideChartHover(container)
    return
  }

  const hoverTime = hoverPoints.reduce((nearest, item) => {
    return Math.abs(item.point.time - targetTime) < Math.abs(nearest - targetTime) ? item.point.time : nearest
  }, hoverPoints[0].point.time)
  const hoverX = model.padding.left + ((hoverTime - model.minTime) / model.timeSpan) * (model.width - model.padding.left - model.padding.right)
  const plotHeight = model.height - model.padding.top - model.padding.bottom
  const pointMarkup = hoverPoints.map((item) => {
    const yy = model.padding.top + plotHeight - (item.point.value / model.maxValue) * plotHeight
    return `<circle class="network-hover-dot" cx="${hoverX.toFixed(1)}" cy="${yy.toFixed(1)}" r="4" fill="${item.color}"></circle>`
  }).join('')

  layer.innerHTML = `
    <line class="network-hover-line" x1="${hoverX.toFixed(1)}" y1="${plotTop}" x2="${hoverX.toFixed(1)}" y2="${plotBottom}"></line>
    ${pointMarkup}
  `

  tooltip.innerHTML = `
    <div class="network-hover-title">${escapeHtml(model.serverName)}</div>
    <div class="network-hover-time">${formatFullTime(hoverTime)}</div>
    <div class="network-hover-list">
      ${hoverPoints.map((item) => `
        <div class="network-hover-row">
          <span><i style="background:${item.color}"></i>${escapeHtml(item.name)}</span>
          <strong>${formatMs(item.point.value)}</strong>
        </div>
      `).join('')}
    </div>
  `
  tooltip.hidden = false

  const canvasRect = canvas.getBoundingClientRect()
  const localX = event.clientX - canvasRect.left
  const localY = event.clientY - canvasRect.top
  const tooltipWidth = tooltip.offsetWidth || 220
  const tooltipHeight = tooltip.offsetHeight || 120
  const left = clamp(localX + 14, 8, Math.max(8, canvasRect.width - tooltipWidth - 8))
  const top = clamp(localY - tooltipHeight - 12, 8, Math.max(8, canvasRect.height - tooltipHeight - 8))
  tooltip.style.left = `${left}px`
  tooltip.style.top = `${top}px`
}

function hideChartHover(container: HTMLElement) {
  const layer = container.querySelector<SVGGElement>('.network-hover-layer')
  const tooltip = container.querySelector<HTMLDivElement>('.network-hover-tooltip')
  if (layer) layer.innerHTML = ''
  if (tooltip) tooltip.hidden = true
}

function nearestSeriesPoints(series: Series[], targetTime: number): HoverPoint[] {
  return series
    .map((item) => {
      const point = nearestPoint(item.points, targetTime)
      return point ? { name: item.name, color: item.color, point } : null
    })
    .filter((item): item is HoverPoint => Boolean(item))
}

function nearestPoint(points: SeriesPoint[], targetTime: number) {
  if (points.length === 0) return null
  let low = 0
  let high = points.length - 1

  while (low < high) {
    const mid = Math.floor((low + high) / 2)
    if (points[mid].time < targetTime) low = mid + 1
    else high = mid
  }

  const current = points[low]
  const previous = points[low - 1]
  if (!previous) return current
  return Math.abs(previous.time - targetTime) <= Math.abs(current.time - targetTime) ? previous : current
}

function setChartMessage(container: HTMLElement, message: string) {
  container.innerHTML = `<div class="network-chart-message">${escapeHtml(message)}</div>`
}

function serverId(server: ServerItem) {
  return String(server.ID ?? server.id ?? '')
}

function serverName(server: ServerItem) {
  return String(server.Name ?? server.name ?? serverId(server))
}

function serverCountry(server: ServerItem) {
  return String(
    server.Host?.CountryCode
      ?? server.Host?.country_code
      ?? server.host?.CountryCode
      ?? server.host?.country_code
      ?? server.CountryCode
      ?? server.country_code
      ?? '',
  ).trim().toLowerCase()
}

function serverFlagMarkup(server: ServerItem) {
  const country = cssSafeId(serverCountry(server))
  if (!country) return ''
  return `<span class="country-flag network-server-flag fi fi-${country}" title="${escapeAttribute(country.toUpperCase())}"></span>`
}

function formatTime(input: number, showDate = false) {
  const date = new Date(input)
  const pad = (value: number) => String(value).padStart(2, '0')
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  if (!showDate) return time
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${time}`
}

function formatFullTime(input: number) {
  const date = new Date(input)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function formatMs(value: number) {
  return `${Math.round(value)} ms`
}

function formatNullableMs(value: number | null) {
  return value === null ? '-' : formatMs(value)
}

function formatPercent(value: number) {
  return `${value < 10 ? value.toFixed(2) : value.toFixed(1)}%`
}

function formatLoss(value: number | null) {
  return value === null ? '--' : formatPercent(value)
}

function finiteNumber(value: unknown, fallback: number) {
  const number = Number(value)
  return Number.isFinite(number) ? number : fallback
}

function optionalNumber(value: unknown) {
  const number = Number(value)
  return Number.isFinite(number) ? number : undefined
}

function nullableNumber(value: unknown) {
  if (value === null || value === undefined || value === '') return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}

function monitorPointId(point: MonitorPoint) {
  return String(point.MonitorID ?? point.monitor_id ?? 0)
}

function monitorPointName(point: MonitorPoint, monitorId: string) {
  const name = String(point.MonitorName ?? point.monitor_name ?? point.Name ?? point.name ?? '').trim()
  return name || monitorLabel(monitorId)
}

function monitorLabel(value: string) {
  const devLabels: Record<string, string> = {
    CN: '中国节点',
    US: '北美节点',
    EU: '欧洲节点',
  }
  return devLabels[value] || `Monitor ${value}`
}

function isRangeKey(value: unknown): value is RangeKey {
  return RANGE_ORDER.includes(value as RangeKey)
}

function niceCeil(value: number) {
  if (value <= 10) return 10
  const exponent = Math.floor(Math.log10(value))
  const base = 10 ** exponent
  const normalized = value / base
  const rounded = normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
  return rounded * base
}

function percentile(values: number[], ratio: number) {
  if (values.length === 0) return null
  const sorted = [...values].sort((a, b) => a - b)
  const index = (sorted.length - 1) * clamp(ratio, 0, 1)
  const lower = Math.floor(index)
  const upper = Math.ceil(index)
  if (lower === upper) return sorted[lower]
  const weight = index - lower
  return sorted[lower] * (1 - weight) + sorted[upper] * weight
}

function buildTimeTicks(minTime: number, maxTime: number, count: number) {
  if (count <= 1 || maxTime <= minTime) return [minTime]
  const ticks: number[] = []
  for (let index = 0; index < count; index++) {
    ticks.push(minTime + ((maxTime - minTime) * index) / (count - 1))
  }
  return ticks
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value))
}

function cssSafeId(value: string) {
  return value.replace(/[^a-z0-9-]/g, '')
}

function escapeHtml(value: unknown) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function escapeAttribute(value: unknown) {
  return escapeHtml(value)
}

function throttleFrame(callback: () => void) {
  let scheduled = false
  return () => {
    if (scheduled) return
    scheduled = true
    requestAnimationFrame(() => {
      scheduled = false
      callback()
    })
  }
}
