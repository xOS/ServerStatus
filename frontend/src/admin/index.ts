import { AUTH_API_BASE, adminApiPath, apiPath, authApiPath } from '../api'
import { authHeaders, clearStoredAuthToken, getStoredAuthToken, setStoredAuthToken } from '../auth'
import { footerOptions, icon } from '../layout'

type AdminKey = 'dashboard' | 'server' | 'monitor' | 'cron' | 'rule' | 'notification' | 'nat' | 'ddns' | 'api' | 'setting'
type Row = Record<string, unknown>
type Method = 'GET' | 'POST' | 'DELETE'
type FieldType = 'hidden' | 'text' | 'number' | 'textarea' | 'checkbox' | 'select' | 'combo' | 'relation'

interface ApiFetchOptions {
  method?: Method
  body?: BodyInit
  json?: unknown
}

interface NavItem {
  key: AdminKey
  href: string
  label: string
  icon: string
}

interface ColumnConfig {
  label: string
  className?: string
  render: (row: Row) => string
}

interface FieldConfig {
  name: string
  label: string
  type: FieldType
  options?: Array<{ value: string; label: string }>
  relation?: RelationConfig
  rows?: number
  placeholder?: string
  defaultValue?: string | number | boolean
  suggestions?: 'server-tags' | 'notification-tags'
}

interface RelationConfig {
  source: AdminKey
  multiple?: boolean
  valuePath?: string
  filter?: (row: Row) => boolean
  label: (row: Row) => string
}

interface EntityConfig {
  key: AdminKey
  title: string
  endpoint: string
  deleteModel: string
  description: string
  addLabel: string
  columns: ColumnConfig[]
  fields: FieldConfig[]
  rowsFrom: (payload: unknown) => Row[]
}

interface ApiResponse {
  code?: number
  message?: string
  result?: unknown
}

interface ConfirmActionOptions {
  title: string
  message: string
  confirmLabel: string
  danger?: boolean
}

interface SelectOption {
  value: string
  label: string
}

const navItems: NavItem[] = [
  { key: 'dashboard', href: '/dashboard', label: '仪表盘', icon: 'dashboard' },
  { key: 'server', href: '/dashboard/server', label: '服务器', icon: 'server' },
  { key: 'monitor', href: '/dashboard/monitor', label: '监控', icon: 'monitor' },
  { key: 'cron', href: '/dashboard/cron', label: '计划任务', icon: 'calendar' },
  { key: 'rule', href: '/dashboard/rule', label: '报警规则', icon: 'alert' },
  { key: 'notification', href: '/dashboard/notification', label: '通知', icon: 'bell' },
  { key: 'nat', href: '/dashboard/nat', label: 'NAT', icon: 'nat' },
  { key: 'ddns', href: '/dashboard/ddns', label: 'DDNS', icon: 'ddns' },
  { key: 'api', href: '/dashboard/api', label: 'API', icon: 'key' },
  { key: 'setting', href: '/dashboard/setting', label: '设置', icon: 'settings' },
]

let app: HTMLDivElement
let contentArea: HTMLElement
let dialog: HTMLDialogElement
let toastTimer = 0
let adminAbortController: AbortController | undefined
const entityRows = new Map<AdminKey, Row[]>()
let settingsCache: Row | null = null
let adminBrandName = 'ServerStatus'
let apiKeyLoginAllowed = false

const yesNo = (value: unknown) => toBool(value) ? '<span class="admin-badge is-green">是</span>' : '<span class="admin-badge">否</span>'
const enabled = (value: unknown) => toBool(value) ? '<span class="admin-badge is-green">启用</span>' : '<span class="admin-badge">停用</span>'

const entityConfigs: Partial<Record<AdminKey, EntityConfig>> = {
  server: {
    key: 'server',
    title: '服务器管理',
    endpoint: adminApiPath('/server'),
    deleteModel: 'server',
    description: '管理 Agent 接入、展示排序、分组、DDNS 关联和可见性。',
    addLabel: '添加服务器',
    rowsFrom: arrayPayload,
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => `${escapeHtml(value(row, 'ID'))}<span class="admin-muted">(${escapeHtml(value(row, 'DisplayIndex'))})</span>` },
      { label: '名称', render: (row) => serverNameMarkup(row) },
      { label: '分组', render: (row) => badge(value(row, 'Tag') || '默认') },
      { label: 'IP', render: (row) => serverIpMarkup(row) },
      { label: '系统', render: (row) => serverSystemMarkup(row) },
      { label: '探针版本', render: (row) => escapeHtml(value(row, 'Host.Version') || '-') },
      { label: '状态', render: (row) => toBool(get(row, 'is_online')) ? '<span class="admin-badge is-green">在线</span>' : '<span class="admin-badge is-red">离线</span>' },
      { label: '密钥', render: (row) => secretMarkup(row) },
      { label: '安装脚本', render: (row) => installButtons(row) },
      { label: '备注', render: (row) => notesMarkup(row) },
      { label: '游客隐藏', render: (row) => yesNo(get(row, 'HideForGuest')) },
      { label: 'DDNS', render: (row) => ddnsProfilesMarkup(row) },
      { label: '操作', className: 'is-actions', render: (row) => actionButtons('server', row) },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      comboField('Tag', '分组', 'server-tags', 'default'),
      numberField('DisplayIndex', '排序', 0),
      textField('Secret', '密钥'),
      checkboxField('HideForGuest', '对游客隐藏'),
      checkboxField('EnableDDNS', '启用 DDNS'),
      relationField('DDNSProfilesRaw', 'DDNS 配置', 'ddns', true),
      textareaField('Note', '管理员备注', 3),
      textareaField('PublicNote', '公开备注', 3),
    ],
  },
  monitor: {
    key: 'monitor',
    title: '监控管理',
    endpoint: adminApiPath('/monitor'),
    deleteModel: 'monitor',
    description: '配置 ICMP/TCP 网络监控、通知和触发任务。',
    addLabel: '添加监控',
    rowsFrom: arrayPayload,
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => escapeHtml(value(row, 'ID')) },
      { label: '名称', render: (row) => `<strong>${escapeHtml(value(row, 'Name'))}</strong>` },
      { label: '类型', render: (row) => monitorType(get(row, 'Type')) },
      { label: '目标', render: (row) => copyTextCell(value(row, 'Target'), '复制目标') },
      { label: '范围', render: (row) => monitorCoverMarkup(row) },
      { label: '间隔', render: (row) => `${escapeHtml(value(row, 'Duration'))}s` },
      { label: '通知组', render: (row) => badge(value(row, 'NotificationTag') || 'default') },
      { label: '通知', render: (row) => enabled(get(row, 'Notify')) },
      { label: '服务页', render: (row) => enabled(get(row, 'EnableShowInService')) },
      { label: '触发任务', render: (row) => triggerTasksMarkup(row) },
      { label: '操作', className: 'is-actions', render: (row) => actionButtons('monitor', row) },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      textField('Target', '目标'),
      selectField('Type', '类型', [['1', 'ICMP Ping'], ['2', 'TCP Ping']]),
      numberField('Duration', '间隔秒数', 30),
      selectField('Cover', '覆盖范围', [['0', '覆盖全部，排除指定服务器'], ['1', '忽略全部，仅包含指定服务器']]),
      textField('NotificationTag', '通知组', 'default', '', 'notification-tags'),
      checkboxField('Notify', '启用通知'),
      checkboxField('LatencyNotify', '启用延迟通知'),
      numberField('MinLatency', '最小延迟阈值', 0),
      numberField('MaxLatency', '最大延迟阈值', 0),
      checkboxField('EnableShowInService', '显示在服务页'),
      checkboxField('EnableTriggerTask', '启用触发任务'),
      relationField('SkipServersRaw', '服务器范围列表', 'server', true),
      relationField('FailTriggerTasksRaw', '失败触发任务', 'cron', true, triggerTaskFilter),
      relationField('RecoverTriggerTasksRaw', '恢复触发任务', 'cron', true, triggerTaskFilter),
    ],
  },
  cron: {
    key: 'cron',
    title: '计划任务',
    endpoint: adminApiPath('/cron'),
    deleteModel: 'cron',
    description: '配置定时任务和被报警规则调用的触发任务。',
    addLabel: '添加任务',
    rowsFrom: arrayPayload,
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => escapeHtml(value(row, 'ID')) },
      { label: '名称', render: (row) => `<strong>${escapeHtml(value(row, 'Name'))}</strong>` },
      { label: '类型', render: (row) => Number(get(row, 'TaskType')) === 1 ? badge('触发任务') : badge('计划任务') },
      { label: '计划', render: (row) => escapeHtml(value(row, 'Scheduler') || '-') },
      { label: '命令', render: (row) => copyTextCell(truncate(value(row, 'Command'), 56), '复制命令', value(row, 'Command')) },
      { label: '服务器', render: (row) => cronServersMarkup(row) },
      { label: '通知组', render: (row) => badge(value(row, 'NotificationTag') || 'default') },
      { label: '最后执行', render: (row) => formatDate(get(row, 'LastExecutedAt')) },
      { label: '结果', render: (row) => enabled(get(row, 'LastResult')) },
      { label: '操作', className: 'is-actions', render: (row) => `${actionButtons('cron', row)}<button class="admin-icon-btn" data-manual-cron="${escapeAttribute(value(row, 'ID'))}" title="手动执行" aria-label="手动执行">${icon('play')}</button>` },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      selectField('TaskType', '类型', [['0', '计划任务'], ['1', '触发任务']]),
      textField('Scheduler', 'Cron 表达式'),
      textareaField('Command', '命令', 5),
      selectField('Cover', '执行范围', [['0', '仅指定服务器'], ['1', '排除指定服务器'], ['2', '由触发服务器执行']]),
      relationField('ServersRaw', '服务器列表', 'server', true),
      textField('NotificationTag', '通知组', 'default', '', 'notification-tags'),
      checkboxField('PushSuccessful', '推送成功通知'),
    ],
  },
  rule: {
    key: 'rule',
    title: '报警规则',
    endpoint: adminApiPath('/notification'),
    deleteModel: 'alert-rule',
    description: '管理资源、离线、流量等报警规则。',
    addLabel: '添加规则',
    rowsFrom: (payload) => arrayFromObject(payload, 'AlertRules'),
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => escapeHtml(value(row, 'ID')) },
      { label: '名称', render: (row) => `<strong>${escapeHtml(value(row, 'Name'))}</strong>` },
      { label: '通知组', render: (row) => badge(value(row, 'NotificationTag') || 'default') },
      { label: '模式', render: (row) => Number(get(row, 'TriggerMode')) === 1 ? badge('单次') : badge('持续') },
      { label: '规则', render: (row) => `<code>${escapeHtml(truncate(value(row, 'RulesRaw'), 76))}</code>` },
      { label: '触发任务', render: (row) => triggerTasksMarkup(row) },
      { label: '状态', render: (row) => enabled(get(row, 'Enable')) },
      { label: '操作', className: 'is-actions', render: (row) => actionButtons('rule', row) },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      checkboxField('Enable', '启用'),
      selectField('TriggerMode', '触发模式', [['0', '始终触发'], ['1', '单次触发']]),
      textField('NotificationTag', '通知组', 'default', '', 'notification-tags'),
      textareaField('RulesRaw', '规则 JSON', 8, '[]'),
      relationField('FailTriggerTasksRaw', '失败触发任务', 'cron', true, triggerTaskFilter),
      relationField('RecoverTriggerTasksRaw', '恢复触发任务', 'cron', true, triggerTaskFilter),
    ],
  },
  notification: {
    key: 'notification',
    title: '通知方式',
    endpoint: adminApiPath('/notification'),
    deleteModel: 'notification',
    description: '配置 Webhook 通知渠道和请求模板。',
    addLabel: '添加通知',
    rowsFrom: (payload) => arrayFromObject(payload, 'Notifications'),
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => escapeHtml(value(row, 'ID')) },
      { label: '名称', render: (row) => `<strong>${escapeHtml(value(row, 'Name'))}</strong>` },
      { label: '分组', render: (row) => badge(value(row, 'Tag') || 'default') },
      { label: '方法', render: (row) => Number(get(row, 'RequestMethod')) === 1 ? 'GET' : 'POST' },
      { label: '类型', render: (row) => Number(get(row, 'RequestType')) === 2 ? 'Form' : 'JSON' },
      { label: 'URL', render: (row) => copyTextCell(truncate(value(row, 'URL'), 72), '复制 URL', value(row, 'URL')) },
      { label: 'SSL', render: (row) => yesNo(get(row, 'VerifySSL')) },
      { label: '操作', className: 'is-actions', render: (row) => actionButtons('notification', row) },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      textField('Tag', '分组', 'default', '', 'notification-tags'),
      textField('URL', 'URL'),
      selectField('RequestMethod', '请求方法', [['1', 'GET'], ['2', 'POST']]),
      selectField('RequestType', '请求类型', [['1', 'JSON'], ['2', 'Form']]),
      checkboxField('VerifySSL', '验证 SSL'),
      checkboxField('SkipCheck', '保存时跳过测试', true),
      textareaField('RequestHeader', '请求头 JSON', 4, '{}'),
      textareaField('RequestBody', '请求体', 6),
    ],
  },
  nat: {
    key: 'nat',
    title: 'NAT 管理',
    endpoint: adminApiPath('/nat'),
    deleteModel: 'nat',
    description: '管理内网服务映射域名。',
    addLabel: '添加 NAT',
    rowsFrom: arrayPayload,
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => escapeHtml(value(row, 'ID')) },
      { label: '名称', render: (row) => `<strong>${escapeHtml(value(row, 'Name'))}</strong>` },
      { label: '服务器', render: (row) => serverRefMarkup(value(row, 'ServerID')) },
      { label: '内网服务', render: (row) => copyTextCell(value(row, 'Host'), '复制内网服务') },
      { label: '域名', render: (row) => copyTextCell(value(row, 'Domain'), '复制域名') },
      { label: '操作', className: 'is-actions', render: (row) => actionButtons('nat', row) },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      relationField('ServerID', '服务器', 'server', false),
      textField('Host', '内网服务'),
      textField('Domain', '绑定域名'),
    ],
  },
  ddns: {
    key: 'ddns',
    title: 'DDNS 配置',
    endpoint: adminApiPath('/ddns'),
    deleteModel: 'ddns',
    description: '管理 DDNS Provider、域名和解析凭据。',
    addLabel: '添加 DDNS',
    rowsFrom: (payload) => arrayFromObject(payload, 'DDNS'),
    columns: [
      { label: 'ID', className: 'is-narrow', render: (row) => escapeHtml(value(row, 'ID')) },
      { label: '名称', render: (row) => `<strong>${escapeHtml(value(row, 'Name'))}</strong>` },
      { label: 'Provider', render: (row) => ddnsProvider(get(row, 'Provider')) },
      { label: '域名', render: (row) => copyTextCell(value(row, 'DomainsRaw') || value(row, 'Domains'), '复制域名') },
      { label: 'Access ID', render: (row) => copyTextCell(value(row, 'AccessID'), '复制 Access ID') },
      { label: 'Secret', render: (row) => secretValueCell(value(row, 'AccessSecret'), '复制 Access Secret') },
      { label: 'IPv4', render: (row) => enabled(get(row, 'EnableIPv4')) },
      { label: 'IPv6', render: (row) => enabled(get(row, 'EnableIPv6')) },
      { label: '重试', render: (row) => escapeHtml(value(row, 'MaxRetries')) },
      { label: '操作', className: 'is-actions', render: (row) => actionButtons('ddns', row) },
    ],
    fields: [
      hiddenField('ID'),
      textField('Name', '名称'),
      selectField('Provider', 'Provider', [['0', 'dummy'], ['1', 'webhook'], ['2', 'cloudflare'], ['3', 'tencentcloud']]),
      checkboxField('EnableIPv4', '启用 IPv4', true),
      checkboxField('EnableIPv6', '启用 IPv6'),
      numberField('MaxRetries', '最大重试次数', 3),
      textField('DomainsRaw', '域名列表'),
      textField('AccessID', 'Access ID'),
      textField('AccessSecret', 'Access Secret'),
      textField('WebhookURL', 'Webhook URL'),
      selectField('WebhookMethod', 'Webhook 方法', [['1', 'GET'], ['2', 'POST']]),
      selectField('WebhookRequestType', 'Webhook 类型', [['1', 'JSON'], ['2', 'Form']]),
      textareaField('WebhookRequestBody', 'Webhook 请求体', 5),
      textareaField('WebhookHeaders', 'Webhook 请求头', 4),
    ],
  },
}

