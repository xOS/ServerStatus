import { injectAppShell, renderChrome, icon } from './layout'
import { apiPath, apiWebSocketUrl } from './api'
import { authHeaders } from './auth'
import './style.css'

type ServerId = string

export interface ProfileResponse {
  Admin?: {
    Name?: string
    Login?: string
    AvatarURL?: string
  }
  Conf?: {
    Site?: {
      Brand?: string
      CustomCode?: string
      CustomCodeDashboard?: string
      FooterYear?: string
      FooterName?: string
      FooterURL?: string
    }
    Login?: {
      EnableOAuth?: boolean
      EnableAPIKey?: boolean
    }
  }
  CustomCode?: string
}

interface HostState {
  CPU?: number
  MemUsed?: number
  SwapUsed?: number
  DiskUsed?: number
  NetInTransfer?: number
  NetOutTransfer?: number
  NetInSpeed?: number
  NetOutSpeed?: number
  Uptime?: number
  Load1?: number
  Load5?: number
  Load15?: number
  TcpConnCount?: number
  UdpConnCount?: number
  ProcessCount?: number
  Temperatures?: Array<{ Name?: string; Temperature?: number }>
  GPU?: number
}

interface HostInfo {
  OS?: string
  Platform?: string
  PlatformVersion?: string
  CPU?: string[]
  MemTotal?: number
  DiskTotal?: number
  SwapTotal?: number
  Arch?: string
  Virtualization?: string
  BootTime?: number
  CountryCode?: string
  Version?: string
  GPU?: string[]
}

interface ServerInfo {
  ID: number | string
  Name?: string
  Tag?: string
  Host?: HostInfo | null
  State?: HostState | null
  LastActive?: string | number
  IsOnline?: boolean
  is_online?: boolean
  live?: boolean
}

interface TrafficWireItem {
  server_id?: number | string
  server_name?: string
  max_bytes?: number
  used_bytes?: number
  max_formatted?: string
  used_formatted?: string
  used_percent?: number
  percent?: number
  cycle_name?: string
}

interface TrafficView {
  max: string
  used: string
  percent: number
  serverName: string
  cycleName: string
}

interface WebSocketFrame {
  now?: number
  servers?: ServerInfo[]
  trafficData?: TrafficWireItem[] | Record<string, TrafficView>
  type?: string
}

interface BootstrapPayload {
  profile?: ProfileResponse
  servers?: unknown
  now?: number
}

interface CardRefs {
  root: HTMLElement
  content: HTMLElement
  loading: HTMLElement
  titleName: HTMLElement
  offline: HTMLElement
  country: HTMLElement
  osIcon: HTMLElement
  cpuTrack: HTMLElement
  cpuA: HTMLElement
  cpuB: HTMLElement
  memTotal: HTMLElement
  diskTotal: HTMLElement
  cpuBar: HTMLElement
  cpuLabel: HTMLElement
  memBar: HTMLElement
  memLabel: HTMLElement
  diskBar: HTMLElement
  diskLabel: HTMLElement
  trafficBar: HTMLElement
  trafficLabel: HTMLElement
  netInSpeed: HTMLElement
  netOutSpeed: HTMLElement
  netInTransfer: HTMLElement
  netOutTransfer: HTMLElement
  processCount: HTMLElement
  tcpCount: HTMLElement
  udpCount: HTMLElement
  uptime: HTMLElement
  loadingName: HTMLElement
  cache: Map<string, string>
}

interface GroupStat {
  total: number
  offline: number
}

const LABELS = {
  'zh-CN': {
    Platform: '架构',
    MemUsed: '内存',
    SwapUsed: '交换',
    DiskUsed: '硬盘',
    TrafficTotal: '流量',
    GPU: 'GPU',
    Temperature: '温度',
    Load: '负载',
    ConnCount: '连接',
    BootTime: '启动',
    LastActive: '活动',
    Version: '版本',
    Count: '个',
    Day: '天',
  },
  en: {
    Platform: 'Platform',
    MemUsed: 'MemUsed',
    SwapUsed: 'SwapUsed',
    DiskUsed: 'DiskUsed',
    TrafficTotal: 'TrafficTotal',
    GPU: 'GPU',
    Temperature: 'Temperature',
    Load: 'Load',
    ConnCount: 'ConnCount',
    BootTime: 'BootTime',
    LastActive: 'LastActive',
    Version: 'Version',
    Count: 'Count',
    Day: 'Day',
  },
} as const

type LabelKey = keyof typeof LABELS['zh-CN']

const SERVER_SNAPSHOT_KEY = 'serverstatus:home:snapshot:v1'
const SERVER_SNAPSHOT_MAX_AGE = 5 * 60 * 1000

interface ServerSnapshot {
  savedAt: number
  apiBase: string
  servers: ServerInfo[]
}

const state = {
  profile: null as ProfileResponse | null,
  servers: [] as ServerInfo[],
  serverById: new Map<ServerId, ServerInfo>(),
  trafficById: new Map<ServerId, TrafficView>(),
  cards: new Map<ServerId, CardRefs>(),
  activeTag: '',
  visibleIds: new Set<ServerId>(),
  tabsSignature: '',
  pendingFrame: null as WebSocketFrame | null,
  frameScheduled: false,
  ws: null as WebSocket | null,
  reconnectTimer: 0,
  reconnectDelay: 3000,
  tooltipServerId: '' as ServerId,
  snapshotAllowed: false,
}

let app: HTMLDivElement
let tabs: HTMLElement | null = null
let tabsContent: HTMLElement | null = null
let tabSlider: HTMLElement | null = null
let cards: HTMLElement | null = null
let emptyState: HTMLElement | null = null
let emptyTitle: HTMLElement | null = null
let emptyText: HTMLElement | null = null
let tooltip: HTMLElement | null = null
let tabsClickHandler: ((event: MouseEvent) => void) | undefined
let cardsPointerOverHandler: ((event: PointerEvent) => void) | undefined
let cardsPointerMoveHandler: ((event: PointerEvent) => void) | undefined
let cardsPointerOutHandler: ((event: PointerEvent) => void) | undefined
let visibilityHandler: (() => void) | undefined
let resizeHandler: (() => void) | undefined
let activeHomeToken = 0

