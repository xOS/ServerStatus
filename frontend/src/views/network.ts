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

type SeriesPoint = {
  time: number
  value: number
  up: number
  down: number
}

type Series = {
  name: string
  color: string
  points: SeriesPoint[]
}

type NetworkSummary = {
  max: number
  min: number
  avg: number
  loss: number | null
  count: number
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

  fetch(apiPath('/profile'), { credentials: 'same-origin', headers: authHeaders(), signal })
    .then((response) => response.json())
    .then((data) => renderChrome(data.data || data))
    .catch((error) => {
      if (!signal.aborted) console.warn('Failed to load profile', error)
    })

  setChartMessage(chartContainer, '加载服务器列表...')

  try {
    const [serversPayload, monitorConfigsPayload] = await Promise.all([
      fetchJson<unknown>(apiPath('/server/list'), signal),
      fetchJson<unknown>(apiPath('/monitor/configs'), signal).catch((error) => {
        if (!signal.aborted) console.warn('Failed to load monitor configs', error)
        return []
      }),
    ])
    const servers = normalizeServers(serversPayload)
    const monitorNames = normalizeMonitorNames(monitorConfigsPayload)
    if (servers.length === 0) {
      buttonsContainer.textContent = ''
      subtitle.textContent = '暂无服务器数据'
      statsContainer.hidden = true
      setChartMessage(chartContainer, '暂无服务器数据')
      return cleanupNetwork
    }

    let currentServerId = serverId(servers[0])
    let currentRange: RangeKey = '24h'
    let currentSeries: Series[] = []
    let currentServerName = serverName(servers[0])
    let chartModel: ChartModel | null = null
    let loadToken = 0
    const historyCache = new Map<string, MonitorPoint[]>()

    buttonsContainer.innerHTML = servers.map((server) => `
      <button class="network-btn" type="button" data-id="${escapeAttribute(serverId(server))}">
        ${serverFlagMarkup(server)}
        <span>${escapeHtml(serverName(server))}</span>
      </button>
    `).join('')

    const updateActiveButton = () => {
      for (const button of buttonsContainer.querySelectorAll<HTMLButtonElement>('.network-btn')) {
        button.classList.toggle('active', button.dataset.id === currentServerId)
      }
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

    const loadChartData = async (id: string) => {
      const token = ++loadToken
      const server = servers.find((item) => serverId(item) === id)
      currentServerName = server ? serverName(server) : `#${id}`
      subtitle.innerHTML = server
        ? `${serverFlagMarkup(server)}<span>${escapeHtml(currentServerName)}</span>`
        : `<span>#${escapeHtml(id)}</span>`
      hideChartHover(chartContainer)
      statsContainer.hidden = true
      chartModel = null
      setChartMessage(chartContainer, '加载监控数据...')

      try {
        const cacheKey = `${id}:${currentRange}`
        let history = historyCache.get(cacheKey)
        if (!history) {
          const payload = await fetchJson<unknown>(`${apiPath(`/monitor/${encodeURIComponent(id)}`)}?range=${currentRange}`, signal)
          history = normalizeHistory(payload, monitorNames)
          historyCache.set(cacheKey, history)
        }
        if (token !== loadToken) return

        if (history.length === 0) {
          currentSeries = []
          statsContainer.hidden = true
          setChartMessage(chartContainer, '该服务器暂无网络监控数据')
          return
        }

        currentSeries = buildSeries(history)
        if (currentSeries.length === 0) {
          statsContainer.hidden = true
          setChartMessage(chartContainer, '该服务器暂无网络监控数据')
          return
        }

        renderSummary(statsContainer, summarizeSeries(currentSeries), currentRange)
        renderCurrentChart()
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

    buttonsContainer.addEventListener('click', (event) => {
      const button = (event.target as HTMLElement).closest<HTMLButtonElement>('.network-btn')
      if (!button || !button.dataset.id || button.dataset.id === currentServerId) return
      currentServerId = button.dataset.id
      updateActiveButton()
      loadChartData(currentServerId)
    }, { signal })

    rangeSwitch.addEventListener('click', (event) => {
      const button = (event.target as HTMLElement).closest<HTMLButtonElement>('.network-range-btn')
      const range = button?.dataset.range
      if (!button || !isRangeKey(range) || range === currentRange) return
      currentRange = range
      updateActiveRange()
      loadChartData(currentServerId)
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

    updateActiveButton()
    updateActiveRange()
    await loadChartData(currentServerId)
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
  const response = await fetch(url, { credentials: 'same-origin', headers: authHeaders(), signal })
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
  let configs: MonitorConfig[] = []
  if (Array.isArray(payload)) {
    configs = payload as MonitorConfig[]
  } else if (payload && typeof payload === 'object') {
    const source = payload as { result?: unknown; data?: unknown; monitors?: unknown }
    if (Array.isArray(source.result)) configs = source.result as MonitorConfig[]
    else if (Array.isArray(source.data)) configs = source.data as MonitorConfig[]
    else if (Array.isArray(source.monitors)) configs = source.monitors as MonitorConfig[]
  }

  for (const item of configs) {
    const id = String(item.ID ?? item.id ?? '').trim()
    const name = String(item.Name ?? item.name ?? '').trim()
    if (id && name) names.set(id, name)
  }
  return names
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

function buildSeries(history: MonitorPoint[]): Series[] {
  const groups = new Map<string, SeriesPoint[]>()

  for (const point of history) {
    const time = Date.parse(point.CreatedAt || point.created_at || '')
    if (!Number.isFinite(time)) continue

    const value = optionalNumber(point.AvgDelay ?? point.avg_delay)
    if (value === undefined || value < 0) continue

    const monitorId = monitorPointId(point)
    const key = monitorPointName(point, monitorId)
    const list = groups.get(key) || []
    list.push({
      time,
      value,
      up: Math.max(0, finiteNumber(point.Up ?? point.up, 0)),
      down: Math.max(0, finiteNumber(point.Down ?? point.down, 0)),
    })
    groups.set(key, list)
  }

  const colors = [
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
  return Array.from(groups.entries()).map(([name, points], index) => ({
    name,
    color: colors[index % colors.length],
    points: points.sort((a, b) => a.time - b.time),
  }))
}

function summarizeSeries(series: Series[]): NetworkSummary | null {
  const points = series.flatMap((item) => item.points)
  const values = points.map((point) => point.value).filter((value) => Number.isFinite(value))
  if (values.length === 0) return null

  const up = points.reduce((sum, point) => sum + point.up, 0)
  const down = points.reduce((sum, point) => sum + point.down, 0)
  const totalChecks = up + down

  return {
    max: Math.max(...values),
    min: Math.min(...values),
    avg: values.reduce((sum, value) => sum + value, 0) / values.length,
    loss: totalChecks > 0 ? (down / totalChecks) * 100 : null,
    count: values.length,
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
      <strong>${formatMs(summary.avg)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="max">
      <span>${RANGE_LABELS[range]}最高</span>
      <strong>${formatMs(summary.max)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="min">
      <span>${RANGE_LABELS[range]}最低</span>
      <strong>${formatMs(summary.min)}</strong>
    </div>
    <div class="network-stat-item" data-network-stat="loss">
      <span>${RANGE_LABELS[range]}丢包</span>
      <strong>${formatLoss(summary.loss)}</strong>
    </div>
  `
}

function renderSvgChart(container: HTMLElement, series: Series[], serverNameValue: string, range: RangeKey): ChartModel | null {
  const width = Math.max(container.clientWidth, 320)
  const compact = width <= 520
  const height = compact ? 360 : 420
  const padding = compact
    ? { top: 18, right: 10, bottom: 36, left: 34 }
    : { top: 24, right: 22, bottom: 42, left: 48 }
  const plotWidth = width - padding.left - padding.right
  const plotHeight = height - padding.top - padding.bottom

  const allPoints = series.flatMap((item) => item.points)
  if (allPoints.length === 0) {
    setChartMessage(container, '该服务器暂无网络监控数据')
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

  const paths = series.map((item) => {
    const d = item.points
      .map((point, index) => `${index === 0 ? 'M' : 'L'} ${x(point.time).toFixed(1)} ${y(point.value).toFixed(1)}`)
      .join(' ')
    return `<path class="network-line" d="${d}" stroke="${item.color}"></path>`
  }).join('')

  const legend = series.map((item) => `
    <span class="network-legend-item">
      <span class="network-legend-swatch" style="background:${item.color}"></span>${escapeHtml(item.name)}
    </span>
  `).join('')

  const grid = yTicks.map((tick) => {
    const yy = y(tick)
    return `
      <line class="network-grid-line" x1="${padding.left}" y1="${yy.toFixed(1)}" x2="${(width - padding.right).toFixed(1)}" y2="${yy.toFixed(1)}"></line>
      <text class="network-axis-label" x="${padding.left - 10}" y="${(yy + 4).toFixed(1)}" text-anchor="end">${tick}</text>
    `
  }).join('')

  container.innerHTML = `
    <div class="network-chart-canvas">
      <svg class="network-chart-svg" style="height:${height}px" viewBox="0 0 ${width} ${height}" role="img" aria-label="${RANGE_LABELS[range]}网络延迟折线图">
        ${grid}
        <line class="network-axis-line" x1="${padding.left}" y1="${padding.top}" x2="${padding.left}" y2="${height - padding.bottom}"></line>
        <line class="network-axis-line" x1="${padding.left}" y1="${height - padding.bottom}" x2="${width - padding.right}" y2="${height - padding.bottom}"></line>
        <text class="network-axis-label" x="${padding.left}" y="${height - 14}" text-anchor="start">${formatTime(minTime, showDateLabel)}</text>
        <text class="network-axis-label" x="${width - padding.right}" y="${height - 14}" text-anchor="end">${formatTime(maxTime, showDateLabel)}</text>
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
    series,
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