export function initAdmin(container: HTMLDivElement) {
  cleanupAdmin()
  adminAbortController = new AbortController()
  app = container

  app.innerHTML = `
    <div class="admin-shell">
      <header class="admin-header">
        <a class="admin-brand" href="/" data-route-home>
          <img src="/static/logo.svg?v20260708" alt="">
          <span id="admin-brand-title">${escapeHtml(adminBrandName)}</span>
          <small>管理后台</small>
        </a>
        <div class="admin-header-actions">
          <button class="admin-button is-ghost" type="button" data-open-auth data-top-action="auth" hidden>${icon('shield', 'admin-button-svg')}<span>授权</span></button>
          <a class="admin-button is-ghost" href="/" data-route-home data-top-action="home">${icon('home', 'admin-button-svg')}<span>前台</span></a>
          <button class="admin-button is-ghost" type="button" data-admin-logout data-top-action="logout">${icon('logout', 'admin-button-svg')}<span>退出</span></button>
        </div>
      </header>
      <aside class="admin-sidebar">
        <nav class="admin-nav">
          ${navItems.map((item) => `
            <a href="${item.href}" data-admin-nav="${item.key}">
              ${icon(item.icon, 'admin-nav-svg')}
              <span>${item.label}</span>
            </a>
          `).join('')}
        </nav>
      </aside>
      <main class="admin-main">
        <section class="admin-content" id="admin-content"></section>
      </main>
      <footer class="admin-footer">
        <b>&copy; <span id="admin-footer-year">2026</span> <a href="/" id="admin-footer-brand">${escapeHtml(adminBrandName)}</a></b>
        <span class="footer-separator">|</span>
        <a href="http://www.nange.cn" target="_blank" rel="noreferrer" id="admin-footer-author">春夏</a>
        <span class="custom-code" id="admin-footer-custom-code"></span>
      </footer>
      <dialog class="admin-dialog" id="admin-dialog"></dialog>
      <div class="admin-toast" id="admin-toast" hidden></div>
    </div>
  `

  contentArea = requiredElement('admin-content')
  dialog = requiredDialog('admin-dialog')
  bindAdminEvents()
  void loadAdminProfile()
  renderAdminRoute()
  return cleanupAdmin
}

export function initLogin(container: HTMLDivElement) {
  cleanupAdmin()
  const controller = new AbortController()
  adminAbortController = controller
  app = container

  const renderLogin = (allowOAuth = true) => {
    apiKeyLoginAllowed = false
    app.innerHTML = `
    <main class="login-shell">
      <section class="login-panel">
        <img src="/static/logo.svg?v20260708" alt="">
        <h1>登录</h1>
        <p>仅允许白名单账号授权登录。</p>
        <div class="login-actions">
          ${allowOAuth ? `<a class="admin-button is-primary" href="${escapeAttribute(oauthLoginUrl())}" data-native-link>${icon('login', 'admin-button-svg')}<span>账号登录</span></a>` : ''}
          <a class="admin-button is-ghost" href="/">${icon('home', 'admin-button-svg')}<span>返回前台</span></a>
        </div>
      </section>
    </main>
  `
  }

  renderLogin()
  const signal = controller.signal
  fetch(apiPath('/profile'), { credentials: 'include', headers: authHeaders(), signal })
    .then((response) => response.json())
    .then((profile) => {
      const row = objectFrom(profile?.data || profile)
      updateAdminBrand(row)
      renderLogin(loginOAuthAllowed(row))
    })
    .catch(() => null)

  return cleanupAdmin
}

function oauthLoginUrl() {
  const url = new URL(authApiPath('/oauth2/login'), window.location.href)
  url.searchParams.set('auth_base', new URL(AUTH_API_BASE, window.location.href).toString().replace(/\/+$/, ''))
  url.searchParams.set('return_to', new URL('/dashboard', window.location.href).toString())
  return url.toString()
}

function cleanupAdmin() {
  adminAbortController?.abort()
  adminAbortController = undefined
  if (toastTimer) {
    window.clearTimeout(toastTimer)
    toastTimer = 0
  }
}

function bindAdminEvents() {
  const signal = adminAbortController?.signal
  if (!signal) return

  app.addEventListener('click', (event) => {
    const target = event.target as HTMLElement
    const authButton = target.closest<HTMLElement>('[data-open-auth]')
    if (authButton) {
      openAuthDialog()
      return
    }

    const logoutButton = target.closest<HTMLElement>('[data-admin-logout]')
    if (logoutButton) {
      logout()
      return
    }

    const addButton = target.closest<HTMLElement>('[data-add-entity]')
    if (addButton) {
      const key = addButton.dataset.addEntity as AdminKey
      void openEntityEditor(key)
      return
    }

    const refreshButton = target.closest<HTMLElement>('[data-refresh-admin]')
    if (refreshButton) {
      void refreshAdminTarget(refreshButton.dataset.refreshAdmin || currentAdminKey())
      return
    }

    const editButton = target.closest<HTMLElement>('[data-edit-entity]')
    if (editButton) {
      const key = editButton.dataset.editEntity as AdminKey
      void openEntityEditor(key, editButton.dataset.id || '')
      return
    }

    const deleteButton = target.closest<HTMLElement>('[data-delete-entity]')
    if (deleteButton) {
      const key = deleteButton.dataset.deleteEntity as AdminKey
      deleteEntity(key, deleteButton.dataset.id || '')
      return
    }

    const manualButton = target.closest<HTMLElement>('[data-manual-cron]')
    if (manualButton) {
      manualCron(manualButton.dataset.manualCron || '')
      return
    }

    const installButton = target.closest<HTMLElement>('[data-copy-install]')
    if (installButton) {
      copyInstallScript(installButton)
      return
    }

    const selectAllServersButton = target.closest<HTMLElement>('[data-select-all-servers]')
    if (selectAllServersButton) {
      toggleServerSelection()
      return
    }

    const batchGroupButton = target.closest<HTMLElement>('[data-server-batch-group]')
    if (batchGroupButton) {
      void openBatchGroupDialog()
      return
    }

    const batchDeleteButton = target.closest<HTMLElement>('[data-server-batch-delete]')
    if (batchDeleteButton) {
      void batchDeleteServers()
      return
    }

    const forceUpdateButton = target.closest<HTMLElement>('[data-server-force-update]')
    if (forceUpdateButton) {
      void forceUpdateServers()
      return
    }

    const copyButton = target.closest<HTMLElement>('[data-copy-value]')
    if (copyButton) {
      copyText(copyButton.dataset.copyValue || '')
      return
    }

    const tokenButton = target.closest<HTMLElement>('[data-token-delete]')
    if (tokenButton) {
      deleteApiToken(tokenButton.dataset.tokenDelete || '')
      return
    }

    const openTokenButton = target.closest<HTMLElement>('[data-open-token-dialog]')
    if (openTokenButton) {
      openTokenDialog()
      return
    }

    const selectOption = target.closest<HTMLElement>('[data-admin-select-option]')
    if (selectOption) {
      setAdminSelectValue(selectOption)
      return
    }

    const comboOption = target.closest<HTMLElement>('[data-admin-combo-option]')
    if (comboOption) {
      setAdminComboValue(comboOption)
      return
    }

    const selectToggle = target.closest<HTMLElement>('[data-admin-select-toggle], [data-admin-combo-toggle]')
    if (selectToggle) {
      toggleAdminDropdown(selectToggle)
      return
    }

    closeAdminDropdowns(target)
  }, { signal })

  app.addEventListener('submit', (event) => {
    const form = event.target as HTMLFormElement
    if (form.matches('[data-entity-form]')) {
      event.preventDefault()
      submitEntityForm(form)
    } else if (form.matches('[data-auth-form]')) {
      event.preventDefault()
      const token = String(new FormData(form).get('token') || '').trim()
      if (token) setStoredAuthToken(token)
      dialog.close()
      if (window.location.pathname === '/dashboard') {
        renderAdminRoute()
      } else {
        window.dispatchEvent(new CustomEvent('app-navigate', { detail: '/dashboard' }))
      }
    } else if (form.matches('[data-token-form]')) {
      event.preventDefault()
      issueApiToken(form)
    } else if (form.matches('[data-setting-form]')) {
      event.preventDefault()
      submitSettings(form)
    } else if (form.matches('[data-server-batch-group-form]')) {
      event.preventDefault()
      batchUpdateServerGroup(form)
    }
  }, { signal })

  app.addEventListener('change', (event) => {
    const selectAllServers = (event.target as HTMLElement).closest<HTMLInputElement>('[data-server-select-all]')
    if (selectAllServers) {
      setAllServerSelection(selectAllServers.checked)
      return
    }

    const serverSelect = (event.target as HTMLElement).closest<HTMLInputElement>('[data-server-select]')
    if (serverSelect) {
      updateServerBatchState()
      return
    }

    const option = (event.target as HTMLElement).closest<HTMLInputElement>('[data-relation-option]')
    if (!option) return
    updateRelationValue(option)
  }, { signal })

  app.addEventListener('input', (event) => {
    const comboInput = (event.target as HTMLElement).closest<HTMLInputElement>('[data-admin-combo-input]')
    if (!comboInput) return
    updateAdminComboInput(comboInput)
  }, { signal })
}

