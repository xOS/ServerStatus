export function createOptionalAbortController() {
  if (typeof AbortController === 'undefined') return undefined
  return new AbortController()
}

export function fetchSignalInit(signal: AbortSignal | undefined) {
  return signal ? { signal } : {}
}

export function addAbortableEvent(
  target: EventTarget,
  type: string,
  listener: EventListenerOrEventListenerObject,
  signal?: AbortSignal,
  options: AddEventListenerOptions = {},
) {
  const removeOptions: EventListenerOptions = { capture: options.capture }

  if (signal) {
    try {
      const abortableOptions = { ...options, signal }
      target.addEventListener(type, listener, abortableOptions)
      return () => target.removeEventListener(type, listener, removeOptions)
    } catch {
      // Older Safari may have AbortController but not support signal in addEventListener options.
    }
  }

  target.addEventListener(type, listener, options)
  const remove = () => target.removeEventListener(type, listener, removeOptions)
  if (signal) signal.addEventListener('abort', remove, { once: true })
  return remove
}