export function initHome(container: HTMLDivElement) {
  cleanupHome()
  const token = ++activeHomeToken
  app = container
  state.tabsSignature = ''

  const { contentArea } = injectAppShell(container, 'home')
  
  contentArea.innerHTML = `
    <div class="tabs-wrapper">
      <div class="custom-tabs-container" id="tabs" hidden>
        <div class="tabs-content-area" id="tabs-content"></div>
        <div class="tab-slider" id="tab-slider"></div>
      </div>
    </div>
    <section class="status-cards" id="cards" aria-live="polite"></section>
    <section class="empty-state" id="empty-state" hidden>
      <div class="empty-state-header" id="empty-title">没有服务器</div>
      <p class="empty-state-text" id="empty-text">请先在后台添加服务器</p>
    </section>
    <div class="server-tooltip" id="tooltip" role="tooltip" hidden></div>
  `


  tabs = requiredElement('tabs')
  tabsContent = requiredElement('tabs-content')
  tabSlider = requiredElement('tab-slider')
  cards = requiredElement('cards')
  emptyState = requiredElement('empty-state')
  emptyTitle = requiredElement('empty-title')
  emptyText = requiredElement('empty-text')
  tooltip = requiredElement('tooltip')

  init(token)
  return cleanupHome
}

const cpuObserver = 'IntersectionObserver' in window
  ? new IntersectionObserver((entries) => {
      for (const entry of entries) {
        entry.target.classList.toggle('is-paused', !entry.isIntersecting)
      }
    }, { rootMargin: '120px' })
  : null


async function init(token: number) {
  bindEvents()
  resetServerState()
  restoreServerSnapshot()
  renderChrome(state.profile)
  if (state.servers.length > 0) {
    renderCards()
    renderTabs()
    applyFilter()
  }
  renderEmptyState()
  connectWebSocket()

  void loadBootstrap().then(() => {
    if (token !== activeHomeToken) return
    renderChrome(state.profile)
    renderCards()
    renderTabs()
    applyFilter()
  }).catch(() => {
    if (token !== activeHomeToken) return
    void loadProfile().then(() => {
      if (token !== activeHomeToken) return
      renderChrome(state.profile)
    })
    void loadServers().then(() => {
      if (token !== activeHomeToken) return
      renderCards()
      renderTabs()
      applyFilter()
    })
  })
}

function resetServerState() {
  state.servers = []
  state.serverById.clear()
  state.cards.clear()
  state.trafficById.clear()
  state.visibleIds.clear()
  state.snapshotAllowed = false
}

function bindEvents() {
  if (!tabs || !tabsContent || !cards) return

  const tabsEl = tabs
  const tabsContentEl = tabsContent
  const cardsEl = cards

  tabsClickHandler = (event) => {
    const target = (event.target as HTMLElement).closest<HTMLButtonElement>('[data-tab]')
    if (!target) return

    state.activeTag = target.dataset.tab || ''
    renderTabs()
    applyFilter()

    if (tabsEl.scrollWidth > tabsEl.clientWidth) {
      target.scrollIntoView({ behavior: 'smooth', inline: 'center', block: 'nearest' })
    }
  }
  tabsContentEl.addEventListener('click', tabsClickHandler)

  cardsPointerOverHandler = (event) => {
    const trigger = (event.target as HTMLElement).closest<HTMLElement>('[data-tooltip-server]')
    if (!trigger) return
    const id = trigger.dataset.tooltipServer || ''
    state.tooltipServerId = id
    showTooltip(id, event as PointerEvent)
  }
  cardsEl.addEventListener('pointerover', cardsPointerOverHandler)

  cardsPointerMoveHandler = (event) => {
    if (!state.tooltipServerId) return
    positionTooltip(event as PointerEvent)
  }
  cardsEl.addEventListener('pointermove', cardsPointerMoveHandler)

  cardsPointerOutHandler = (event) => {
    const trigger = (event.target as HTMLElement).closest<HTMLElement>('[data-tooltip-server]')
    if (!trigger) return
    const next = event.relatedTarget as Node | null
    if (next && trigger.contains(next)) return
    hideTooltip()
  }
  cardsEl.addEventListener('pointerout', cardsPointerOutHandler)

  visibilityHandler = () => {
    if (!document.hidden && state.pendingFrame) {
      scheduleFrame(state.pendingFrame)
    }
  }
  document.addEventListener('visibilitychange', visibilityHandler)

  resizeHandler = throttleFrame(() => {
    updateTabSlider()
    refreshCpuOverflow()
    refreshProgressLabels()
  })
  window.addEventListener('resize', resizeHandler)
}

function cleanupHome() {
  activeHomeToken += 1
  if (tooltip) hideTooltip()
  window.clearTimeout(state.reconnectTimer)
  state.reconnectTimer = 0

  if (state.ws) {
    const ws = state.ws
    state.ws = null
    ws.close()
  }

  if (tabs && tabsClickHandler) tabs.removeEventListener('click', tabsClickHandler)
  if (cards && cardsPointerOverHandler) cards.removeEventListener('pointerover', cardsPointerOverHandler)
  if (cards && cardsPointerMoveHandler) cards.removeEventListener('pointermove', cardsPointerMoveHandler)
  if (cards && cardsPointerOutHandler) cards.removeEventListener('pointerout', cardsPointerOutHandler)
  if (visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler)
  if (resizeHandler) window.removeEventListener('resize', resizeHandler)

  for (const refs of state.cards.values()) {
    cpuObserver?.unobserve(refs.cpuTrack)
  }
  cpuMeasureSet.clear()
  cpuMeasureQueued = false

  tabsClickHandler = undefined
  cardsPointerOverHandler = undefined
  cardsPointerMoveHandler = undefined
  cardsPointerOutHandler = undefined
  visibilityHandler = undefined
  resizeHandler = undefined
  state.tooltipServerId = ''
  state.pendingFrame = null
  state.frameScheduled = false
  tabs = null
  tabsContent = null
  tabSlider = null
  cards = null
  emptyState = null
  emptyTitle = null
  emptyText = null
  tooltip = null
}

async function loadProfile() {
  try {
    state.profile = await fetchJson<ProfileResponse>(apiPath('/profile'))
  } catch (error) {
    console.warn('Failed to load profile', error)
  }
}

async function loadBootstrap() {
  const payload = await fetchJson<BootstrapPayload>(apiPath('/bootstrap'))
  if (payload.profile) state.profile = payload.profile
  state.snapshotAllowed = !state.profile?.Admin
  applyServerPayload(payload.servers, payload.now)
}