async function renderAdminRoute() {
  const key = currentAdminKey()
  setActiveNav(key)

  if (key === 'dashboard') {
    await renderDashboard()
    return
  }
  if (key === 'api') {
    await renderApiTokens()
    return
  }
  if (key === 'setting') {
    await renderSettings()
    return
  }

  const config = entityConfigs[key]
  if (!config) {
    contentArea.innerHTML = emptyPanel('页面不存在')
    return
  }
  await renderEntityPage(config)
}

async function refreshAdminTarget(target: string) {
  if (target === 'dashboard') {
    await renderDashboard()
    return
  }
  if (target === 'api') {
    await renderApiTokens()
    return
  }
  if (target === 'setting') {
    await renderSettings()
    return
  }
  const config = entityConfigs[target as AdminKey]
  if (config) {
    await renderEntityPage(config)
    return
  }
  await renderAdminRoute()
}

async function renderDashboard() {
  contentArea.innerHTML = loadingPanel('加载后台概览...')
  try {
    const [overview, profile] = await Promise.all([
      apiFetch<unknown>(adminApiPath('/overview')),
      apiFetch<unknown>(apiPath('/profile')),
    ])

    const total = countValue(overview, 'servers.total')
    const online = countValue(overview, 'servers.online')
    const offline = countValue(overview, 'servers.offline', Math.max(total - online, 0))
    const monitorTotal = countValue(overview, 'monitors.total')
    const cronTotal = countValue(overview, 'crons.total')
    const notificationTotal = countValue(overview, 'notifications.total')
    const alertRuleTotal = countValue(overview, 'alert_rules.total')
    const ddnsTotal = countValue(overview, 'ddns.total')
    const natTotal = countValue(overview, 'nat.total')
    const tokenTotal = countValue(overview, 'tokens.total')
    const onlineRate = total > 0 ? Math.round((online / total) * 100) : 0
    const profileRow = objectFrom(profile)
    updateAdminBrand(profileRow)
    contentArea.innerHTML = `
      <div class="admin-overview">
        ${statCard('服务器', String(total), `${online} 在线`, 'server', '/dashboard/server')}
        ${statCard('监控', String(monitorTotal), 'ICMP / TCP', 'monitor', '/dashboard/monitor')}
        ${statCard('计划任务', String(cronTotal), '定时与触发', 'calendar', '/dashboard/cron')}
        ${statCard('通知', String(notificationTotal), `${alertRuleTotal} 条规则`, 'bell', '/dashboard/notification')}
        ${statCard('DDNS', String(ddnsTotal), '解析配置', 'ddns', '/dashboard/ddns')}
        ${statCard('NAT', String(natTotal), '内网映射', 'nat', '/dashboard/nat')}
        ${statCard('API Token', String(tokenTotal), '请求头授权', 'key', '/dashboard/api')}
      </div>
      <section class="admin-panel">
        <div class="admin-panel-head">
          <div>
            <h2>运行状态</h2>
            <p>后台数据通过 REST API 实时读取，点击下方条目可进入对应管理页面。</p>
          </div>
          <button class="admin-button is-ghost" type="button" data-refresh-admin="dashboard" data-toolbar-action="refresh">${icon('refresh', 'admin-button-svg')}<span>刷新</span></button>
        </div>
        <div class="admin-status-grid">
          ${statusMeter('服务器在线率', `${onlineRate}%`, onlineRate, `${online} 在线 / ${offline} 离线`, '/dashboard/server')}
          ${statusItem('离线服务器', String(offline), `${total} 台总数`, 'server', '/dashboard/server')}
          ${statusItem('监控任务', String(monitorTotal), '网络探测配置', 'monitor', '/dashboard/monitor')}
          ${statusItem('通知规则', String(alertRuleTotal), '事件触发规则', 'alert', '/dashboard/rule')}
          ${statusItem('授权 Token', String(tokenTotal), 'Authorization 请求头', 'key', '/dashboard/api')}
          ${statusItem('刷新时间', currentTimeLabel(), '本页数据读取时间', 'activity', '/dashboard')}
        </div>
      </section>
    `
  } catch (error) {
    handlePageError(error)
  }
}

async function loadAdminProfile() {
  try {
    const profile = await apiFetch<unknown>(apiPath('/profile'))
    updateAdminBrand(objectFrom(profile))
  } catch {
    updateAdminBrand({})
  }
}

function updateAdminBrand(profile: Row) {
  const brand = value(profile, 'Conf.Site.Brand') || adminBrandName
  apiKeyLoginAllowed = false
  adminBrandName = brand
  document.title = brand
  const footer = footerOptions(profile)
  const title = document.getElementById('admin-brand-title')
  const footerBrand = document.getElementById('admin-footer-brand')
  const footerYear = document.getElementById('admin-footer-year')
  const footerAuthor = document.getElementById('admin-footer-author') as HTMLAnchorElement | null
  const footerCustomCode = document.getElementById('admin-footer-custom-code')
  if (title) title.textContent = brand
  if (footerBrand) footerBrand.textContent = brand
  if (footerYear) footerYear.textContent = footer.year
  if (footerAuthor) {
    footerAuthor.textContent = footer.name
    footerAuthor.href = footer.url
  }
  if (footerCustomCode) footerCustomCode.innerHTML = value(profile, 'CustomCodeDashboard') || value(profile, 'Conf.Site.CustomCodeDashboard') || ''
  document.querySelectorAll<HTMLElement>('[data-open-auth]').forEach((button) => {
    button.hidden = true
  })
}

function loginOAuthAllowed(profile: Row) {
  const input = get(profile, 'Conf.Login.EnableOAuth')
  return input === undefined || toBool(input)
}

async function renderEntityPage(config: EntityConfig) {
  contentArea.innerHTML = loadingPanel(`加载${config.title}...`)
  try {
    const payload = await apiFetch<unknown>(config.endpoint)
    const rows = config.rowsFrom(payload)
    entityRows.set(config.key, rows)
    await loadDisplayLookups(config.key)
    contentArea.innerHTML = `
      <section class="admin-panel">
        <div class="admin-panel-head">
          <div>
            <h2>${escapeHtml(config.title)}</h2>
            <p>${escapeHtml(config.description)}</p>
          </div>
          <div class="admin-toolbar">
            ${config.key === 'server' ? serverBatchToolbar(rows.length) : ''}
            <button class="admin-button is-ghost" type="button" data-refresh-admin="${config.key}" data-toolbar-action="refresh">${icon('refresh', 'admin-button-svg')}<span>刷新</span></button>
            <button class="admin-button is-ghost" type="button" data-add-entity="${config.key}" data-toolbar-action="add">${icon('edit', 'admin-button-svg')}<span>${escapeHtml(config.addLabel)}</span></button>
          </div>
        </div>
        ${tableMarkup(config, rows)}
      </section>
    `
    if (config.key === 'server') updateServerBatchState()
  } catch (error) {
    handlePageError(error)
  }
}

async function loadAdminLookups() {
  const keys: AdminKey[] = ['server', 'cron', 'notification', 'ddns']
  await Promise.allSettled(keys.map(async (key) => {
    if (entityRows.has(key)) return
    const config = entityConfigs[key]
    if (!config) return
    const payload = await apiFetch<unknown>(config.endpoint)
    entityRows.set(key, config.rowsFrom(payload))
  }))

  if (!settingsCache) {
    const result = await Promise.allSettled([apiFetch<Row>(adminApiPath('/setting'))])
    const payload = result[0].status === 'fulfilled' ? result[0].value : null
    settingsCache = payload ? objectFrom(get(payload, 'Settings')) : null
  }
}

async function loadDisplayLookups(key: AdminKey) {
  if (!['server', 'monitor', 'cron', 'rule', 'nat'].includes(key)) return
  const keys: AdminKey[] = ['server', 'cron', 'ddns']
  await Promise.allSettled(keys.map(async (lookupKey) => {
    if (entityRows.has(lookupKey)) return
    const config = entityConfigs[lookupKey]
    if (!config) return
    const payload = await apiFetch<unknown>(config.endpoint)
    entityRows.set(lookupKey, config.rowsFrom(payload))
  }))
}

async function renderApiTokens() {
  contentArea.innerHTML = loadingPanel('加载 API Token...')
  try {
    const payload = await apiFetch<unknown>(adminApiPath('/token'))
    const rows = arrayFromObject(payload, 'result')
    contentArea.innerHTML = `
      <section class="admin-panel">
        <div class="admin-panel-head">
          <div>
            <h2>API 管理</h2>
            <p>创建和管理可通过 Authorization 请求头访问接口的 Token。</p>
          </div>
          <div class="admin-toolbar">
            <button class="admin-button is-ghost" type="button" data-open-token-dialog data-toolbar-action="token">${icon('key', 'admin-button-svg')}<span>生成</span></button>
            <button class="admin-button is-ghost" type="button" data-refresh-admin="api" data-toolbar-action="refresh">${icon('refresh', 'admin-button-svg')}<span>刷新</span></button>
          </div>
        </div>
        <div class="admin-table-wrap">
          <table class="admin-table">
            <thead><tr><th>备注</th><th>Token</th><th class="is-actions">操作</th></tr></thead>
            <tbody>
              ${rows.map((row) => `
                <tr>
                  <td>${escapeHtml(apiTokenNote(row) || '-')}</td>
                  <td>${copyTextCell(apiTokenValue(row), '复制 Token')}</td>
                  <td class="is-actions">
                    <button class="admin-icon-btn" data-copy-value="${escapeAttribute(apiTokenValue(row))}" title="复制" aria-label="复制 Token">${icon('copy')}</button>
                    <button class="admin-icon-btn is-danger" data-token-delete="${escapeAttribute(apiTokenValue(row))}" title="删除" aria-label="删除 Token">${icon('trash')}</button>
                  </td>
                </tr>
              `).join('') || `<tr><td colspan="3" class="admin-empty-cell">暂无 Token</td></tr>`}
            </tbody>
          </table>
        </div>
      </section>
    `
  } catch (error) {
    handlePageError(error)
  }
}

