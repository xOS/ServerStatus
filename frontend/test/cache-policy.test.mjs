import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const readProjectFile = (path) => readFile(new URL(`../${path}`, import.meta.url), 'utf8')

test('Vercel rewrites only application routes', async () => {
  const config = JSON.parse(await readProjectFile('vercel.json'))
  const sources = config.rewrites.map((rule) => rule.source)

  assert.equal(sources.includes('/(.*)'), false)
  assert.equal(sources.some((source) => source.startsWith('/assets')), false)
  assert.equal(sources.some((source) => source.startsWith('/static')), false)
  assert.deepEqual(sources, [
    '/login',
    '/dashboard/:path*',
    '/network/:path*',
    '/_asset-recovery/:nonce',
  ])
})

test('Vercel keeps documents fresh and hashed assets immutable', async () => {
  const config = JSON.parse(await readProjectFile('vercel.json'))
  const headers = new Map(config.headers.map((rule) => [
    rule.source,
    new Map(rule.headers.map((header) => [header.key, header.value])),
  ]))

  assert.match(headers.get('/assets/:path*').get('Cache-Control'), /immutable/)
  for (const source of ['/', '/index.html', '/login', '/dashboard/:path*', '/network/:path*', '/_asset-recovery/:nonce']) {
    assert.match(headers.get(source).get('Cache-Control'), /no-store/)
    assert.equal(headers.get(source).get('CDN-Cache-Control'), 'no-store')
  }
})

test('asset recovery uses a unique path instead of a cache-ignored query', async () => {
  const html = await readProjectFile('index.html')

  assert.match(html, /recoveryPrefix = '\/_asset-recovery\/'/)
  assert.match(html, /serverstatus:app-ready/)
  assert.doesNotMatch(html, /searchParams\.set\('_reload'/)
})

test('Cloudflare Pages does not apply a catch-all rewrite to missing assets', async () => {
  const redirects = await readProjectFile('public-lite/_redirects')

  assert.doesNotMatch(redirects, /^\/\*\s/m)
  assert.match(redirects, /^\/dashboard\/\*/m)
  assert.match(redirects, /^\/_asset-recovery\/\*/m)
})
