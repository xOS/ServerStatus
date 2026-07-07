import { ProfileResponse } from './home'
import { authApiPath } from './api'

let _app: HTMLDivElement
let adminArea: HTMLElement
let footerContent: HTMLElement

// Reusable escape helpers
const escapeHtml = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
const escapeAttribute = (s: string) => s.replace(/"/g, '&quot;')

export const icon = (name: string, cls: string = '') => {
  const c = `class="svg-icon${cls ? ` ${cls}` : ''}"`
  const base = `${c} viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"`
  switch (name) {
    case 'circleDot':
      return `<svg ${base}><circle cx="12" cy="12" r="8.2"/><circle class="svg-accent" cx="12" cy="12" r="2.35" fill="currentColor" stroke="none"/><path d="M12 3.2v2.1M12 18.7v2.1M3.2 12h2.1M18.7 12h2.1"/><path class="svg-soft" d="M7.35 7.35 5.85 5.85M16.65 7.35l1.5-1.5M7.35 16.65l-1.5 1.5M16.65 16.65l1.5 1.5"/></svg>`

    case 'server':
      return `<svg ${base}><rect x="3.4" y="4.3" width="17.2" height="6.2" rx="1.8"/><rect x="3.4" y="13.5" width="17.2" height="6.2" rx="1.8"/><path class="svg-soft" d="M9.6 7.4h7.2M9.6 16.6h7.2"/><circle class="svg-accent" cx="6.6" cy="7.4" r="1" fill="currentColor" stroke="none"/><circle class="svg-accent" cx="6.6" cy="16.6" r="1" fill="currentColor" stroke="none"/></svg>`

    case 'login':
      return `<svg ${base}><path d="M14.4 4.4h3.8a2.4 2.4 0 0 1 2.4 2.4v10.4a2.4 2.4 0 0 1-2.4 2.4h-3.8"/><path class="svg-accent" d="M9.7 7.7 14 12l-4.3 4.3"/><path class="svg-accent" d="M13.7 12H3.8"/></svg>`

    case 'chevronDown':
      return `<svg ${base}><path d="m6.7 9.2 5.3 5.3 5.3-5.3"/></svg>`

    case 'cancel':
      return `<svg ${base}><circle cx="12" cy="12" r="8.4"/><path class="svg-accent" d="m8.8 8.8 6.4 6.4M15.2 8.8l-6.4 6.4"/></svg>`

    case 'grid':
      return `<svg ${base}><rect x="3.5" y="3.5" width="6.2" height="6.2" rx="1.4"/><rect x="14.3" y="3.5" width="6.2" height="6.2" rx="1.4"/><rect x="14.3" y="14.3" width="6.2" height="6.2" rx="1.4"/><rect x="3.5" y="14.3" width="6.2" height="6.2" rx="1.4"/></svg>`

    case 'dashboard':
      return `<svg ${base}><path d="M4.2 13.4a7.8 7.8 0 1 1 15.6 0"/><path d="M4.2 13.4v4.2a2.2 2.2 0 0 0 2.2 2.2h11.2a2.2 2.2 0 0 0 2.2-2.2v-4.2"/><path class="svg-accent" d="m12 13.2 3.4-4.4"/><path class="svg-soft" d="M7.2 13.2h1.2M15.6 13.2h1.2M12 6.7v1.2"/></svg>`

    case 'monitor':
      return `<svg ${base}><path d="M3.8 12.7h3.4l2.05-5.35 3.6 9.3 2.2-5.2h5.15"/><path class="svg-soft" d="M4.4 19.2h15.2M4.4 4.8h15.2"/><circle class="svg-accent" cx="17.1" cy="11.45" r="1.1" fill="currentColor" stroke="none"/></svg>`

    case 'calendar':
      return `<svg ${base}><rect x="4" y="5.2" width="16" height="15" rx="2.2"/><path d="M8 3.4v3.6M16 3.4v3.6M4 9.1h16"/><path class="svg-accent" d="M8.1 12.2h.1M12 12.2h.1M15.9 12.2h.1M8.1 15.8h.1M12 15.8h.1" stroke-width="2.6"/></svg>`

    case 'alert':
      return `<svg ${base}><path d="M12 3.6 21 19.2H3L12 3.6Z"/><path class="svg-accent" d="M12 9.2v4.5"/><circle class="svg-accent" cx="12" cy="16.7" r=".9" fill="currentColor" stroke="none"/></svg>`

    case 'bell':
      return `<svg ${base}><path d="M18.2 10.7c0-3.25-1.8-5.35-4.4-5.95a1.85 1.85 0 0 0-3.6 0c-2.6.6-4.4 2.7-4.4 5.95v3.2l-1.7 2.6h15.8l-1.7-2.6v-3.2Z"/><path class="svg-accent" d="M9.5 19.2a2.7 2.7 0 0 0 5 0"/></svg>`

    case 'nat':
      return `<svg ${base}><rect x="3.7" y="4.2" width="6.2" height="5.2" rx="1.4"/><rect x="14.1" y="4.2" width="6.2" height="5.2" rx="1.4"/><rect x="8.9" y="14.6" width="6.2" height="5.2" rx="1.4"/><path d="M6.8 9.4v2.1c0 1.2.9 2.1 2.1 2.1H12"/><path d="M17.2 9.4v2.1c0 1.2-.9 2.1-2.1 2.1H12"/><path class="svg-accent" d="M12 13.6v1"/></svg>`

    case 'ddns':
      return `<svg ${base}><path d="M7.1 17.5h9.9a3.65 3.65 0 0 0 .3-7.3 5.15 5.15 0 0 0-9.8-1.5 4.45 4.45 0 0 0-.4 8.8Z"/><path class="svg-accent" d="M9.2 12.7h5.6"/><path class="svg-accent" d="m12.5 10.4 2.3 2.3-2.3 2.3"/></svg>`

    case 'key':
      return `<svg ${base}><circle cx="8.1" cy="14.9" r="3.2"/><path class="svg-accent" d="M10.4 12.6 20.2 2.8"/><path d="M15.4 7.6 17.6 9.8M13.2 9.8 15 11.6"/></svg>`

    case 'settings':
      return `<svg ${base}><path d="M12 8.4a3.6 3.6 0 1 1 0 7.2 3.6 3.6 0 0 1 0-7.2Z"/><path d="M19.1 13.2a7.7 7.7 0 0 0 0-2.4l2-1.55-2-3.45-2.5 1a7.2 7.2 0 0 0-2.1-1.2L14.1 3h-4.2l-.4 2.6c-.75.28-1.45.68-2.1 1.2l-2.5-1-2 3.45 2 1.55a7.7 7.7 0 0 0 0 2.4l-2 1.55 2 3.45 2.5-1c.65.52 1.35.92 2.1 1.2l.4 2.6h4.2l.4-2.6c.75-.28 1.45-.68 2.1-1.2l2.5 1 2-3.45-2-1.55Z"/></svg>`

    case 'edit':
      return `<svg ${base}><path d="M4.2 19.8h15.6"/><path d="M5.7 14.9 15.8 4.8a2 2 0 0 1 2.8 2.8L8.5 17.7l-4.1 1.1 1.3-3.9Z"/><path class="svg-soft" d="m14.2 6.4 3.4 3.4"/></svg>`

    case 'trash':
      return `<svg ${base}><path d="M4.2 6.7h15.6"/><path d="M9.2 6.7V4.5h5.6v2.2"/><path d="M6.4 6.7 7.3 20h9.4l.9-13.3"/><path class="svg-accent" d="M10 10.3v5.9M14 10.3v5.9"/></svg>`

    case 'copy':
      return `<svg ${base}><rect x="8.2" y="8.2" width="11.4" height="11.4" rx="2"/><path d="M5.8 15.8H5.1a1.9 1.9 0 0 1-1.9-1.9V5.1a1.9 1.9 0 0 1 1.9-1.9h8.8a1.9 1.9 0 0 1 1.9 1.9v.7"/><path class="svg-soft" d="M11.2 12.1h5.4M11.2 15.5h3.5"/></svg>`

    case 'checkSquare':
      return `<svg ${base}><rect x="4" y="4" width="16" height="16" rx="3"/><path class="svg-accent" d="m8.2 12.2 2.5 2.5 5.3-5.4"/><path class="svg-soft" d="M7.2 7.3h5.8"/></svg>`

    case 'group':
      return `<svg ${base}><rect x="4" y="5" width="16" height="5.5" rx="1.7"/><rect x="4" y="13.5" width="16" height="5.5" rx="1.7"/><path class="svg-accent" d="M7.1 7.8h5.9M7.1 16.3h5.9"/><circle class="svg-accent" cx="17" cy="7.75" r=".85" fill="currentColor" stroke="none"/><circle class="svg-accent" cx="17" cy="16.25" r=".85" fill="currentColor" stroke="none"/></svg>`

    case 'play':
      return `<svg ${base}><circle cx="12" cy="12" r="8.7"/><path class="svg-accent" d="M10.1 8.4 15.8 12l-5.7 3.6V8.4Z" fill="currentColor" stroke="none"/></svg>`

    case 'home':
      return `<svg ${base}><path d="M3.8 11.4 12 4.2l8.2 7.2"/><path d="M6.2 10.2v9.2h11.6v-9.2"/><path class="svg-accent" d="M10 19.4v-5.2h4v5.2"/></svg>`

    case 'shield':
      return `<svg ${base}><path d="M12 3.3 19.4 6v5.4c0 4.65-3.05 7.45-7.4 9.3-4.35-1.85-7.4-4.65-7.4-9.3V6L12 3.3Z"/><path class="svg-accent" d="m8.7 12.1 2.1 2.1 4.6-4.7"/></svg>`

    case 'logout':
      return `<svg ${base}><path d="M9.2 20.2H5.8a2.2 2.2 0 0 1-2.2-2.2V6a2.2 2.2 0 0 1 2.2-2.2h3.4"/><path class="svg-accent" d="m15.6 16.2 4.2-4.2-4.2-4.2"/><path class="svg-accent" d="M19.5 12H8.8"/></svg>`

    case 'info':
      return `<svg ${base}><circle cx="12" cy="12" r="8.7"/><circle class="svg-accent" cx="12" cy="7.8" r="1" fill="currentColor" stroke="none"/><path class="svg-accent" d="M11.35 11h1.05v5.15"/><path class="svg-accent" d="M10.65 16.15h2.85"/></svg>`

    case 'cpu':
      return `<svg ${base}><rect x="7.1" y="7.1" width="9.8" height="9.8" rx="2"/><rect class="svg-accent" x="10.1" y="10.1" width="3.8" height="3.8" rx=".8" fill="currentColor" stroke="none"/><path d="M9 3.2v2.2M15 3.2v2.2M9 18.6v2.2M15 18.6v2.2M3.2 9h2.2M3.2 15h2.2M18.6 9h2.2M18.6 15h2.2"/></svg>`

    case 'memory':
    case 'ram':
      return `<svg ${base}><rect x="5.2" y="4.4" width="13.6" height="15.2" rx="2"/><path class="svg-soft" d="M8.5 8.2h7M8.5 12h7M8.5 15.8h7"/><path d="M3.4 8h1.8M3.4 16h1.8M18.8 8h1.8M18.8 16h1.8"/></svg>`

    case 'swap':
      return `<svg ${base}><ellipse cx="12" cy="6.8" rx="7.8" ry="2.8"/><path d="M4.2 6.8v10.4c0 1.55 3.5 2.8 7.8 2.8s7.8-1.25 7.8-2.8V6.8"/><path class="svg-soft" d="M4.2 12c0 1.55 3.5 2.8 7.8 2.8s7.8-1.25 7.8-2.8"/></svg>`

    case 'disk':
      return `<svg ${base}><rect x="4.5" y="3.7" width="15" height="16.6" rx="2.3"/><path class="svg-soft" d="M7.6 7.7h8.8M7.6 11.8h8.8M7.6 16.1h5.6"/><circle class="svg-accent" cx="16.4" cy="16.1" r=".95" fill="currentColor" stroke="none"/></svg>`

    case 'up':
      return `<svg ${base}><path class="svg-accent" d="M12 18.3V5.7"/><path class="svg-accent" d="m7.9 9.8 4.1-4.1 4.1 4.1"/><path class="svg-soft" d="M5 18.4h3.4M15.6 18.4H19M6.4 14.7h2.2M15.4 14.7h2.2"/></svg>`

    case 'down':
      return `<svg ${base}><path class="svg-accent" d="M12 5.7v12.6"/><path class="svg-accent" d="m7.9 14.2 4.1 4.1 4.1-4.1"/><path class="svg-soft" d="M5 5.6h3.4M15.6 5.6H19M6.4 9.3h2.2M15.4 9.3h2.2"/></svg>`

    case 'upSolid':
      return `<svg ${base}><path class="svg-accent" d="M12 15.2V4.9"/><path class="svg-accent" d="m8.7 8.2 3.3-3.3 3.3 3.3"/><path d="M5.1 15.2v2.15a2 2 0 0 0 2 2h9.8a2 2 0 0 0 2-2V15.2"/><path class="svg-soft" d="M8.3 15.2h7.4"/></svg>`

    case 'downSolid':
      return `<svg ${base}><path class="svg-accent" d="M12 4.9v10.3"/><path class="svg-accent" d="m8.7 11.9 3.3 3.3 3.3-3.3"/><path d="M5.1 15.2v2.15a2 2 0 0 0 2 2h9.8a2 2 0 0 0 2-2V15.2"/><path class="svg-soft" d="M8.3 15.2h7.4"/></svg>`

    case 'transfer':
      return `<svg ${base}><path d="M4.2 16.8 10 11M4.2 16.8l4 .9M4.2 16.8l.9-4"/><path d="M19.8 7.2 14 13M19.8 7.2l-4-.9M19.8 7.2l-.9 4"/><circle class="svg-accent" cx="15.8" cy="16.8" r="2" fill="currentColor" stroke="none"/><circle class="svg-accent" cx="8.2" cy="7.2" r="2" fill="currentColor" stroke="none"/></svg>`

    case 'activity':
      return `<svg ${base}><path d="M3 19.3h18"/><rect x="5" y="10.4" width="3.3" height="6.4" rx="1"/><rect class="svg-accent" x="10.35" y="5" width="3.3" height="11.8" rx="1" fill="currentColor" stroke="none"/><rect x="15.7" y="12.6" width="3.3" height="4.2" rx="1"/></svg>`

    case 'refresh':
      return `<svg ${base}><path d="M20.1 7.9a8.1 8.1 0 0 0-14.25-1.7"/><path class="svg-accent" d="M5.6 3.6v3.15h3.15"/><path d="M3.9 16.1a8.1 8.1 0 0 0 14.25 1.7"/><path class="svg-accent" d="M18.4 20.4v-3.15h-3.15"/></svg>`

    case 'clock':
      return `<svg ${base}><path d="M12 3.7a8.3 8.3 0 1 1-8.3 8.3"/><path class="svg-soft" d="M3.7 7.2V3.7h3.5"/><path class="svg-accent" d="M12 7.4v4.9l3.1 1.9"/></svg>`

    case 'tcp':
      return `<svg ${base}><path d="M5.2 8.6h5.5v6.8H5.2z"/><path d="M13.3 8.6h5.5v6.8h-5.5z"/><path class="svg-accent" d="M10.7 12h2.6"/><path class="svg-soft" d="M7.3 17.8h9.4"/></svg>`

    case 'udp':
      return `<svg ${base}><path d="M5 11.2a10 10 0 0 1 14 0"/><path d="M8.4 14.7a5.1 5.1 0 0 1 7.2 0"/><circle class="svg-accent" cx="12" cy="18.4" r="1.55" fill="currentColor" stroke="none"/></svg>`
      
    case 'windows':
      return `<svg ${c} viewBox="0 0 24 24" aria-hidden="true" fill="currentColor"><path d="M3.4 5.6 10.7 4.6v6.55H3.4V5.6Z"/><path d="M12.2 4.38 20.6 3.2v7.95h-8.4V4.38Z"/><path d="M3.4 12.85h7.3v6.55l-7.3-1.02v-5.53Z"/><path d="M12.2 12.85h8.4v7.95l-8.4-1.18v-6.77Z"/></svg>`

    case 'linuxScript':
      return `<svg ${base}><rect x="3.8" y="4.2" width="16.4" height="15.6" rx="2.4"/><path class="svg-soft" d="M7.1 8.2h9.8M7.1 16h9.8"/><path class="svg-accent" d="m8.1 10.5 2 1.5-2 1.5"/><path class="svg-accent" d="M12 13.6h4.1"/><circle class="svg-accent" cx="16.8" cy="8.2" r=".9" fill="currentColor" stroke="none"/></svg>`

    case 'windowsScript':
      return `<svg ${base}><path d="M4 5.6 10.7 4.7v6.15H4V5.6Z"/><path d="M12.1 4.5 20 3.4v7.45h-7.9V4.5Z"/><path d="M4 12.55h6.7v6.15L4 17.76v-5.21Z"/><path d="M12.1 12.55H20V20l-7.9-1.1v-6.35Z"/><path class="svg-accent" d="M6.8 15.15h3M14 15.15h3.4"/></svg>`

    case 'chinaSource':
      return `<svg ${base}><path d="M5.1 4.3v15.4"/><path d="M5.1 5.2h13.4l-1.7 4.05 1.7 4.05H5.1V5.2Z"/><path class="svg-accent" d="m9.3 7.45.56 1.12 1.24.18-.9.88.21 1.24-1.11-.58-1.1.58.21-1.24-.9-.88 1.24-.18.55-1.12Z" fill="currentColor" stroke="none"/><path class="svg-soft" d="M12.8 8.1h2.35M12.8 10.7h1.9"/></svg>`
      
    case 'apple':
      return `<svg ${c} viewBox="0 0 24 24" aria-hidden="true" fill="currentColor"><path d="M16.5 12.2c-.02-2.15 1.75-3.18 1.83-3.23-1-1.47-2.56-1.67-3.12-1.69-1.33-.13-2.6.78-3.27.78-.68 0-1.72-.76-2.83-.74-1.46.02-2.8.85-3.55 2.16-1.51 2.62-.39 6.5 1.09 8.62.72 1.04 1.58 2.21 2.71 2.17 1.09-.04 1.5-.7 2.81-.7 1.31 0 1.68.7 2.83.68 1.17-.02 1.91-1.06 2.62-2.1.83-1.21 1.17-2.39 1.19-2.45-.03-.01-2.28-.87-2.31-3.5ZM14.34 5.88c.59-.71.99-1.7.88-2.68-.85.03-1.88.57-2.49 1.28-.55.64-1.03 1.64-.9 2.61.94.07 1.91-.48 2.51-1.21Z"/></svg>`
      
    case 'tux':
      return `<svg ${base}><path d="M7.4 17.8c.65-1.1 1.45-2.1 2.15-3.35.45-.8.58-1.9.58-2.92"/><path d="M16.6 17.8c-.65-1.1-1.45-2.1-2.15-3.35-.45-.8-.58-1.9-.58-2.92"/><ellipse cx="12" cy="8.1" rx="4.15" ry="5.35"/><path class="svg-accent" d="M9.2 18.6c.7 1.05 1.65 1.7 2.8 1.7s2.1-.65 2.8-1.7"/><circle class="svg-accent" cx="10.55" cy="7.35" r=".55" fill="currentColor" stroke="none"/><circle class="svg-accent" cx="13.45" cy="7.35" r=".55" fill="currentColor" stroke="none"/></svg>`

    default:
      return ''
  }
}


export function injectAppShell(container: HTMLDivElement, activeRoute: 'home' | 'network'): { contentArea: HTMLElement } {
  _app = container
  _app.innerHTML = `
    <div class="app-shell">
      <nav class="top-nav" aria-label="Primary">
        <div class="top-nav-inner">
          <div class="nav-left">
            <a class="nav-logo" href="/" aria-label="Server Status">
              <img src="/static/logo.svg?v20220602" alt="">
            </a>
            <a class="nav-link ${activeRoute === 'home' ? 'active' : ''}" href="/" data-route="home">${icon('server', 'nav-svg')}<span>服务器</span></a>
            <a class="nav-link ${activeRoute === 'network' ? 'active' : ''}" href="/network" data-route="network">${icon('circleDot', 'nav-svg')}<span>网络</span></a>
          </div>
          <div class="nav-right" id="admin-area"></div>
        </div>
      </nav>

      <main class="page-main">
        <div class="nb-container">
          <div class="nb-inner" id="page-content">
            <!-- Content injected here -->
          </div>
        </div>
      </main>

      <footer class="bottom-footer">
        <div class="bottom-footer-inner" id="footer-content"></div>
      </footer>
    </div>
  `

  adminArea = _app.querySelector('#admin-area')!
  footerContent = _app.querySelector('#footer-content')!
  
  return {
    contentArea: _app.querySelector('#page-content')!
  }
}

export function renderChrome(profile: ProfileResponse | null | any) {
  const admin = profile?.Admin
  const brandText = String(profile?.Conf?.Site?.Brand || '服务器')
  const brand = escapeHtml(brandText)

  document.title = brandText

  if (admin) {
    const name = escapeHtml(shortAdminName(admin.Name || admin.Login || '管理员'))
    const avatar = escapeAttribute(admin.AvatarURL || '/static/logo.svg?v20220602')
    adminArea.innerHTML = `
      <div class="admin-menu">
        <button class="admin-trigger" type="button">
          <img src="${avatar}" alt="">
          <span>${name}</span>
          <span class="admin-caret" aria-hidden="true"></span>
        </button>
        <div class="admin-dropdown">
          <a href="/dashboard">${icon('grid', 'dropdown-svg')}<span>管理</span></a>
          <a class="is-logout" href="${authApiPath('/logout')}" data-native-link>${icon('logout', 'dropdown-svg')}<span>退出</span></a>
        </div>
      </div>
    `
  } else {
    adminArea.innerHTML = `<a href="/login" class="login-button">${icon('login', 'login-svg')}<span>登录</span></a>`
  }

  footerContent.innerHTML = `
    <b>&copy; 2026 <a href="/">${brand}</a></b>
    <span class="footer-separator">|</span>
    <a href="http://www.nange.cn" target="_blank" rel="noreferrer">春夏</a>
    <span class="custom-code">${profile?.CustomCode || profile?.Conf?.Site?.CustomCode || ''}</span>
  `
}

function shortAdminName(input: unknown) {
  const chars = Array.from(String(input || '管理员').trim())
  return chars.slice(0, 2).join('') || '管理'
}