async function renderSettings() {
  contentArea.innerHTML = loadingPanel('加载系统设置...')
  try {
    const payload = await apiFetch<Row>(adminApiPath('/setting'))
    const settings = objectFrom(get(payload, 'Settings'))
    settingsCache = settings

    contentArea.innerHTML = `
      <section class="admin-panel">
        <div class="admin-panel-head">
          <div>
            <h2>系统设置</h2>
            <p>修改站点、通知和 gRPC 基础配置。</p>
          </div>
        </div>
        <form class="admin-form-grid" data-setting-form>
          ${inputMarkup('Title', '站点名称', settings.Title)}
          ${inputMarkup('Admin', '管理员账号', settings.Admin)}
          ${inputMarkup('FooterYear', '页脚年份', settings.FooterYear || '2026')}
          ${inputMarkup('FooterName', '页脚作者名称', settings.FooterName || '春夏')}
          ${inputMarkup('FooterURL', '页脚作者链接', settings.FooterURL || 'http://www.nange.cn')}
          ${checkboxMarkup('EnableOAuthLogin', '启用账号授权登录', settings.EnableOAuthLogin)}
          ${checkboxMarkup('EnableAPIKeyLogin', '允许 API 请求头授权', settings.EnableAPIKeyLogin)}
          ${inputMarkup('GRPCHost', 'gRPC Host', settings.GRPCHost)}
          ${inputMarkup('GRPCPort', 'gRPC 端口', settings.GRPCPort, 'number')}
          ${selectMarkup('Cover', 'IP 变更通知范围', [['0', '忽略指定服务器'], ['1', '仅指定服务器']], settings.Cover)}
          ${inputMarkup('IPChangeNotificationTag', 'IP 变更通知组', settings.IPChangeNotificationTag)}
          ${textareaMarkup('IgnoredIPNotification', '忽略服务器 ID', settings.IgnoredIPNotification, 2)}
          ${textareaMarkup('CustomNameservers', '自定义 DNS', settings.CustomNameservers, 2)}
          ${textareaMarkup('AllowedOrigins', 'API 跨域白名单', settings.AllowedOrigins, 2)}
          ${inputMarkup('ViewPassword', '查看密码', settings.ViewPassword)}
          ${checkboxMarkup('EnableIPChangeNotification', '启用 IP 变更通知', settings.EnableIPChangeNotification)}
          ${checkboxMarkup('EnablePlainIPInNotification', '通知中显示明文 IP', settings.EnablePlainIPInNotification)}
          ${textareaMarkup('CustomCode', '前台自定义代码', settings.CustomCode, 5)}
          ${textareaMarkup('CustomCodeDashboard', '后台自定义代码', settings.CustomCodeDashboard, 5)}
          <div class="admin-form-actions">
            <button class="admin-button is-ghost" type="submit" data-form-action="save">${icon('settings', 'admin-button-svg')}<span>保存设置</span></button>
          </div>
        </form>
      </section>
    `
  } catch (error) {
    handlePageError(error)
  }
}

function tableMarkup(config: EntityConfig, rows: Row[]) {
  const selectable = config.key === 'server'
  return `
    <div class="admin-table-wrap">
      <table class="admin-table">
        <thead>
          <tr>
            ${selectable ? `
              <th class="is-select">
                <label class="admin-row-check" title="全选">
                  <input type="checkbox" data-server-select-all>
                  <span></span>
                </label>
              </th>
            ` : ''}
            ${config.columns.map((column) => `<th class="${column.className || ''}">${escapeHtml(column.label)}</th>`).join('')}
          </tr>
        </thead>
        <tbody>
          ${rows.map((row) => `
            <tr class="admin-data-row" data-server-id="${escapeAttribute(value(row, 'ID'))}">
              ${selectable ? `
                <td class="is-select">
                  <label class="admin-row-check" title="选择服务器">
                    <input type="checkbox" data-server-select value="${escapeAttribute(value(row, 'ID'))}">
                    <span></span>
                  </label>
                </td>
              ` : ''}
              ${config.columns.map((column) => `<td class="${column.className || ''}">${column.render(row)}</td>`).join('')}
            </tr>
          `).join('') || `<tr><td colspan="${config.columns.length + (selectable ? 1 : 0)}" class="admin-empty-cell">暂无数据</td></tr>`}
        </tbody>
      </table>
    </div>
  `
}

function serverBatchToolbar(total: number) {
  return `
    <div class="admin-batch-toolbar" data-server-batch-bar>
      <button class="admin-button is-ghost" type="button" data-select-all-servers data-batch-action="select">
        ${icon('checkSquare', 'admin-button-svg')}<span data-select-all-label>全选</span>
      </button>
      <span class="admin-batch-count"><strong data-selected-count>0</strong> / ${escapeHtml(total)} 已选</span>
      <button class="admin-button is-ghost" type="button" data-server-batch-group data-server-requires-selection data-batch-action="group" disabled>
        ${icon('group', 'admin-button-svg')}<span>批量改组</span>
      </button>
      <button class="admin-button is-ghost" type="button" data-server-force-update data-server-requires-selection data-batch-action="update" disabled>
        ${icon('upSolid', 'admin-button-svg')}<span>强制更新</span>
      </button>
      <button class="admin-button is-ghost" type="button" data-server-batch-delete data-server-requires-selection data-batch-action="delete" disabled>
        ${icon('trash', 'admin-button-svg')}<span>批量删除</span>
      </button>
    </div>
  `
}

function selectedServerIds() {
  return Array.from(contentArea.querySelectorAll<HTMLInputElement>('[data-server-select]:checked'))
    .map((input) => Number(input.value))
    .filter((id) => Number.isFinite(id) && id > 0)
}

function serverSelectionInputs() {
  return Array.from(contentArea.querySelectorAll<HTMLInputElement>('[data-server-select]'))
}

function toggleServerSelection() {
  const inputs = serverSelectionInputs()
  const shouldCheck = inputs.some((input) => !input.checked)
  inputs.forEach((input) => {
    input.checked = shouldCheck
  })
  updateServerBatchState()
}

function setAllServerSelection(checked: boolean) {
  serverSelectionInputs().forEach((input) => {
    input.checked = checked
  })
  updateServerBatchState()
}

function updateServerBatchState() {
  const inputs = serverSelectionInputs()
  const selected = inputs.filter((input) => input.checked).length
  const allSelected = inputs.length > 0 && selected === inputs.length
  contentArea.querySelectorAll<HTMLElement>('[data-selected-count]').forEach((element) => {
    element.textContent = String(selected)
  })
  contentArea.querySelectorAll<HTMLButtonElement>('[data-server-requires-selection]').forEach((button) => {
    button.disabled = selected === 0
  })
  contentArea.querySelectorAll<HTMLElement>('[data-select-all-label]').forEach((element) => {
    element.textContent = allSelected ? '取消全选' : '全选'
  })
  const selectAll = contentArea.querySelector<HTMLInputElement>('[data-server-select-all]')
  if (selectAll) {
    selectAll.checked = allSelected
    selectAll.indeterminate = selected > 0 && selected < inputs.length
  }
}

async function openEntityEditor(key: AdminKey, id = '') {
  const config = entityConfigs[key]
  if (!config) return
  dialog.innerHTML = `<section class="admin-dialog-body is-small admin-loading">加载表单数据...</section>`
  dialog.showModal()
  await loadAdminLookups()
  if (key === 'server') await loadAdminSettings().catch(() => null)

  const row = id ? findEntityRow(key, id) : undefined
  const title = row ? `编辑${config.title}` : config.addLabel
  dialog.innerHTML = `
    <form class="admin-dialog-body" data-entity-form="${key}">
      <header>
        <h2>${escapeHtml(title)}</h2>
        <button type="button" class="admin-dialog-close" data-dialog-close>×</button>
      </header>
      <div class="admin-form-grid">
        ${config.fields.map((field) => fieldMarkup(field, row)).join('')}
      </div>
      ${key === 'server' ? serverDetailPanel(row) : ''}
      ${key === 'server' ? serverInstallPanel(row) : ''}
      <footer>
        <button class="admin-button is-ghost" type="button" data-dialog-cancel data-form-action="cancel">${icon('cancel', 'admin-button-svg')}<span>取消</span></button>
        <button class="admin-button is-ghost" type="submit" data-form-action="save">${icon('edit', 'admin-button-svg')}<span>保存</span></button>
      </footer>
    </form>
  `
  bindDialogClose()
}

function openAuthDialog() {
  if (!apiKeyLoginAllowed) return
  dialog.innerHTML = `
    <form class="admin-dialog-body is-small" data-auth-form>
      <header>
        <h2>API 授权</h2>
        <button type="button" class="admin-dialog-close" data-dialog-close>×</button>
      </header>
      <label class="admin-field">
        <span>Authorization</span>
        <input name="token" type="password" autocomplete="off" value="${escapeAttribute(getStoredAuthToken())}">
      </label>
      <footer>
        <button class="admin-button is-ghost" type="button" data-clear-auth data-form-action="clear">${icon('trash', 'admin-button-svg')}<span>清除</span></button>
        <button class="admin-button is-ghost" type="submit" data-form-action="save">${icon('shield', 'admin-button-svg')}<span>保存</span></button>
      </footer>
    </form>
  `
  bindDialogClose()
  dialog.querySelector('[data-clear-auth]')?.addEventListener('click', () => {
    clearStoredAuthToken()
    dialog.close()
    renderAdminRoute()
  }, { once: true })
  dialog.showModal()
}

function openTokenDialog() {
  dialog.innerHTML = `
    <form class="admin-dialog-body is-small" data-token-form>
      <header>
        <h2>生成 Token</h2>
        <button type="button" class="admin-dialog-close" data-dialog-close>×</button>
      </header>
      <label class="admin-field">
        <span>备注</span>
        <input name="Note" type="text" autocomplete="off" placeholder="例如：自动化脚本">
      </label>
      <footer>
        <button class="admin-button is-ghost" type="button" data-dialog-cancel data-form-action="cancel">${icon('cancel', 'admin-button-svg')}<span>取消</span></button>
        <button class="admin-button is-ghost" type="submit" data-form-action="generate">${icon('key', 'admin-button-svg')}<span>生成</span></button>
      </footer>
    </form>
  `
  bindDialogClose()
  dialog.showModal()
  dialog.querySelector<HTMLInputElement>('input[name="Note"]')?.focus()
}

function bindDialogClose() {
  dialog.querySelectorAll<HTMLElement>('[data-dialog-close], [data-dialog-cancel]').forEach((button) => {
    button.addEventListener('click', () => dialog.close(), { once: true })
  })
}

function confirmAction(options: ConfirmActionOptions): Promise<boolean> {
  dialog.innerHTML = `
    <div class="admin-dialog-body is-small">
      <header>
        <h2>${escapeHtml(options.title)}</h2>
        <button type="button" class="admin-dialog-close" data-dialog-cancel>×</button>
      </header>
      <p class="admin-confirm-message">${escapeHtml(options.message)}</p>
      <footer>
        <button class="admin-button is-ghost" type="button" data-dialog-cancel data-form-action="cancel">${icon('cancel', 'admin-button-svg')}<span>取消</span></button>
        <button class="admin-button is-ghost" type="button" data-confirm-action data-form-action="${options.danger ? 'danger' : 'confirm'}">
          ${icon(options.danger ? 'trash' : 'play', 'admin-button-svg')}<span>${escapeHtml(options.confirmLabel)}</span>
        </button>
      </footer>
    </div>
  `

  return new Promise((resolve) => {
    let settled = false
    const finish = (value: boolean) => {
      if (settled) return
      settled = true
      dialog.removeEventListener('close', onClose)
      resolve(value)
    }
    const onClose = () => finish(false)

    dialog.addEventListener('close', onClose, { once: true })
    dialog.querySelectorAll<HTMLElement>('[data-dialog-cancel]').forEach((button) => {
      button.addEventListener('click', () => dialog.close(), { once: true })
    })
    dialog.querySelector<HTMLButtonElement>('[data-confirm-action]')?.addEventListener('click', () => {
      finish(true)
      dialog.close()
    }, { once: true })
    dialog.showModal()
  })
}

function setFormBusy(form: HTMLFormElement, busy: boolean, busyText = '处理中...') {
  form.classList.toggle('is-busy', busy)
  const controls = form.querySelectorAll<HTMLInputElement | HTMLButtonElement | HTMLSelectElement | HTMLTextAreaElement>('button, input, select, textarea')
  controls.forEach((control) => {
    control.disabled = busy
  })

  const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]')
  if (!submit) return

  if (busy) {
    if (!submit.dataset.idleHtml) submit.dataset.idleHtml = submit.innerHTML
    submit.innerHTML = `<span class="spinner admin-button-spinner"></span><span>${escapeHtml(busyText)}</span>`
  } else if (submit.dataset.idleHtml) {
    submit.innerHTML = submit.dataset.idleHtml
    delete submit.dataset.idleHtml
  }
}

async function submitEntityForm(form: HTMLFormElement) {
  const key = form.dataset.entityForm as AdminKey
  const config = entityConfigs[key]
  if (!config) return

  const payload = formParams(form, config.fields)
  setFormBusy(form, true, '保存中...')
  try {
    await apiFetch<ApiResponse>(config.endpoint, { method: 'POST', body: payload })
    dialog.close()
    toast('保存成功')
    await renderEntityPage(config)
  } catch (error) {
    toast(errorMessage(error), true)
  } finally {
    setFormBusy(form, false)
  }
}