async function loadServers() {
  try {
    const payload = await fetchJson<unknown>(apiPath('/server/list'))
    applyServerPayload(payload)
  } catch (error) {
    console.warn('Failed to load servers', error)
    state.servers = []
    state.serverById.clear()
  }
}

function applyServerPayload(payload: unknown, now?: number) {
  const list = normalizeServerList(payload)
  state.servers = list
  state.serverById.clear()
  for (const server of list) {
    server.live = isServerLive(server, now)
    state.serverById.set(serverId(server), server)
  }
  storeServerSnapshot(list)
}

function restoreServerSnapshot() {
  let snapshot: ServerSnapshot | null = null
  try {
    snapshot = JSON.parse(sessionStorage.getItem(SERVER_SNAPSHOT_KEY) || 'null') as ServerSnapshot | null
  } catch {
    return
  }

  if (!snapshot || snapshot.apiBase !== apiPath('') || Date.now() - snapshot.savedAt > SERVER_SNAPSHOT_MAX_AGE) return
  const list = normalizeServerList(snapshot.servers)
  if (list.length === 0) return
  state.servers = list
  state.serverById.clear()
  for (const server of list) {
    server.live = isServerLive(server)
    state.serverById.set(serverId(server), server)
  }
}

function storeServerSnapshot(list: ServerInfo[]) {
  if (!state.snapshotAllowed || state.profile?.Admin) return
  try {
    sessionStorage.setItem(SERVER_SNAPSHOT_KEY, JSON.stringify({
      savedAt: Date.now(),
      apiBase: apiPath(''),
      servers: list,
    } satisfies ServerSnapshot))
  } catch {
    // Snapshot is only a first-paint optimization.
  }
}

