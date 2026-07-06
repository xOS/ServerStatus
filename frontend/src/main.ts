import './style.css'
import { Router } from './router'
import { initHome } from './home'
import { initAdmin, initLogin } from './admin'
import { initNetwork } from './views/network'

const appContainer = document.querySelector<HTMLDivElement>('#app')
if (!appContainer) throw new Error('Missing #app root')

const router = new Router('app')

router.addRoute({
  path: /^\/dashboard/,
  render: (container) => {
    return initAdmin(container as HTMLDivElement)
  }
})

router.addRoute({
  path: '/login',
  render: (container) => {
    return initLogin(container as HTMLDivElement)
  }
})

router.addRoute({
  path: /^\/network/,
  render: (container) => {
    return initNetwork(container as HTMLDivElement)
  }
})

router.addRoute({
  path: /.*/,
  render: (container) => {
    return initHome(container as HTMLDivElement)
  }
})

// Listen for custom navigation events
window.addEventListener('app-navigate', ((e: CustomEvent<string>) => {
  router.navigate(e.detail)
}) as EventListener)

// Handle link clicks globally
document.body.addEventListener('click', (e) => {
  const link = (e.target as HTMLElement).closest('a')
  if (link && link.href && link.origin === window.location.origin && !link.hasAttribute('target') && !link.hasAttribute('data-native-link')) {
    e.preventDefault()
    window.dispatchEvent(new CustomEvent('app-navigate', { detail: link.pathname }))
  }
})

// Initial load
router.navigate(window.location.pathname, false)