async function deleteEntity(key: AdminKey, id: string) {
  const config = entityConfigs[key]
  if (!config || !id) return
  const confirmed = await confirmAction({
    title: '确认删除',
    message: `删除 #${id} 后无法从当前后台直接恢复。`,
    confirmLabel: '删除',
    danger: true,
  })
  if (!confirmed) return

  try {
    await apiFetch<ApiResponse>(adminApiPath(`/${config.deleteModel}/${encodeURIComponent(id)}`), { method: 'DELETE' })
    toast('删除成功')
    await renderEntityPage(config)
  } catch (error) {
    toast(errorMessage(error), true)
  }
}

async function openBatchGroupDialog() {
  const ids = selectedServerIds()
  if (ids.length === 0) {
    toast('请选择服务器', true)
    return
  }
  await loadAdminLookups()
  const groups = suggestionOptions('server-tags')
  const options = groups.length > 0 ? groups : ['default']
  dialog.innerHTML = `
    <form class="admin-dialog-body is-small" data-server-batch-group-form>
      <header>
        <h2>批量修改分组</h2>
        <button type="button" class="admin-dialog-close" data-dialog-close>×</button>
      </header>
      <p class="admin-confirm-message">将 ${escapeHtml(ids.length)} 台服务器移动到选中的已有分组。</p>
      <label class="admin-field">
        <span>分组</span>
        ${adminSelectMarkup('group', options.map((group) => ({ value: group, label: group })), options[0] || 'default')}
      </label>
      <footer>
        <button class="admin-button is-ghost" type="button" data-dialog-cancel data-form-action="cancel">${icon('cancel', 'admin-button-svg')}<span>取消</span></button>
        <button class="admin-button is-ghost" type="submit" data-form-action="save">${icon('group', 'admin-button-svg')}<span>保存</span></button>
      </footer>
    </form>
  `
  bindDialogClose()
  dialog.showModal()
}

async function batchUpdateServerGroup(form: HTMLFormElement) {
  const config = entityConfigs.server
  const ids = selectedServerIds()
  const group = String(new FormData(form).get('group') || '').trim()
  if (!config || ids.length === 0) {
    toast('请选择服务器', true)
    return
  }
  if (!group) {
    toast('请选择分组', true)
    return
  }

  setFormBusy(form, true, '保存中...')
  try {
    await apiFetch<ApiResponse>(adminApiPath('/batch-update-server-group'), {
      method: 'POST',
      json: { servers: ids, group },
    })
    dialog.close()
    toast('分组已更新')
    await renderEntityPage(config)
  } catch (error) {
    toast(errorMessage(error), true)
  } finally {
    setFormBusy(form, false)
  }
}

async function batchDeleteServers() {
  const config = entityConfigs.server
  const ids = selectedServerIds()
  if (!config || ids.length === 0) {
    toast('请选择服务器', true)
    return
  }
  const confirmed = await confirmAction({
    title: '批量删除服务器',
    message: `确认删除选中的 ${ids.length} 台服务器？该操作无法从当前后台直接恢复。`,
    confirmLabel: '删除',
    danger: true,
  })
  if (!confirmed) return

  try {
    await apiFetch<ApiResponse>(adminApiPath('/batch-delete-server'), { method: 'POST', json: ids })
    toast('服务器已删除')
    await renderEntityPage(config)
  } catch (error) {
    toast(errorMessage(error), true)
  }
}

async function forceUpdateServers() {
  const ids = selectedServerIds()
  if (ids.length === 0) {
    toast('请选择服务器', true)
    return
  }
  const confirmed = await confirmAction({
    title: '强制更新 Agent',
    message: `确认向选中的 ${ids.length} 台服务器下发 Agent 更新指令？离线服务器会返回离线结果。`,
    confirmLabel: '下发',
  })
  if (!confirmed) return

  try {
    const response = await apiFetch<ApiResponse>(adminApiPath('/force-update'), { method: 'POST', json: ids })
    toast(plainTextFromHtml(response.message || '更新指令已下发'))
  } catch (error) {
    toast(errorMessage(error), true)
  }
}

async function manualCron(id: string) {
  if (!id) return
  const confirmed = await confirmAction({
    title: '手动执行任务',
    message: `确认立即触发计划任务 #${id}？`,
    confirmLabel: '执行',
  })
  if (!confirmed) return
  try {
    await apiFetch<ApiResponse>(adminApiPath(`/cron/${encodeURIComponent(id)}/manual`), { method: 'POST' })
    toast('任务已触发')
  } catch (error) {
    toast(errorMessage(error), true)
  }
}

async function issueApiToken(form: HTMLFormElement) {
  setFormBusy(form, true, '生成中...')
  try {
    const payload = new URLSearchParams()
    const note = new FormData(form).get('Note')
    payload.set('Note', String(note || ''))
    await apiFetch<ApiResponse>(adminApiPath('/token'), { method: 'POST', body: payload })
    form.reset()
    if (dialog.open) dialog.close()
    toast('Token 已生成')
    await renderApiTokens()
  } catch (error) {
    toast(errorMessage(error), true)
  } finally {
    setFormBusy(form, false)
  }
}

async function deleteApiToken(token: string) {
  if (!token) return
  const confirmed = await confirmAction({
    title: '删除 Token',
    message: '删除后使用该 Authorization 的 API 请求会立即失效。',
    confirmLabel: '删除',
    danger: true,
  })
  if (!confirmed) return
  try {
    await apiFetch<ApiResponse>(adminApiPath(`/token/${encodeURIComponent(token)}`), { method: 'DELETE' })
    toast('Token 已删除')
    await renderApiTokens()
  } catch (error) {
    toast(errorMessage(error), true)
  }
}

async function submitSettings(form: HTMLFormElement) {
  const checkboxNames = ['EnableIPChangeNotification', 'EnablePlainIPInNotification', 'EnableOAuthLogin', 'EnableAPIKeyLogin']
  const payload = new URLSearchParams()
  const data = new FormData(form)
  for (const [key, value] of data.entries()) {
    payload.set(key, String(value))
  }
  for (const name of checkboxNames) {
    const input = form.elements.namedItem(name) as HTMLInputElement | null
    payload.set(name, input?.checked ? 'on' : '')
  }

  setFormBusy(form, true, '保存中...')
  try {
    await apiFetch<ApiResponse>(adminApiPath('/setting'), { method: 'POST', body: payload })
    toast('设置已保存')
    await loadAdminProfile()
    await renderSettings()
  } catch (error) {
    toast(errorMessage(error), true)
  } finally {
    setFormBusy(form, false)
  }
}

async function logout() {
  clearStoredAuthToken()
  try {
    await apiFetch<ApiResponse>(authApiPath('/logout'), { method: 'POST' })
  } catch {
    // The local token is cleared even if cookie logout is not available.
  }
  window.dispatchEvent(new CustomEvent('app-navigate', { detail: '/login' }))
}

async function apiFetch<T>(url: string, options: ApiFetchOptions = {}): Promise<T> {
  const headers = authHeaders()
  const body = options.json === undefined ? options.body : JSON.stringify(options.json)
  if (options.json !== undefined) headers.set('Content-Type', 'application/json')
  const response = await fetch(url, {
    method: options.method || 'GET',
    body,
    headers,
    credentials: 'include',
  })
  const payload = await response.json().catch(() => ({})) as ApiResponse
  if (!response.ok || payload.code === 403) {
    throw new Error(payload.message || `HTTP ${response.status}`)
  }
  if (typeof payload.code === 'number' && payload.code >= 400) {
    throw new Error(payload.message || '请求失败')
  }
  return payload as T
}

function formParams(form: HTMLFormElement, fields: FieldConfig[]) {
  const formData = new FormData(form)
  const payload = new URLSearchParams()
  for (const field of fields) {
    if (field.type === 'checkbox') {
      const input = form.elements.namedItem(field.name) as HTMLInputElement | null
      payload.set(field.name, input?.checked ? 'on' : '')
    } else {
      payload.set(field.name, String(formData.get(field.name) ?? ''))
    }
  }
  return payload
}

function fieldMarkup(field: FieldConfig, row?: Row) {
  if (field.type === 'hidden') {
    return `<input type="hidden" name="${field.name}" value="${escapeAttribute(fieldValue(field, row))}">`
  }
  if (field.type === 'relation' && field.relation) {
    return relationMarkup(field, row)
  }
  if (field.type === 'checkbox') {
    return checkboxMarkup(field.name, field.label, fieldValue(field, row))
  }
  if (field.type === 'select') {
    return selectMarkup(field.name, field.label, field.options?.map((option) => [option.value, option.label]) || [], fieldValue(field, row))
  }
  if (field.type === 'combo') {
    return comboMarkup(field.name, field.label, field.suggestions, fieldValue(field, row))
  }
  if (field.type === 'textarea') {
    return textareaMarkup(field.name, field.label, fieldValue(field, row), field.rows || 3, field.placeholder)
  }
  return inputMarkup(field.name, field.label, fieldValue(field, row), field.type, field.placeholder, field.suggestions)
}

function fieldValue(field: FieldConfig, row?: Row) {
  const current = row ? get(row, field.name) : undefined
  if (current !== undefined && current !== null) return normalizeFieldValue(current)
  if (field.defaultValue !== undefined) return normalizeFieldValue(field.defaultValue)
  return ''
}

function normalizeFieldValue(input: unknown) {
  if (typeof input === 'boolean') return input ? 'on' : ''
  if (Array.isArray(input)) return JSON.stringify(input)
  if (typeof input === 'object' && input !== null) return JSON.stringify(input)
  return String(input)
}

function hiddenField(name: string): FieldConfig {
  return { name, label: name, type: 'hidden' }
}

function textField(name: string, label: string, defaultValue: string | number | boolean = '', placeholder = '', suggestions?: FieldConfig['suggestions']): FieldConfig {
  return { name, label, type: 'text', defaultValue, placeholder, suggestions }
}

function comboField(name: string, label: string, suggestions: NonNullable<FieldConfig['suggestions']>, defaultValue: string | number | boolean = ''): FieldConfig {
  return { name, label, type: 'combo', defaultValue, suggestions }
}

function numberField(name: string, label: string, defaultValue: number): FieldConfig {
  return { name, label, type: 'number', defaultValue }
}

function textareaField(name: string, label: string, rows = 3, defaultValue = '', placeholder = ''): FieldConfig {
  return { name, label, type: 'textarea', rows, defaultValue, placeholder }
}

function checkboxField(name: string, label: string, defaultValue = false): FieldConfig {
  return { name, label, type: 'checkbox', defaultValue }
}

function selectField(name: string, label: string, pairs: Array<[string, string]>): FieldConfig {
  return { name, label, type: 'select', options: pairs.map(([value, optionLabel]) => ({ value, label: optionLabel })) }
}

function relationField(name: string, label: string, source: AdminKey, multiple = false, filter?: (row: Row) => boolean): FieldConfig {
  return {
    name,
    label,
    type: 'relation',
    relation: {
      source,
      multiple,
      filter,
      valuePath: 'ID',
      label: relationLabel,
    },
    defaultValue: multiple ? '[]' : '',
  }
}

function inputMarkup(name: string, label: string, fieldValueInput: unknown, type = 'text', placeholder = '', suggestions?: FieldConfig['suggestions']) {
  const listId = suggestions ? `admin-list-${name}` : ''
  return `
    <label class="admin-field">
      <span>${escapeHtml(label)}</span>
      <input name="${name}" type="${type}" value="${escapeAttribute(normalizeFieldValue(fieldValueInput))}" placeholder="${escapeAttribute(placeholder)}"${listId ? ` list="${escapeAttribute(listId)}"` : ''}>
      ${listId ? `<datalist id="${escapeAttribute(listId)}">${suggestionOptions(suggestions).map((item) => `<option value="${escapeAttribute(item)}"></option>`).join('')}</datalist>` : ''}
    </label>
  `
}

function textareaMarkup(name: string, label: string, fieldValueInput: unknown, rows = 3, placeholder = '') {
  return `
    <label class="admin-field is-wide">
      <span>${escapeHtml(label)}</span>
      <textarea name="${name}" rows="${rows}" placeholder="${escapeAttribute(placeholder)}">${escapeHtml(normalizeFieldValue(fieldValueInput))}</textarea>
    </label>
  `
}

