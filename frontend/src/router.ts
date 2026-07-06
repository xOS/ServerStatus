export interface Route {
  path: string | RegExp
  render: (appContainer: HTMLElement) => void | (() => void) | Promise<void | (() => void)>
  cleanup?: () => void
}

let currentCleanup: (() => void) | undefined
let currentPath = ''
let navigationToken = 0

export class Router {
  private routes: Route[] = []
  private container: HTMLElement

  constructor(containerId: string) {
    const el = document.getElementById(containerId)
    if (!el) throw new Error(`Container #${containerId} not found`)
    this.container = el

    window.addEventListener('popstate', () => this.navigate(window.location.pathname, false))
  }

  addRoute(route: Route) {
    this.routes.push(route)
  }

  async navigate(path: string, pushState = true) {
    if (path === currentPath && !pushState) return
    if (path === currentPath && pushState) return

    const route = this.routes.find(r => {
      if (typeof r.path === 'string') return r.path === path
      return r.path.test(path)
    })

    if (!route) {
      console.warn(`No route found for ${path}`)
      return
    }

    const token = ++navigationToken
    currentPath = path

    if (pushState) {
      window.history.pushState(null, '', path)
    }

    if (currentCleanup) {
      currentCleanup()
      currentCleanup = undefined
    }

    this.container.innerHTML = ''
    
    currentCleanup = route.cleanup
    const cleanup = await route.render(this.container)
    if (token !== navigationToken) {
      if (typeof cleanup === 'function') cleanup()
      return
    }
    if (typeof cleanup === 'function') {
      currentCleanup = cleanup
    }
  }
}

export const navigateTo = (path: string) => {
  window.dispatchEvent(new CustomEvent('app-navigate', { detail: path }))
}