async function fetchJson<T>(url: string): Promise<T> {
  const response = await fetch(url, { credentials: 'include', headers: authHeaders() })
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

function normalizeServerList(payload: unknown): ServerInfo[] {
  let list: ServerInfo[] = []
  if (Array.isArray(payload)) {
    list = payload as ServerInfo[]
  } else if (payload && typeof payload === 'object') {
    const source = payload as { result?: unknown; servers?: unknown; data?: unknown }
    if (Array.isArray(source.result)) {
      list = source.result as ServerInfo[]
    } else if (source.result && typeof source.result === 'object') {
      const result = source.result as { servers?: unknown }
      if (Array.isArray(result.servers)) list = result.servers as ServerInfo[]
    } else if (Array.isArray(source.servers)) {
      list = source.servers as ServerInfo[]
    } else if (Array.isArray(source.data)) {
      list = source.data as ServerInfo[]
    }
  }

  return list.map(normalizeServerInfo)
}

function normalizeServerInfo(server: ServerInfo): ServerInfo {
  const host = server.Host
    ? {
        ...server.Host,
        CPU: Array.isArray(server.Host.CPU) ? [...server.Host.CPU] : [],
        GPU: Array.isArray(server.Host.GPU) ? [...server.Host.GPU] : [],
      }
    : server.Host
  return {
    ...server,
    Host: host,
    State: server.State
      ? {
          ...server.State,
          Temperatures: Array.isArray(server.State.Temperatures) ? [...server.State.Temperatures] : [],
        }
      : server.State,
  }
}

function renderCards() {
  if (!cards) return

  cards.textContent = ''
  state.cards.clear()
  state.visibleIds.clear()

  for (const server of state.servers) {
    const refs = createServerCard(server)
    state.cards.set(serverId(server), refs)
    cards.append(refs.root)
    updateCard(server, refs)
    if (cpuObserver) cpuObserver.observe(refs.cpuTrack)
  }

  refreshCpuOverflow()
}

function createServerCard(server: ServerInfo): CardRefs {
  const id = serverId(server)
  const root = document.createElement('article')
  root.className = 'server-card'
  root.id = `server-${cssSafeId(id)}`
  root.dataset.serverId = id
  root.innerHTML = `
    <div class="server-card-content" data-ref="content">
      <div class="card-header">
        <div class="server-title">
          <span class="country-flag" data-ref="country"></span>
          <span class="os-icon" data-ref="osIcon"></span>
          <span class="server-name" data-ref="titleName"></span>
          <span class="offline-label" data-ref="offline">[离线]</span>
        </div>
        <button class="info-button" type="button" aria-label="服务器详情" data-tooltip-server="${escapeAttribute(id)}">
          ${icon('info')}
        </button>
      </div>

      <div class="card-divider"></div>

      <div class="host-line">
        ${icon('cpu', 'metric-svg icon-blue')}
        <div class="cpu-track" data-ref="cpuTrack">
          <span class="cpu-copy" data-ref="cpuA"></span>
          <span class="cpu-copy" data-ref="cpuB"></span>
        </div>
        <div class="host-capacity">
          ${icon('memory', 'metric-svg icon-orange')}<span data-ref="memTotal"></span>
          <span class="capacity-space"></span>
          ${icon('disk', 'metric-svg icon-purple')}<span data-ref="diskTotal"></span>
        </div>
      </div>

      <div class="description">
        ${progressRow('CPU', 'cpu')}
        ${progressRow('内存', 'mem')}
        ${progressRow('硬盘', 'disk')}
        ${progressRow('流量', 'traffic')}

        <div class="metric-row">
          <div class="metric-label">速率</div>
          <div class="metric-value">
            ${icon('down', 'metric-svg icon-green')}<span data-ref="netInSpeed"></span><span>/s</span>
            <span class="metric-separator">|</span>
            ${icon('up', 'metric-svg icon-red')}<span data-ref="netOutSpeed"></span><span>/s</span>
          </div>
        </div>

        <div class="metric-row">
          <div class="metric-label">传输</div>
          <div class="metric-value">
            ${icon('downSolid', 'metric-svg icon-green')}<span data-ref="netInTransfer"></span>
            <span class="metric-separator">|</span>
            ${icon('upSolid', 'metric-svg icon-red')}<span data-ref="netOutTransfer"></span>
          </div>
        </div>

        <div class="metric-row">
          <div class="metric-label">动态</div>
          <div class="metric-value">
            ${icon('activity', 'metric-svg icon-violet')}<span data-ref="processCount"></span>
            <span class="metric-separator">|</span>
            ${icon('tcp', 'metric-svg icon-green')}<span data-ref="tcpCount"></span>
            <span class="metric-separator">|</span>
            ${icon('udp', 'metric-svg icon-red')}<span data-ref="udpCount"></span>
          </div>
        </div>

        <div class="metric-row">
          <div class="metric-label">在线</div>
          <div class="metric-value">
            ${icon('clock', 'metric-svg icon-orange')}<span data-ref="uptime"></span>
          </div>
        </div>
      </div>
    </div>

    <div class="server-card-loading" data-ref="loading">
      <p data-ref="loadingName"></p>
      <p><span class="spinner"></span> 连接中...</p>
    </div>
  `

  return {
    root,
    content: ref(root, 'content'),
    loading: ref(root, 'loading'),
    titleName: ref(root, 'titleName'),
    offline: ref(root, 'offline'),
    country: ref(root, 'country'),
    osIcon: ref(root, 'osIcon'),
    cpuTrack: ref(root, 'cpuTrack'),
    cpuA: ref(root, 'cpuA'),
    cpuB: ref(root, 'cpuB'),
    memTotal: ref(root, 'memTotal'),
    diskTotal: ref(root, 'diskTotal'),
    cpuBar: ref(root, 'cpuBar'),
    cpuLabel: ref(root, 'cpuLabel'),
    memBar: ref(root, 'memBar'),
    memLabel: ref(root, 'memLabel'),
    diskBar: ref(root, 'diskBar'),
    diskLabel: ref(root, 'diskLabel'),
    trafficBar: ref(root, 'trafficBar'),
    trafficLabel: ref(root, 'trafficLabel'),
    netInSpeed: ref(root, 'netInSpeed'),
    netOutSpeed: ref(root, 'netOutSpeed'),
    netInTransfer: ref(root, 'netInTransfer'),
    netOutTransfer: ref(root, 'netOutTransfer'),
    processCount: ref(root, 'processCount'),
    tcpCount: ref(root, 'tcpCount'),
    udpCount: ref(root, 'udpCount'),
    uptime: ref(root, 'uptime'),
    loadingName: ref(root, 'loadingName'),
    cache: new Map(),
  }
}

function progressRow(label: string, key: string) {
  return `
    <div class="metric-row">
      <div class="metric-label">${label}</div>
      <div class="metric-progress">
        <div class="progress-track ui progress">
          <div class="progress-fill bar" data-ref="${key}Bar"><small data-ref="${key}Label"></small></div>
        </div>
      </div>
    </div>
  `
}

function updateCard(server: ServerInfo, refs: CardRefs) {
  const host = server.Host
  const stateInfo = server.State || {}
  const live = isServerLive(server)
  server.live = live

  setVisible(refs.content, Boolean(host), 'content-visible', refs.cache)
  setVisible(refs.loading, !host, 'loading-visible', refs.cache)
  setText(refs.titleName, server.Name || '', 'name', refs.cache)
  setText(refs.loadingName, server.Name || '', 'loading-name', refs.cache)
  setVisible(refs.offline, !live, 'offline-visible', refs.cache)

  if (!host) return

  const country = (host.CountryCode || '').toLowerCase()
  setFlag(refs.country, country, refs.cache)

  const osMarkup = osIconMarkup(host.Platform || '', stateInfo.Uptime || 0)
  setHtml(refs.osIcon, osMarkup, 'os', refs.cache)

  const cpuText = normalizeCpu(host.CPU)
  const cpuDisplay = `${cpuText}        `
  setText(refs.cpuA, cpuDisplay, 'cpu-a', refs.cache)
  setText(refs.cpuB, cpuDisplay, 'cpu-b', refs.cache)

  setText(refs.memTotal, getK2Gb(host.MemTotal), 'mem-total', refs.cache)
  setText(refs.diskTotal, getK2Gb(host.DiskTotal), 'disk-total', refs.cache)

  setProgress(refs.cpuBar, refs.cpuLabel, percent(live, stateInfo.CPU, 100), live, 'cpu-progress', refs.cache)
  setProgress(refs.memBar, refs.memLabel, percent(live, stateInfo.MemUsed, host.MemTotal), live, 'mem-progress', refs.cache)
  setProgress(refs.diskBar, refs.diskLabel, percent(live, stateInfo.DiskUsed, host.DiskTotal), live, 'disk-progress', refs.cache)

  const traffic = trafficFor(server)
  setProgress(refs.trafficBar, refs.trafficLabel, traffic.percent, live, 'traffic-progress', refs.cache, trafficLabel(traffic))

  setText(refs.netInSpeed, formatByteSize(stateInfo.NetInSpeed), 'net-in-speed', refs.cache)
  setText(refs.netOutSpeed, formatByteSize(stateInfo.NetOutSpeed), 'net-out-speed', refs.cache)
  setText(refs.netInTransfer, formatByteSize(stateInfo.NetInTransfer), 'net-in-transfer', refs.cache)
  setText(refs.netOutTransfer, formatByteSize(stateInfo.NetOutTransfer), 'net-out-transfer', refs.cache)
  setText(refs.processCount, String(toNumber(stateInfo.ProcessCount)), 'process', refs.cache)
  setText(refs.tcpCount, String(toNumber(stateInfo.TcpConnCount)), 'tcp', refs.cache)
  setText(refs.udpCount, String(toNumber(stateInfo.UdpConnCount)), 'udp', refs.cache)
  setText(refs.uptime, secondToDate(stateInfo.Uptime), 'uptime', refs.cache)

  queueCpuMeasure(refs)
}

function setProgress(
  bar: HTMLElement,
  label: HTMLElement,
  value: number,
  live: boolean,
  key: string,
  cache: Map<string, string>,
  labelText?: string,
) {
  const safeValue = clamp(value, 0, 100)
  const width = `${formatProgressWidth(safeValue)}%`
  const tone = live ? progressTone(safeValue) : 'offline'
  const displayText = labelText ?? formatProgressLabel(safeValue)
  const signature = `${width}|${tone}|${displayText}`
  if (cache.get(key) === signature) return
  cache.set(key, signature)
  const track = bar.parentElement
  if (track) {
    track.className = `progress-track ui progress ${tone}`
  }
  bar.style.width = width
  bar.style.minWidth = '24px'
  bar.className = 'progress-fill bar'
  label.textContent = displayText
  queueProgressLabelMeasure(bar, label)
}

function queueProgressLabelMeasure(bar: HTMLElement, label: HTMLElement) {
  requestAnimationFrame(() => {
    const fillWidth = bar.clientWidth
    if (fillWidth <= 0) {
      label.style.removeProperty('--progress-label-right')
      return
    }

    const edgeInset = 7
    const labelWidth = label.scrollWidth
    const defaultLeft = fillWidth - edgeInset - labelWidth
    if (defaultLeft >= edgeInset) {
      label.style.removeProperty('--progress-label-right')
      return
    }

    label.style.setProperty('--progress-label-right', `${fillWidth - edgeInset - labelWidth}px`)
  })
}

function renderTabs() {
  const stats = groupStats()
  const tagNames = Array.from(stats.tags.keys()).reverse()
  const signature = JSON.stringify({
    active: state.activeTag,
    all: stats.all,
    online: stats.online,
    offline: stats.offline,
    tags: tagNames.map((tag) => [tag, stats.tags.get(tag)]),
  })

  if (signature === state.tabsSignature) {
    updateTabSlider()
    return
  }

  state.tabsSignature = signature
  if (!tabsContent || !tabs || !tabSlider) return

  tabs.hidden = state.servers.length === 0
  tabsContent.textContent = ''

  tabsContent.append(
    tabButton('', '全部', stats.all, 'all'),
    tabButton('online', '在线', stats.online, 'online'),
    tabButton('offline', '离线', stats.offline, 'offline'),
  )

  for (const tag of tagNames) {
    tabsContent.append(tabButton(tag, tag, stats.tags.get(tag) || { total: 0, offline: 0 }, 'custom'))
  }

  requestAnimationFrame(updateTabSlider)
}

function tabButton(tag: string, label: string, stat: GroupStat, kind: string) {
  const button = document.createElement('button')
  button.type = 'button'
  button.className = `custom-tab${state.activeTag === tag ? ' active' : ''}`
  button.dataset.tab = tag
  button.dataset.tag = kind
  const badgeStyle = badgeInlineStyle(stat)
  button.innerHTML = `
    <span>${escapeHtml(label)}</span>
    <span class="server-count-badge"${badgeStyle ? ` style="${badgeStyle}"` : ''}>${stat.total}</span>
  `
  return button
}

function applyFilter() {
  state.visibleIds.clear()

  for (const server of state.servers) {
    const id = serverId(server)
    const refs = state.cards.get(id)
    if (!refs) continue

    const visible = serverMatchesActiveTab(server)
    refs.root.hidden = !visible
    if (visible) {
      state.visibleIds.add(id)
      updateCard(server, refs)
    }
  }

  renderEmptyState()
  updateTabSlider()
  refreshCpuOverflow()
}

function renderEmptyState() {
  if (!emptyState || !cards || !emptyTitle || !emptyText) return

  const hasServers = state.servers.length > 0
  const hasVisible = state.visibleIds.size > 0

  emptyState.hidden = hasServers && hasVisible
  cards.hidden = !hasServers

  if (!hasServers || state.activeTag === '') {
    emptyTitle.textContent = '没有服务器'
    emptyText.textContent = '请先在后台添加服务器'
    return
  }

  if (state.activeTag === 'online') {
    emptyTitle.textContent = '没有在线服务器'
    emptyText.textContent = ''
  } else if (state.activeTag === 'offline') {
    emptyTitle.textContent = '没有离线服务器'
    emptyText.textContent = ''
  } else {
    emptyTitle.textContent = `没有该分组的服务器 "${state.activeTag}"`
    emptyText.textContent = ''
  }
}

function connectWebSocket() {
  if (state.ws && state.ws.readyState <= WebSocket.OPEN) return

  const ws = new WebSocket(apiWebSocketUrl('/ws'))
  state.ws = ws

  ws.addEventListener('open', () => {
    state.reconnectDelay = 3000
  })

  ws.addEventListener('message', (event) => {
    let frame: WebSocketFrame
    try {
      frame = JSON.parse(event.data) as WebSocketFrame
    } catch (error) {
      console.warn('Invalid WebSocket payload', error)
      return
    }

    if (frame.type === 'pong') return

    if (document.hidden) {
      state.pendingFrame = mergeFrame(state.pendingFrame, frame)
      return
    }

    scheduleFrame(frame)
  })

  ws.addEventListener('close', () => {
    if (state.ws !== ws) return
    window.clearTimeout(state.reconnectTimer)
    state.reconnectTimer = window.setTimeout(connectWebSocket, state.reconnectDelay)
    state.reconnectDelay = Math.min(state.reconnectDelay * 1.5, 15000)
  })
}

function scheduleFrame(frame: WebSocketFrame) {
  state.pendingFrame = mergeFrame(state.pendingFrame, frame)
  if (state.frameScheduled) return

  state.frameScheduled = true
  requestAnimationFrame(() => {
    state.frameScheduled = false
    const next = state.pendingFrame
    state.pendingFrame = null
    if (!next) return
    applyFrame(next)
  })
}

function mergeFrame(previous: WebSocketFrame | null, next: WebSocketFrame): WebSocketFrame {
  if (!previous) return next
  return {
    now: next.now ?? previous.now,
    servers: next.servers ?? previous.servers,
    trafficData: next.trafficData ?? previous.trafficData,
    type: next.type ?? previous.type,
  }
}

function applyFrame(frame: WebSocketFrame) {
  let cardStructureChanged = false

  if (Array.isArray(frame.servers)) {
    cardStructureChanged = updateServerData(frame.servers, frame.now)
  }

  if (frame.trafficData) {
    updateTrafficData(frame.trafficData)
  }

  if (cardStructureChanged) {
    renderCards()
  }

  renderTabs()
  applyFilter()
  refreshTooltipContent()
}

function updateServerData(incoming: ServerInfo[], now?: number) {
  const incomingById = new Map<ServerId, ServerInfo>()
  let structureChanged = false

  for (const server of incoming) {
    const normalized = normalizeServerInfo(server)
    incomingById.set(serverId(normalized), normalized)
  }

  for (const server of state.servers) {
    const id = serverId(server)
    const liveData = incomingById.get(id)

    if (!liveData) {
      server.live = false
      continue
    }

    const live = isServerLive(liveData, now)
    server.live = live
    server.State = liveData.State || server.State || {}
    server.Host = liveData.Host || server.Host || null
    server.LastActive = liveData.LastActive || server.LastActive
    server.Name = liveData.Name || server.Name
    server.Tag = liveData.Tag ?? server.Tag
    incomingById.delete(id)
  }

  for (const [id, server] of incomingById) {
    server.live = isServerLive(server, now)
    state.servers.push(server)
    state.serverById.set(id, server)
    structureChanged = true
  }

  return structureChanged
}

function updateTrafficData(payload: TrafficWireItem[] | Record<string, TrafficView>) {
  if (Array.isArray(payload)) {
    for (const item of payload) {
      if (item.server_id === undefined || item.server_id === null) continue
      const maxBytes = toNumber(item.max_bytes)
      const usedBytes = toNumber(item.used_bytes)
      const percent = finiteNumber(item.used_percent ?? item.percent, maxBytes > 0 ? (usedBytes / maxBytes) * 100 : 0)
      state.trafficById.set(String(item.server_id), {
        max: item.max_formatted || formatByteSize(maxBytes),
        used: item.used_formatted || formatByteSize(usedBytes),
        percent: clamp(percent, 0, 100),
        serverName: item.server_name || '',
        cycleName: item.cycle_name || '',
      })
    }
    return
  }

  for (const [id, value] of Object.entries(payload)) {
    state.trafficById.set(String(id), {
      max: value.max || '0B',
      used: value.used || '0B',
      percent: clamp(finiteNumber(value.percent, 0), 0, 100),
      serverName: value.serverName || '',
      cycleName: value.cycleName || '',
    })
  }
}

function groupStats() {
  const all: GroupStat = { total: 0, offline: 0 }
  const online: GroupStat = { total: 0, offline: 0 }
  const offline: GroupStat = { total: 0, offline: 0 }
  const tags = new Map<string, GroupStat>()

  for (const server of state.servers) {
    if (!server.Host) continue
    const live = isServerLive(server)
    all.total += 1
    if (!live) all.offline += 1

    if (live) {
      online.total += 1
    } else {
      offline.total += 1
      offline.offline += 1
    }

    const tag = (server.Tag || '').trim()
    if (tag) {
      const stat = tags.get(tag) || { total: 0, offline: 0 }
      stat.total += 1
      if (!live) stat.offline += 1
      tags.set(tag, stat)
    }
  }

  return { all, online, offline, tags }
}

function serverMatchesActiveTab(server: ServerInfo) {
  if (!server.Host) return false
  if (!state.activeTag) return true
  if (state.activeTag === 'online') return isServerLive(server)
  if (state.activeTag === 'offline') return !isServerLive(server)
  return server.Tag === state.activeTag
}

function updateTabSlider() {
  if (!tabsContent || !tabs || !tabSlider) return

  const active = tabsContent.querySelector<HTMLElement>('.custom-tab.active')
  if (!active || tabs.hidden) {
    tabSlider.style.width = '0px'
    return
  }

  const containerRect = tabs.getBoundingClientRect()
  const tabRect = active.getBoundingClientRect()
  tabSlider.style.left = `${tabRect.left - containerRect.left + tabs.scrollLeft}px`
  tabSlider.style.width = `${tabRect.width}px`
}

let cpuMeasureQueued = false
const cpuMeasureSet = new Set<CardRefs>()

function queueCpuMeasure(refs: CardRefs) {
  cpuMeasureSet.add(refs)
  if (cpuMeasureQueued) return
  cpuMeasureQueued = true
  requestAnimationFrame(() => {
    cpuMeasureQueued = false
    for (const item of cpuMeasureSet) {
      measureCpuOverflow(item)
    }
    cpuMeasureSet.clear()
  })
}

function refreshCpuOverflow() {
  for (const refs of state.cards.values()) {
    queueCpuMeasure(refs)
  }
}

function refreshProgressLabels() {
  for (const refs of state.cards.values()) {
    queueProgressLabelMeasure(refs.cpuBar, refs.cpuLabel)
    queueProgressLabelMeasure(refs.memBar, refs.memLabel)
    queueProgressLabelMeasure(refs.diskBar, refs.diskLabel)
    queueProgressLabelMeasure(refs.trafficBar, refs.trafficLabel)
  }
}

function measureCpuOverflow(refs: CardRefs) {
  if (refs.root.hidden || refs.loading.hidden === false) {
    refs.cpuTrack.classList.remove('is-overflowing')
    return
  }

  const overflowing = refs.cpuA.scrollWidth > refs.cpuTrack.clientWidth
  refs.cpuTrack.classList.toggle('is-overflowing', overflowing)
}

function showTooltip(id: ServerId, event: PointerEvent) {
  const server = state.serverById.get(id)
  if (!server || !server.Host || !tooltip) return
  tooltip.hidden = false
  refreshTooltipContent(server)
  positionTooltip(event)
}

function refreshTooltipContent(server = state.serverById.get(state.tooltipServerId)) {
  if (!tooltip || !state.tooltipServerId || !server || !server.Host) return
  tooltip.innerHTML = tooltipContent(server)
}

function hideTooltip() {
  state.tooltipServerId = ''
  if (!tooltip) return
  tooltip.hidden = true
}

function positionTooltip(event: PointerEvent) {
  if (!tooltip) return

  const gap = 15
  const width = tooltip.offsetWidth || 250
  const height = tooltip.offsetHeight || 180
  const viewportWidth = window.innerWidth
  const viewportHeight = window.innerHeight
  const roomRight = viewportWidth - event.clientX
  const roomLeft = event.clientX
  const roomBottom = viewportHeight - event.clientY
  const roomTop = event.clientY

  let placement: 'right' | 'left' | 'bottom' | 'top' = 'right'
  let left = event.clientX + gap
  let top = event.clientY - height / 2

  if (roomRight < width + gap + 8 && roomLeft >= width + gap + 8) {
    placement = 'left'
    left = event.clientX - width - gap
  } else if (roomRight < width + gap + 8 && roomBottom >= height + gap + 8) {
    placement = 'bottom'
    left = event.clientX - width / 2
    top = event.clientY + gap
  } else if (roomRight < width + gap + 8 && roomTop >= height + gap + 8) {
    placement = 'top'
    left = event.clientX - width / 2
    top = event.clientY - height - gap
  }

  left = clamp(left, 8, viewportWidth - width - 8)
  top = clamp(top, 8, viewportHeight - height - 8)

  tooltip.dataset.placement = placement
  tooltip.style.left = `${left}px`
  tooltip.style.top = `${top}px`

  if (placement === 'left' || placement === 'right') {
    const arrowTop = clamp(event.clientY - top, 12, height - 12)
    tooltip.style.setProperty('--tooltip-arrow-top', `${arrowTop}px`)
    tooltip.style.removeProperty('--tooltip-arrow-left')
  } else {
    const arrowLeft = clamp(event.clientX - left, 12, width - 12)
    tooltip.style.setProperty('--tooltip-arrow-left', `${arrowLeft}px`)
    tooltip.style.removeProperty('--tooltip-arrow-top')
  }
}

function tooltipContent(server: ServerInfo) {
  const host = server.Host || {}
  const current = server.State || {}
  const lines = [
    `${t('Platform')}：${formatPlatformLine(host)}`,
    `${t('MemUsed')}：${formatByteSize(current.MemUsed)} / ${formatByteSize(host.MemTotal)}`,
  ]

  if (host.SwapTotal) {
    lines.push(`${t('SwapUsed')}：${formatByteSize(current.SwapUsed)} / ${formatByteSize(host.SwapTotal)}`)
  }

  lines.push(`${t('DiskUsed')}：${formatByteSize(current.DiskUsed)} / ${formatByteSize(host.DiskTotal)}`)
  lines.push(`${t('TrafficTotal')}：${trafficTooltip(server)}`)

  const gpu = Array.isArray(host.GPU) ? host.GPU.filter((item) => item && item !== 'unknown') : []
  if (gpu.length > 0) lines.push(`${t('GPU')}：${gpu.join(', ')}`)

  const temperature = firstTemperature(current.Temperatures)
  if (temperature > 0) lines.push(`${t('Temperature')}：${temperature}°C`)

  lines.push(`${t('Load')}：${toFixed2(current.Load1)} | ${toFixed2(current.Load5)} | ${toFixed2(current.Load15)}`)
  lines.push(`${t('ConnCount')}：TCP ${toNumber(current.TcpConnCount)} ${t('Count')} | UDP ${toNumber(current.UdpConnCount)} ${t('Count')}`)
  lines.push(`${t('BootTime')}：${host.BootTime ? timeStamp(host.BootTime * 1000) : ''}`)
  lines.push(`${t('LastActive')}：${server.LastActive ? timeStamp(server.LastActive) : ''}`)
  lines.push(`${t('Version')}：${host.Version || ''}`)

  return lines.map((line) => `<div>${escapeHtml(line.replace(/\s+/g, ' ').trim())}</div>`).join('')
}

function formatPlatformLine(host: HostInfo) {
  let line = specialOS(host.Platform)
  if (!isWindowsPlatform(host.Platform)) {
    const version = formatPlatformVersion(host.PlatformVersion)
    if (version) line += ` ${version}`
  }
  const virtualization = specialVir(host.Virtualization)
  if (virtualization) line += ` ${virtualization}`
  line += ` [${host.Arch || ''}]`
  return line.trim()
}

function trafficFor(server: ServerInfo): TrafficView {
  return state.trafficById.get(serverId(server)) || {
    max: '0B',
    used: '0B',
    percent: 0,
    serverName: server.Name || '',
    cycleName: '',
  }
}

function trafficLabel(traffic: TrafficView) {
  return formatProgressLabel(traffic.percent)
}

function trafficTooltip(server: ServerInfo) {
  const traffic = trafficFor(server)
  return `${traffic.used} / ${traffic.max}`
}

function isServerLive(server: ServerInfo, now?: number) {
  if (typeof server.live === 'boolean' && now === undefined) return server.live
  if (typeof server.IsOnline === 'boolean') return server.IsOnline
  if (typeof server.is_online === 'boolean') return server.is_online

  if (now && server.LastActive) {
    const lastActive = new Date(server.LastActive).getTime()
    const diff = now - lastActive
    if (!Number.isNaN(lastActive)) return diff < 130000 && diff > -10000
  }

  return Boolean(server.Host)
}

function serverId(server: ServerInfo) {
  return String(server.ID)
}

function ref(root: HTMLElement, name: string) {
  const element = root.querySelector<HTMLElement>(`[data-ref="${name}"]`)
  if (!element) throw new Error(`Missing card ref ${name}`)
  return element
}

function requiredElement<T extends HTMLElement = HTMLElement>(id: string): T {
  const el = app?.querySelector<T>(`#${id}`)
  if (!el) {
    throw new Error(`Missing #${id}`)
  }
  return el
}

function setText(element: HTMLElement, value: string, key: string, cache: Map<string, string>) {
  if (cache.get(key) === value) return
  cache.set(key, value)
  element.textContent = value
}

function setHtml(element: HTMLElement, value: string, key: string, cache: Map<string, string>) {
  if (cache.get(key) === value) return
  cache.set(key, value)
  element.innerHTML = value
}

function setVisible(element: HTMLElement, visible: boolean, key: string, cache: Map<string, string>) {
  const value = visible ? '1' : '0'
  if (cache.get(key) === value) return
  cache.set(key, value)
  element.hidden = !visible
}

function setFlag(element: HTMLElement, countryCode: string, cache: Map<string, string>) {
  const value = countryCode.trim().toLowerCase()
  if (cache.get('country') === value) return
  cache.set('country', value)
  element.hidden = !value
  element.className = value ? `country-flag fi fi-${cssSafeId(value)}` : 'country-flag'
  element.title = value.toUpperCase()
  element.textContent = ''
}

function percent(live: boolean, used?: number, total?: number) {
  if (!live) return 0
  const usedValue = toNumber(used)
  const totalValue = toNumber(total)
  if (usedValue <= 0 || totalValue <= 0) return 0
  return clamp((usedValue / totalValue) * 100, 0, 100)
}

function formatProgressWidth(value: number) {
  return trimNumber(clamp(value, 0, 100), 4)
}

function formatProgressLabel(value: number) {
  const safeValue = clamp(finiteNumber(value, 0), 0, 100)
  if (safeValue <= 0) return '0%'
  if (safeValue < 0.1) return '<0.1%'
  if (safeValue < 10) return `${trimNumber(safeValue, 1)}%`
  return `${Math.round(safeValue)}%`
}

function progressTone(value: number) {
  if (value < 60) return 'fine'
  if (value < 90) return 'warning'
  return 'error'
}

function badgeInlineStyle(stat: GroupStat) {
  if (stat.total === 0) return ''
  if (stat.offline === stat.total) return 'background-color:#f44336'
  if (stat.offline === 0) return 'background-color:#4caf50'
  return 'background-color:#ff8840'
}

function formatByteSize(bytes?: number) {
  const value = toNumber(bytes)
  if (!value) return '0B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB', 'ZB', 'YB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  const number = value / Math.pow(1024, index)
  return `${parseFloat(number.toFixed(2))}${units[index]}`
}

function getK2Gb(bytes?: number) {
  const value = toNumber(bytes)
  if (!value) return '0MB'
  const mb = Math.floor(value / 1024 / 1024)
  if (mb < 1000) return `${mb}MB`
  return `${Math.ceil(mb / 1024)}GB`
}

function secondToDate(seconds?: number) {
  const value = toNumber(seconds)
  if (!value) return '0:00:00'
  const days = Math.floor(value / 3600 / 24)
  if (days > 0) return `${days} ${t('Day')}`
  const hours = Math.floor((value / 3600) % 24)
  const minutes = Math.floor((value / 60) % 60)
  const secs = Math.floor(value % 60)
  return `${hours}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
}

function timeStamp(input: string | number) {
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return '-'
  return formatDate(date)
}

function normalizeCpu(cpu?: string[]) {
  if (!Array.isArray(cpu) || cpu.length === 0) return 'Unknown CPU'
  return cpu.filter(Boolean).join(' ')
}

function specialOS(platform?: string) {
  if (!platform) return ''
  const value = platform.toString().toLowerCase()
  if (value.includes('windows')) {
    return platform.replace('Microsoft ', '').replace('Datacenter', '').replace('Service Pack 1', '').trim()
  }
  const osMapping: Record<string, string> = {
    ubuntu: 'Ubuntu',
    debian: 'Debian',
    centos: 'CentOS',
    darwin: 'MacOS',
    redhat: 'RedHat',
    archlinux: 'Archlinux',
    coreos: 'Coreos',
    deepin: 'Deepin',
    fedora: 'Fedora',
    alpine: 'Alpine',
    tux: 'Tux',
    linuxmint: 'LinuxMint',
    oracle: 'Oracle',
    slackware: 'SlackWare',
    raspbian: 'Raspbian',
    gentoo: 'GenToo',
    arch: 'Arch',
    amazon: 'Amazon',
    xenserver: 'XenServer',
    scientific: 'ScientificSL',
    rhel: 'Rhel',
    rawhide: 'RawHide',
    cloudlinux: 'CloudLinux',
    ibm_powerkvm: 'IBM',
    almalinux: 'Almalinux',
    suse: 'Suse',
    opensuse: 'OpenSuse',
    'opensuse-leap': 'OpenSuse',
    'opensuse-tumbleweed': 'OpenSuse',
    'opensuse-tumbleweed-kubic': 'OpenSuse',
    sles: 'Sles',
    sled: 'Sled',
    caasp: 'Caasp',
    exherbo: 'ExherBo',
    solus: 'Solus',
  }
  return osMapping[value] || platform
}

function formatPlatformVersion(version?: string) {
  if (!version) return ''
  if (/^[\d.]+$/.test(version)) return version
  const cleanVersion = version.split('/')[0]
  return cleanVersion
    .split(' ')
    .map((word) => word ? word.charAt(0).toUpperCase() + word.slice(1).toLowerCase() : word)
    .join(' ')
}

function specialVir(virtualization?: string) {
  if (!virtualization) return ''
  const value = virtualization.toString().toLowerCase()
  const mapping: Record<string, string> = {
    kvm: 'KVM',
    openvz: 'OpenVZ',
    lxc: 'LXC',
    xen: 'Xen',
    vbox: 'VirtualBox',
    virtualbox: 'VirtualBox',
    rkt: 'RKT',
    docker: 'Docker',
    vmware: 'VMware',
    'vmware-esxi': 'VMware ESXi',
    'linux-vserver': 'VServer',
    hyperv: 'Hyper-V',
    'hyper-v': 'Hyper-V',
    microsoft: 'Hyper-V',
    qemu: 'QEMU',
    parallels: 'Parallels',
    bhyve: 'bhyve',
    jail: 'FreeBSD Jail',
    zone: 'Solaris Zone',
    wsl: 'WSL',
    podman: 'Podman',
    containerd: 'containerd',
    'systemd-nspawn': 'systemd-nspawn',
  }
  return mapping[value] || value.charAt(0).toUpperCase() + value.slice(1)
}

function isWindowsPlatform(platform?: string) {
  return Boolean(platform && platform.toLowerCase().includes('windows'))
}

function osIconMarkup(platform: string, uptime: number) {
  const logo = getFontLogoClass(platform)
  if (isWindowsPlatform(platform)) return icon('windows', 'os-windows-svg')
  if (logo) return `<i class="fl-${logo} os-font-logo" aria-hidden="true"></i>`
  if (uptime > 0) return `<i class="fl-tux os-font-logo" aria-hidden="true"></i>`
  return ''
}

function getFontLogoClass(platform: string) {
  const value = platform.toLowerCase()
  if (value.includes('centos')) return 'centos'
  if (value.includes('ubuntu')) return 'ubuntu'
  if (value.includes('debian')) return 'debian'
  if (value.includes('alpine')) return 'alpine'
  if (value.includes('darwin')) return 'apple'
  if (value.includes('freebsd')) return 'freebsd'
  if (value.includes('redhat')) return 'redhat'
  if (value.includes('opensuse')) return 'opensuse'
  if (value.includes('fedora')) return 'fedora'
  if (value.includes('arch')) return 'archlinux'
  if (value.includes('coreos')) return 'coreos'
  if (value.includes('deepin')) return 'deepin'
  return ''
}

function firstTemperature(items?: Array<{ Temperature?: number }>) {
  if (!Array.isArray(items)) return 0
  const item = items.find((entry) => toNumber(entry.Temperature) > 0)
  return item ? toNumber(item.Temperature) : 0
}

function toFixed2(value?: number) {
  return finiteNumber(value, 0).toFixed(2)
}

function trimNumber(value: number, digits: number) {
  return Number(value.toFixed(digits)).toString()
}

function t(key: LabelKey) {
  return LABELS['zh-CN'][key]
}

function formatDate(date: Date) {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}年${pad(date.getMonth() + 1)}月${pad(date.getDate())}日 ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

function finiteNumber(value: unknown, fallback: number) {
  const number = Number(value)
  return Number.isFinite(number) ? number : fallback
}

function toNumber(value: unknown) {
  return finiteNumber(value, 0)
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value))
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

function cssSafeId(value: string) {
  return value.replace(/[^a-zA-Z0-9_-]/g, '-')
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