function checkboxMarkup(name: string, label: string, fieldValueInput: unknown) {
  return `
    <label class="admin-check">
      <input name="${name}" type="checkbox" ${toBool(fieldValueInput) ? 'checked' : ''}>
      <span>${escapeHtml(label)}</span>
    </label>
  `
}

function selectMarkup(name: string, label: string, options: Array<[string, string]>, fieldValueInput: unknown) {
  const selected = normalizeFieldValue(fieldValueInput)
  const normalized = options.map(([optionValue, optionLabel]) => ({ value: optionValue, label: optionLabel }))
  return `
    <label class="admin-field">
      <span>${escapeHtml(label)}</span>
      ${adminSelectMarkup(name, normalized, selected)}
    </label>
  `
}

function comboMarkup(name: string, label: string, suggestions: FieldConfig['suggestions'], fieldValueInput: unknown) {
  const options = suggestionOptions(suggestions)
  const selected = normalizeFieldValue(fieldValueInput) || 'default'
  const values = uniqueStrings([selected, ...options].filter(Boolean))
  return `
    <label class="admin-field">
      <span>${escapeHtml(label)}</span>
      <div class="admin-combo" data-admin-combo>
        <input type="hidden" name="${name}" value="${escapeAttribute(selected)}" data-admin-combo-value>
        <button class="admin-select-trigger" type="button" data-admin-combo-toggle>
          <span data-admin-combo-label>${escapeHtml(selected)}</span>
          ${icon('chevronDown', 'admin-select-chevron')}
        </button>
        <div class="admin-select-panel" data-admin-combo-panel hidden>
          <input class="admin-combo-input" type="text" value="${escapeAttribute(selected)}" placeholder="输入或选择${escapeAttribute(label)}" data-admin-combo-input>
          <div class="admin-select-options">
            ${values.map((optionValue) => `
              <button type="button" class="admin-select-option" data-admin-combo-option="${escapeAttribute(optionValue)}">
                ${escapeHtml(optionValue)}
              </button>
            `).join('')}
          </div>
        </div>
      </div>
    </label>
  `
}

function adminSelectMarkup(name: string, options: SelectOption[], selected: string) {
  const current = options.find((option) => option.value === selected) || options[0]
  const valueInput = current?.value || ''
  const labelInput = current?.label || '未选择'
  return `
    <div class="admin-select" data-admin-select>
      <input type="hidden" name="${name}" value="${escapeAttribute(valueInput)}" data-admin-select-value>
      <button class="admin-select-trigger" type="button" data-admin-select-toggle>
        <span data-admin-select-label>${escapeHtml(labelInput)}</span>
        ${icon('chevronDown', 'admin-select-chevron')}
      </button>
      <div class="admin-select-panel" data-admin-select-panel hidden>
        <div class="admin-select-options">
          ${options.map((option) => `
            <button type="button" class="admin-select-option ${option.value === valueInput ? 'is-selected' : ''}" data-admin-select-option="${escapeAttribute(option.value)}" data-admin-select-label-value="${escapeAttribute(option.label)}">
              ${escapeHtml(option.label)}
            </button>
          `).join('')}
        </div>
      </div>
    </div>
  `
}

function serverNameMarkup(row: Row) {
  return `<strong>${escapeHtml(value(row, 'Name') || '-')}</strong>`
}

function serverIpMarkup(row: Row) {
  const ipv4 = value(row, 'IPv4')
  const ipv6 = value(row, 'IPv6')
  const raw = value(row, 'HostIP') || value(row, 'Host.IP')
  if (!ipv4 && !ipv6 && !raw) return '<span class="admin-muted">-</span>'

  const items = [
    ipv4 ? copyChip('IPv4', ipv4) : '',
    ipv6 ? copyChip('IPv6', ipv6) : '',
  ].filter(Boolean)

  return `
    <div class="admin-ip-stack">
      ${items.join('') || copyChip('IP', raw)}
    </div>
  `
}

function serverSystemMarkup(row: Row) {
  const platform = value(row, 'Host.Platform')
  const version = value(row, 'Host.PlatformVersion')
  const arch = value(row, 'Host.Arch')
  const title = [formatPlatformName(platform), version, arch].filter(Boolean).join(' ')
  if (!platform) return '<span class="admin-muted">-</span>'
  return `
    <span class="admin-system-icon" title="${escapeAttribute(title)}">
      ${adminOsIcon(platform)}
    </span>
  `
}

function secretMarkup(row: Row) {
  const secret = value(row, 'Secret')
  if (!secret) return '<span class="admin-muted">-</span>'
  return `
    <div class="admin-secret-cell">
      <code title="${escapeAttribute(secret)}">${escapeHtml(secret)}</code>
      <button class="admin-icon-btn" type="button" data-copy-value="${escapeAttribute(secret)}" title="复制密钥" aria-label="复制密钥">${icon('copy')}</button>
    </div>
  `
}

function installButtons(row: Row) {
  const secret = value(row, 'Secret')
  if (!secret) return '<span class="admin-muted">-</span>'
  const ip = value(row, 'HostIP') || value(row, 'IPv4') || value(row, 'IPv6')
  const country = countryCode(row)
  return `
    <div class="admin-install-actions">
      <button class="admin-icon-btn" type="button" data-copy-install="linux" data-secret="${escapeAttribute(secret)}" data-ip="${escapeAttribute(ip)}" data-country="${escapeAttribute(country)}" title="复制安装脚本" aria-label="复制安装脚本">${icon('copy')}</button>
    </div>
  `
}

function notesMarkup(row: Row) {
  const note = value(row, 'Note')
  const publicNote = value(row, 'PublicNote')
  if (!note && !publicNote) return '<span class="admin-muted">-</span>'
  return `
    <div class="admin-note-stack">
      ${note ? `<span title="${escapeAttribute(note)}">${icon('edit', 'admin-note-svg')} ${escapeHtml(truncate(note, 28))}</span>` : ''}
      ${publicNote ? `<span title="${escapeAttribute(publicNote)}">${icon('info', 'admin-note-svg')} ${escapeHtml(truncate(publicNote, 28))}</span>` : ''}
    </div>
  `
}

function ddnsProfilesMarkup(row: Row) {
  if (!toBool(get(row, 'EnableDDNS'))) return '<span class="admin-badge">停用</span>'
  const ids = parseIdList(get(row, 'DDNSProfilesRaw'))
  if (ids.length === 0) return '<span class="admin-badge is-green">启用</span>'
  return `
    <div class="admin-badge-list">
      ${ids.map((id) => `<span class="admin-badge is-green">${escapeHtml(ddnsName(id))}</span>`).join('')}
    </div>
  `
}

function monitorCoverMarkup(row: Row) {
  const ids = parseIdList(get(row, 'SkipServersRaw'))
  const cover = Number(get(row, 'Cover'))
  const label = cover === 1 ? '仅包含' : '排除'
  if (ids.length === 0) return badge(cover === 1 ? '未选择' : '全部')
  return `
    <div class="admin-badge-list">
      <span class="admin-badge">${label}</span>
      ${ids.slice(0, 3).map((id) => `<span class="admin-badge">${escapeHtml(serverLabel(id))}</span>`).join('')}
      ${ids.length > 3 ? `<span class="admin-muted">+${ids.length - 3}</span>` : ''}
    </div>
  `
}

function cronServersMarkup(row: Row) {
  const cover = Number(get(row, 'Cover'))
  if (cover === 2) return badge('触发服务器')
  const ids = parseIdList(get(row, 'ServersRaw'))
  if (ids.length === 0) return badge(cover === 1 ? '排除空列表' : '未选择')
  const prefix = cover === 1 ? '排除' : '指定'
  return `
    <div class="admin-badge-list">
      <span class="admin-badge">${prefix}</span>
      ${ids.slice(0, 3).map((id) => `<span class="admin-badge">${escapeHtml(serverLabel(id))}</span>`).join('')}
      ${ids.length > 3 ? `<span class="admin-muted">+${ids.length - 3}</span>` : ''}
    </div>
  `
}

function triggerTasksMarkup(row: Row) {
  const fail = parseIdList(get(row, 'FailTriggerTasksRaw'))
  const recover = parseIdList(get(row, 'RecoverTriggerTasksRaw'))
  if (fail.length === 0 && recover.length === 0) return '<span class="admin-muted">-</span>'
  return `
    <div class="admin-badge-list">
      ${fail.length ? `<span class="admin-badge is-red">失败 ${fail.map(taskLabel).join(', ')}</span>` : ''}
      ${recover.length ? `<span class="admin-badge is-green">恢复 ${recover.map(taskLabel).join(', ')}</span>` : ''}
    </div>
  `
}

function serverRefMarkup(id: string) {
  if (!id) return '<span class="admin-muted">-</span>'
  return `<span class="admin-badge">${escapeHtml(serverLabel(id))}</span>`
}

function copyTextCell(displayText: string, title: string, copyValue = displayText) {
  if (!copyValue) return '<span class="admin-muted">-</span>'
  return `
    <div class="admin-copy-cell">
      <code title="${escapeAttribute(copyValue)}">${escapeHtml(displayText || copyValue)}</code>
      <button class="admin-icon-btn" type="button" data-copy-value="${escapeAttribute(copyValue)}" title="${escapeAttribute(title)}" aria-label="${escapeAttribute(title)}">${icon('copy')}</button>
    </div>
  `
}

function secretValueCell(secret: string, title: string) {
  if (!secret) return '<span class="admin-muted">-</span>'
  return `
    <div class="admin-secret-cell">
      <code title="${escapeAttribute(secret)}">${escapeHtml(secret)}</code>
      <button class="admin-icon-btn" type="button" data-copy-value="${escapeAttribute(secret)}" title="${escapeAttribute(title)}" aria-label="${escapeAttribute(title)}">${icon('copy')}</button>
    </div>
  `
}

function apiTokenValue(row: Row) {
  return value(row, 'Token') || value(row, 'token')
}

function apiTokenNote(row: Row) {
  return value(row, 'Note') || value(row, 'note')
}

function serverLabel(id: string | number) {
  const found = (entityRows.get('server') || []).find((row) => value(row, 'ID') === String(id))
  return found ? `#${id} ${value(found, 'Name') || '服务器'}` : `#${id}`
}

function taskLabel(id: string | number) {
  const found = (entityRows.get('cron') || []).find((row) => value(row, 'ID') === String(id))
  return found ? `#${id} ${value(found, 'Name') || '任务'}` : `#${id}`
}

function copyChip(label: string, text: string) {
  return `
    <button class="admin-copy-chip" type="button" data-copy-value="${escapeAttribute(text)}" title="复制 ${escapeAttribute(label)}">
      <span>${escapeHtml(label)}</span><code>${escapeHtml(text)}</code>${icon('copy', 'admin-chip-svg')}
    </button>
  `
}

function adminOsIcon(platform: string) {
  const normalized = platform.toLowerCase()
  if (!platform) return ''
  if (normalized.includes('windows')) return icon('windows', 'admin-os-svg os-windows-svg')
  if (normalized.includes('darwin') || normalized.includes('mac')) return icon('apple', 'admin-os-svg icon-violet')
  const logo = getFontLogoClass(platform)
  if (logo) return `<i class="fl-${logo} os-font-logo admin-os-font" aria-hidden="true"></i>`
  return icon('tux', 'admin-os-svg')
}

function formatPlatformName(platform: string) {
  if (!platform) return ''
  const mapping: Record<string, string> = {
    ubuntu: 'Ubuntu',
    debian: 'Debian',
    centos: 'CentOS',
    darwin: 'MacOS',
    redhat: 'RedHat',
    archlinux: 'Archlinux',
    fedora: 'Fedora',
    alpine: 'Alpine',
    opensuse: 'OpenSuse',
  }
  const normalized = platform.toLowerCase()
  return mapping[normalized] || platform
}

function getFontLogoClass(platform: string) {
  const value = platform.toLowerCase()
  if (value.includes('centos')) return 'centos'
  if (value.includes('ubuntu')) return 'ubuntu'
  if (value.includes('debian')) return 'debian'
  if (value.includes('alpine')) return 'alpine'
  if (value.includes('freebsd')) return 'freebsd'
  if (value.includes('redhat')) return 'redhat'
  if (value.includes('opensuse')) return 'opensuse'
  if (value.includes('fedora')) return 'fedora'
  if (value.includes('arch')) return 'archlinux'
  if (value.includes('coreos')) return 'coreos'
  if (value.includes('deepin')) return 'deepin'
  return ''
}

function ddnsName(id: string | number) {
  const found = (entityRows.get('ddns') || []).find((row) => value(row, 'ID') === String(id))
  return found ? `#${id} ${value(found, 'Name') || 'DDNS'}` : `#${id}`
}

function triggerTaskFilter(row: Row) {
  return Number(get(row, 'TaskType')) === 1
}

function suggestionOptions(kind: FieldConfig['suggestions']) {
  if (kind === 'server-tags') {
    return uniqueStrings((entityRows.get('server') || []).map((row) => value(row, 'Tag')).filter(Boolean))
  }
  if (kind === 'notification-tags') {
    return uniqueStrings((entityRows.get('notification') || []).map((row) => value(row, 'Tag')).filter(Boolean))
  }
  return []
}

function uniqueStrings(items: string[]) {
  return Array.from(new Set(items)).sort((a, b) => a.localeCompare(b))
}

function parseIdList(input: unknown) {
  if (Array.isArray(input)) return input.map(String).filter(Boolean)
  const text = normalizeFieldValue(input).trim()
  if (!text) return []
  try {
    const parsed = JSON.parse(text)
    if (Array.isArray(parsed)) return parsed.map(String).filter(Boolean)
  } catch {
    // Fall through to permissive parsing for legacy comma-separated values.
  }
  return text.split(/[,\s]+/).map((item) => item.replace(/^\[|\]$/g, '').trim()).filter(Boolean)
}

async function copyInstallScript(button: HTMLElement) {
  const type = button.dataset.copyInstall || 'linux'
  const secret = button.dataset.secret || ''
  const country = button.dataset.country || ''
  if (!secret) return
  await loadAdminSettings()
  const script = type === 'windows'
    ? windowsInstallScript(secret)
    : linuxInstallScript(secret, country)
  await copyText(script)
}

async function loadAdminSettings() {
  if (settingsCache) return settingsCache
  const payload = await apiFetch<Row>(adminApiPath('/setting'))
  settingsCache = objectFrom(get(payload, 'Settings'))
  return settingsCache
}

function linuxInstallScript(secret: string, country: string) {
  return linuxInstallScriptFromSource(secret, country && country !== 'cn' ? 'github' : 'gitee')
}

function linuxInstallScriptFromSource(secret: string, source: 'github' | 'gitee') {
  const settings = settingsCache || {}
  const host = value(settings, 'GRPCHost')
  if (!host) return '请先在设置中配置 GRPCHost'
  const port = value(settings, 'ProxyGRPCPort') || value(settings, 'GRPCPort')
  const tls = toBool(get(settings, 'TLS')) ? ' --tls' : ''
  const baseUrl = source === 'github'
    ? 'https://raw.githubusercontent.com/xos/serverstatus/master'
    : 'https://gitee.com/ten/ServerStatus/raw/master'
  return `curl -L ${baseUrl}/script/server-status.sh -o server-status.sh && chmod +x server-status.sh && sudo ./server-status.sh install_agent ${host} ${port} ${secret}${tls}`
}

function windowsInstallScript(secret: string) {
  return windowsInstallScriptFromSource(secret, 'global')
}

function windowsInstallScriptFromSource(secret: string, source: 'global' | 'china') {
  const settings = settingsCache || {}
  const host = value(settings, 'GRPCHost')
  if (!host) return '请先在设置中配置 GRPCHost'
  const port = value(settings, 'ProxyGRPCPort') || value(settings, 'GRPCPort')
  const tls = toBool(get(settings, 'TLS')) ? ' --tls' : ''
  const baseUrl = source === 'global'
    ? 'https://fastly.jsdelivr.net/gh/xos/serverstatus@master'
    : 'https://gitee.com/ten/ServerStatus/raw/master'
  return `[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Ssl3 -bor [Net.SecurityProtocolType]::Tls -bor [Net.SecurityProtocolType]::Tls11 -bor [Net.SecurityProtocolType]::Tls12;set-ExecutionPolicy RemoteSigned;Invoke-WebRequest ${baseUrl}/script/server-status.ps1 -OutFile C:\\server-status.ps1;powershell.exe C:\\server-status.ps1 ${host}:${port} ${secret}${tls}`
}

function serverInstallPanel(row?: Row) {
  const secret = row ? value(row, 'Secret') : ''
  if (!secret) {
    return `
      <section class="admin-script-panel">
        <header><strong>安装脚本</strong><span>保存服务器后生成密钥，再复制安装脚本。</span></header>
      </section>
    `
  }

  const platform = row ? value(row, 'Host.Platform') : ''
  const windows = isWindowsPlatform(platform)
  return `
    <section class="admin-script-panel">
      <header>
        <strong>安装脚本</strong>
        <span>当前匹配 ${windows ? 'Windows' : 'Linux'}，同时提供 Global / China 源。</span>
      </header>
      ${windows
        ? [
          scriptBlock('Windows · Global', windowsInstallScriptFromSource(secret, 'global'), 'windowsScript', 'script-windows-svg'),
          scriptBlock('Windows · China', windowsInstallScriptFromSource(secret, 'china'), 'chinaSource', 'script-china-svg'),
        ].join('')
        : [
          scriptBlock('Linux · Global', linuxInstallScriptFromSource(secret, 'github'), 'linuxScript', 'script-linux-svg'),
          scriptBlock('Linux · China', linuxInstallScriptFromSource(secret, 'gitee'), 'chinaSource', 'script-china-svg'),
        ].join('')}
    </section>
  `
}

function serverDetailPanel(row?: Row) {
  if (!row) {
    return `
      <section class="admin-detail-panel">
        <header><strong>运行详情</strong><span>保存服务器并等待 Agent 上报后显示。</span></header>
      </section>
    `
  }

  const status = toBool(get(row, 'is_online')) ? '<span class="admin-badge is-green">在线</span>' : '<span class="admin-badge is-red">离线</span>'
  const platform = value(row, 'Host.Platform')
  const platformTitle = [formatPlatformName(platform), value(row, 'Host.PlatformVersion')].filter(Boolean).join(' ')
  const ipMarkup = serverIpMarkup(row)
  return `
    <section class="admin-detail-panel">
      <header>
        <strong>运行详情</strong>
        <span>来自数据库缓存和 Agent 当前上报数据。</span>
      </header>
      <div class="admin-detail-grid">
        ${detailItem('状态', status, true)}
        ${detailItem('系统', platform ? `<span class="admin-detail-os">${adminOsIcon(platform)}${escapeHtml(platformTitle || platform)}</span>` : '<span class="admin-muted">-</span>', true)}
        ${detailItem('架构', escapeHtml(value(row, 'Host.Arch') || '-'), true)}
        ${detailItem('虚拟化', escapeHtml(formatVirtualization(value(row, 'Host.Virtualization')) || '-'), true)}
        ${detailItem('Agent 版本', escapeHtml(value(row, 'Host.Version') || '-'), true)}
        ${detailItem('最后在线', escapeHtml(formatDate(get(row, 'LastOnline'))), true)}
        ${detailItem('IP', ipMarkup, true, 'is-wide')}
      </div>
    </section>
  `
}

function detailItem(label: string, content: string, raw = false, className = '') {
  return `
    <div class="admin-detail-item ${className}">
      <span>${escapeHtml(label)}</span>
      <div class="admin-detail-value">${raw ? content : escapeHtml(content)}</div>
    </div>
  `
}

function formatVirtualization(input: string) {
  if (!input) return ''
  const normalized = input.toLowerCase()
  const mapping: Record<string, string> = {
    kvm: 'KVM',
    vmware: 'VMware',
    hyperv: 'Hyper-V',
    docker: 'Docker',
    lxc: 'LXC',
    openvz: 'OpenVZ',
    xen: 'Xen',
  }
  return mapping[normalized] || input
}

function isWindowsPlatform(platform: string) {
  return platform.toLowerCase().includes('windows')
}

function scriptBlock(label: string, script: string, iconName: string, iconClass = '') {
  return `
    <div class="admin-script-block">
      <div class="admin-script-head">
        <span>${icon(iconName, `admin-button-svg ${iconClass}`)} ${escapeHtml(label)}</span>
        <button class="admin-icon-btn" type="button" data-copy-value="${escapeAttribute(script)}" title="复制 ${escapeAttribute(label)} 安装脚本" aria-label="复制 ${escapeAttribute(label)} 安装脚本">${icon('copy')}</button>
      </div>
      <pre>${escapeHtml(script)}</pre>
    </div>
  `
}

function relationMarkup(field: FieldConfig, row?: Row) {
  const relation = field.relation
  if (!relation) return ''

  const rows = relationRows(relation)
  const selected = relation.multiple
    ? selectedRelationValues(fieldValue(field, row))
    : new Set([normalizeFieldValue(fieldValue(field, row))].filter(Boolean))

  if (!relation.multiple) {
    const current = Array.from(selected)[0] || ''
    const options: SelectOption[] = [
      { value: '', label: '未选择' },
      ...rows.map((item) => {
        const optionValue = relationValue(item, relation)
        return { value: optionValue, label: relation.label(item) }
      }),
    ]
    return `
      <label class="admin-field">
        <span>${escapeHtml(field.label)}</span>
        ${adminSelectMarkup(field.name, options, current)}
      </label>
    `
  }

  const hiddenValue = JSON.stringify(Array.from(selected).map(normalizeRelationOutputValue))
  if (field.name === 'DDNSProfilesRaw') {
    return `
      <div class="admin-field is-wide admin-relation-field">
        <span>${escapeHtml(field.label)}</span>
        <input type="hidden" name="${field.name}" value="${escapeAttribute(hiddenValue)}">
        <div class="admin-choice-list is-direct" data-relation-group="${escapeAttribute(field.name)}">
          ${rows.map((item) => {
            const optionValue = relationValue(item, relation)
            return `
              <label class="admin-choice">
                <input type="checkbox" data-relation-option="${escapeAttribute(field.name)}" value="${escapeAttribute(optionValue)}" ${selected.has(optionValue) ? 'checked' : ''}>
                <span>${escapeHtml(relation.label(item))}</span>
              </label>
            `
          }).join('') || '<span class="admin-choice-empty">暂无可选数据</span>'}
        </div>
      </div>
    `
  }

  const summary = relationSummary(rows, relation, selected)
  return `
    <div class="admin-field is-wide admin-relation-field">
      <span>${escapeHtml(field.label)}</span>
      <input type="hidden" name="${field.name}" value="${escapeAttribute(hiddenValue)}">
      <details class="admin-relation-dropdown">
        <summary>
          <span data-relation-summary="${escapeAttribute(field.name)}">${escapeHtml(summary)}</span>
          ${icon('chevronDown', 'admin-select-chevron')}
        </summary>
        <div class="admin-choice-list" data-relation-group="${escapeAttribute(field.name)}">
          ${rows.map((item) => {
            const optionValue = relationValue(item, relation)
            return `
              <label class="admin-choice">
                <input type="checkbox" data-relation-option="${escapeAttribute(field.name)}" value="${escapeAttribute(optionValue)}" ${selected.has(optionValue) ? 'checked' : ''}>
                <span>${escapeHtml(relation.label(item))}</span>
              </label>
            `
          }).join('') || '<span class="admin-choice-empty">暂无可选数据</span>'}
        </div>
      </details>
    </div>
  `
}

function relationSummary(rows: Row[], relation: RelationConfig, selected: Set<string>) {
  if (selected.size === 0) return '未选择'
  const labels = Array.from(selected).map((id) => {
    const found = rows.find((row) => relationValue(row, relation) === id)
    return found ? relation.label(found) : `#${id}`
  })
  const shown = labels.slice(0, 2).join('、')
  return labels.length > 2 ? `${shown} 等 ${labels.length} 项` : shown
}

function relationRows(relation: RelationConfig) {
  const rows = entityRows.get(relation.source) || []
  return relation.filter ? rows.filter(relation.filter) : rows
}

function relationValue(row: Row, relation: RelationConfig) {
  return value(row, relation.valuePath || 'ID')
}

function relationLabel(row: Row) {
  const id = value(row, 'ID')
  const name = value(row, 'Name') || value(row, 'Tag') || value(row, 'Domain') || id
  if (value(row, 'ServerID') && !value(row, 'Name')) return `#${value(row, 'ServerID')} ${value(row, 'Domain')}`
  return id ? `#${id} ${name}` : name
}

function selectedRelationValues(input: unknown) {
  const values = parseIdList(input)
  return new Set(values.map(String))
}

function normalizeRelationOutputValue(input: string) {
  return /^\d+$/.test(input) ? Number(input) : input
}

function updateRelationValue(option: HTMLInputElement) {
  const name = option.dataset.relationOption || ''
  const group = option.closest<HTMLElement>(`[data-relation-group="${cssEscape(name)}"]`)
  const field = group?.closest<HTMLElement>('.admin-relation-field')
  const hidden = field?.querySelector<HTMLInputElement>(`input[type="hidden"][name="${cssEscape(name)}"]`)
  if (!group || !hidden) return
  const checked = Array.from(group.querySelectorAll<HTMLInputElement>('[data-relation-option]:checked'))
  const values = checked
    .map((item) => item.value)
    .map(normalizeRelationOutputValue)
  hidden.value = JSON.stringify(values)
  const summary = field?.querySelector<HTMLElement>(`[data-relation-summary="${cssEscape(name)}"]`)
  if (summary) {
    const labels = checked.map((item) => item.closest('label')?.querySelector('span')?.textContent?.trim() || item.value)
    const shown = labels.slice(0, 2).join('、')
    summary.textContent = labels.length === 0 ? '未选择' : labels.length > 2 ? `${shown} 等 ${labels.length} 项` : shown
  }
}

function toggleAdminDropdown(toggle: HTMLElement) {
  const root = toggle.closest<HTMLElement>('[data-admin-select], [data-admin-combo]')
  if (!root) return
  const panel = root.querySelector<HTMLElement>('[data-admin-select-panel], [data-admin-combo-panel]')
  if (!panel) return
  const shouldOpen = panel.hidden !== false
  closeAdminDropdowns(root)
  panel.hidden = !shouldOpen
  root.classList.toggle('is-open', shouldOpen)
  if (shouldOpen) {
    const comboInput = root.querySelector<HTMLInputElement>('[data-admin-combo-input]')
    comboInput?.focus()
    comboInput?.select()
  }
}

function closeAdminDropdowns(target?: HTMLElement) {
  app.querySelectorAll<HTMLElement>('[data-admin-select], [data-admin-combo]').forEach((root) => {
    if (target && root.contains(target)) return
    const panel = root.querySelector<HTMLElement>('[data-admin-select-panel], [data-admin-combo-panel]')
    if (panel) panel.hidden = true
    root.classList.remove('is-open')
  })
}

function setAdminSelectValue(option: HTMLElement) {
  const root = option.closest<HTMLElement>('[data-admin-select]')
  if (!root) return
  const valueInput = root.querySelector<HTMLInputElement>('[data-admin-select-value]')
  const label = root.querySelector<HTMLElement>('[data-admin-select-label]')
  const value = option.dataset.adminSelectOption || ''
  const labelValue = option.dataset.adminSelectLabelValue || value
  if (valueInput) valueInput.value = value
  if (label) label.textContent = labelValue
  root.querySelectorAll<HTMLElement>('[data-admin-select-option]').forEach((item) => {
    item.classList.toggle('is-selected', item === option)
  })
  closeAdminDropdowns()
}

function setAdminComboValue(option: HTMLElement) {
  const root = option.closest<HTMLElement>('[data-admin-combo]')
  if (!root) return
  const value = option.dataset.adminComboOption || ''
  const hidden = root.querySelector<HTMLInputElement>('[data-admin-combo-value]')
  const input = root.querySelector<HTMLInputElement>('[data-admin-combo-input]')
  const label = root.querySelector<HTMLElement>('[data-admin-combo-label]')
  if (hidden) hidden.value = value
  if (input) input.value = value
  if (label) label.textContent = value || '未填写'
  closeAdminDropdowns()
}

function updateAdminComboInput(input: HTMLInputElement) {
  const root = input.closest<HTMLElement>('[data-admin-combo]')
  if (!root) return
  const hidden = root.querySelector<HTMLInputElement>('[data-admin-combo-value]')
  const label = root.querySelector<HTMLElement>('[data-admin-combo-label]')
  const text = input.value.trim()
  if (hidden) hidden.value = text
  if (label) label.textContent = text || '未填写'
}

function actionButtons(key: AdminKey, row: Row) {
  const id = value(row, 'ID')
  const secret = value(row, 'Secret')
  return `
    ${secret ? `<button class="admin-icon-btn" data-copy-value="${escapeAttribute(secret)}" title="复制密钥" aria-label="复制密钥">${icon('copy')}</button>` : ''}
    <button class="admin-icon-btn" data-edit-entity="${key}" data-id="${escapeAttribute(id)}" title="编辑" aria-label="编辑">${icon('edit')}</button>
    <button class="admin-icon-btn is-danger" data-delete-entity="${key}" data-id="${escapeAttribute(id)}" title="删除" aria-label="删除">${icon('trash')}</button>
  `
}

function currentAdminKey(): AdminKey {
  const path = window.location.pathname
  const segment = path.replace(/^\/dashboard\/?/, '').split('/')[0] || 'dashboard'
  if (segment === 'alert-rule') return 'rule'
  if (navItems.some((item) => item.key === segment)) return segment as AdminKey
  return 'dashboard'
}

function setActiveNav(key: AdminKey) {
  app.querySelectorAll<HTMLElement>('[data-admin-nav]').forEach((link) => {
    link.classList.toggle('active', link.dataset.adminNav === key)
  })
}

function findEntityRow(key: AdminKey, id: string) {
  return (entityRows.get(key) || []).find((row) => value(row, 'ID') === id)
}

function handlePageError(error: unknown) {
  const message = errorMessage(error)
  if (message.includes('登录') || message.includes('403')) {
    contentArea.innerHTML = `
      <section class="admin-panel admin-auth-panel">
        <h2>需要授权</h2>
        <p>请使用白名单账号授权登录。</p>
        <a class="admin-button is-primary" href="/login">${icon('login', 'admin-button-svg')}<span>去登录</span></a>
      </section>
    `
    return
  }
  contentArea.innerHTML = emptyPanel(message)
}

function statCard(label: string, valueText: string, subtext: string, iconName: string, href: string) {
  return `
    <a class="admin-stat-card" href="${escapeAttribute(href)}" aria-label="打开${escapeAttribute(label)}">
      <div class="admin-stat-icon" data-stat-icon="${escapeAttribute(iconName)}">${icon(iconName)}</div>
      <span>${escapeHtml(label)}</span>
      <strong>${escapeHtml(valueText)}</strong>
      <small>${escapeHtml(subtext)}</small>
    </a>
  `
}

function statusItem(label: string, valueText: string, subtext: string, iconName: string, href: string) {
  return `
    <a class="admin-status-item" href="${escapeAttribute(href)}" aria-label="打开${escapeAttribute(label)}">
      <span class="admin-status-icon" data-stat-icon="${escapeAttribute(iconName)}">${icon(iconName)}</span>
      <span>
        <small>${escapeHtml(label)}</small>
        <strong>${escapeHtml(valueText)}</strong>
        <em>${escapeHtml(subtext)}</em>
      </span>
    </a>
  `
}

function statusMeter(label: string, valueText: string, percent: number, subtext: string, href: string) {
  const safePercent = Math.max(0, Math.min(100, percent))
  return `
    <a class="admin-status-item is-meter" href="${escapeAttribute(href)}" aria-label="打开${escapeAttribute(label)}">
      <span>
        <small>${escapeHtml(label)}</small>
        <strong>${escapeHtml(valueText)}</strong>
        <em>${escapeHtml(subtext)}</em>
      </span>
      <span class="admin-status-meter"><i style="width:${safePercent}%"></i></span>
    </a>
  `
}

function currentTimeLabel() {
  return new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function loadingPanel(message: string) {
  return `<section class="admin-panel admin-loading"><span>${escapeHtml(message)}</span></section>`
}

function emptyPanel(message: string) {
  return `<section class="admin-panel admin-empty">${escapeHtml(message)}</section>`
}

function badge(text: string) {
  return `<span class="admin-badge">${escapeHtml(text)}</span>`
}

function monitorType(input: unknown) {
  const number = Number(input)
  if (number === 2 || number === 3) return badge('TCP')
  return badge('ICMP')
}

function ddnsProvider(input: unknown) {
  const providers: Record<string, string> = {
    '0': 'dummy',
    '1': 'webhook',
    '2': 'cloudflare',
    '3': 'tencentcloud',
  }
  return badge(providers[String(input)] || String(input || '-'))
}

function countryCode(row: Row) {
  return cssSafeId(value(row, 'Host.CountryCode').toLowerCase())
}

function arrayPayload(payload: unknown): Row[] {
  return Array.isArray(payload) ? payload.filter(isRow) : []
}

function arrayFromObject(payload: unknown, key: string): Row[] {
  const source = objectFrom(payload)
  const list = source[key]
  return Array.isArray(list) ? list.filter(isRow) : []
}

function objectFrom(payload: unknown): Row {
  return isRow(payload) ? payload : {}
}

function countValue(row: unknown, path: string, fallback = 0) {
  const number = Number(get(row, path))
  return Number.isFinite(number) && number >= 0 ? Math.floor(number) : fallback
}

function isRow(input: unknown): input is Row {
  return Boolean(input) && typeof input === 'object' && !Array.isArray(input)
}

function get(row: unknown, path: string): unknown {
  if (!isRow(row)) return undefined
  let current: unknown = row
  for (const part of path.split('.')) {
    if (!isRow(current)) return undefined
    current = current[part]
  }
  return current
}

function value(row: unknown, path: string) {
  const input = get(row, path)
  if (input === undefined || input === null) return ''
  if (typeof input === 'string' || typeof input === 'number' || typeof input === 'boolean') return String(input)
  return JSON.stringify(input)
}

function toBool(input: unknown) {
  if (typeof input === 'boolean') return input
  if (typeof input === 'number') return input > 0
  const text = String(input || '').toLowerCase()
  return text === 'true' || text === '1' || text === 'on'
}

function formatDate(input: unknown) {
  const date = new Date(String(input || ''))
  if (!Number.isFinite(date.getTime()) || date.getFullYear() < 2000) return '-'
  const pad = (number: number) => String(number).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function truncate(input: string, max: number) {
  return input.length > max ? `${input.slice(0, max - 1)}...` : input
}

function cssSafeId(input: string) {
  return input.replace(/[^a-z0-9-]/g, '')
}

function cssEscape(input: string) {
  return window.CSS?.escape ? window.CSS.escape(input) : input.replace(/["\\]/g, '\\$&')
}

function requiredElement(id: string) {
  const element = document.getElementById(id)
  if (!element) throw new Error(`Missing #${id}`)
  return element
}

function requiredDialog(id: string) {
  const element = document.getElementById(id)
  if (!(element instanceof HTMLDialogElement)) throw new Error(`Missing dialog #${id}`)
  return element
}

async function copyText(text: string) {
  if (!text) return
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
    } else {
      fallbackCopyText(text)
    }
    toast('已复制')
  } catch (error) {
    toast(errorMessage(error), true)
  }
}

function fallbackCopyText(text: string) {
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  document.body.append(textarea)
  textarea.select()
  const ok = document.execCommand('copy')
  textarea.remove()
  if (!ok) throw new Error('复制失败')
}

function toast(message: string, danger = false) {
  const element = document.getElementById('admin-toast')
  if (!element) return
  element.textContent = message
  element.classList.toggle('is-danger', danger)
  element.hidden = false
  if (toastTimer) window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => {
    element.hidden = true
  }, 2400)
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error || '请求失败')
}

function plainTextFromHtml(input: string) {
  return input
    .replace(/<br\s*\/?>/gi, '；')
    .replace(/<[^>]+>/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

function escapeHtml(input: unknown) {
  return String(input ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function escapeAttribute(input: unknown) {
  return escapeHtml(input)
}
