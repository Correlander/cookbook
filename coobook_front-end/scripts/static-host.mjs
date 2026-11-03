import { createServer } from 'node:http'
import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { readFile, stat } from 'node:fs/promises'
import { extname, resolve, sep } from 'node:path'

const dist = resolve(process.env.STATIC_DIST ?? 'dist')
const apiOrigin = process.env.API_ORIGIN
const port = Number(process.env.STATIC_PORT ?? 4178)
if (!apiOrigin || !Number.isInteger(port)) throw new Error('API_ORIGIN and STATIC_PORT are required')
const mime = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.png': 'image/png', '.webp': 'image/webp', '.ico': 'image/x-icon', '.json': 'application/json; charset=utf-8', '.woff2': 'font/woff2' }
const pageAliases = new Map([
  ['', { file: 'index.html', route: '/' }],
  ['index.html', { file: 'index.html', route: '/' }],
  ['auth/confirm', { file: 'auth/confirm.html', route: '/auth/confirm' }],
  ['auth/confirm.html', { file: 'auth/confirm.html', route: '/auth/confirm' }],
  ['auth/enroll', { file: 'auth/enroll.html', route: '/auth/enroll' }],
  ['auth/enroll.html', { file: 'auth/enroll.html', route: '/auth/enroll' }],
  ['account/security', { file: 'account/security.html', route: '/account/security' }],
  ['account/security.html', { file: 'account/security.html', route: '/account/security' }],
  ['admin/settings', { file: 'admin/settings.html', route: '/admin/settings' }],
  ['admin/settings.html', { file: 'admin/settings.html', route: '/admin/settings' }],
  ['library', { file: 'library.html', route: '/library' }],
  ['library.html', { file: 'library.html', route: '/library' }],
  ['recipe', { file: 'recipe.html', route: '/recipe' }],
  ['recipe.html', { file: 'recipe.html', route: '/recipe' }],
])

const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url, `http://${request.headers.host ?? 'localhost'}`)
    if (url.pathname === '/healthz' || url.pathname.startsWith('/api/')) {
      const upstream = new URL(url.pathname + url.search, apiOrigin)
      const headers = { ...request.headers, host: request.headers.host }
      const method = request.method ?? 'GET'
      const upstreamRequest = (upstream.protocol === 'https:' ? httpsRequest : httpRequest)(upstream, { method, headers }, (upstreamResponse) => {
        const responseHeaders = Object.fromEntries(Object.entries(upstreamResponse.headers).filter(([name]) => !['connection', 'transfer-encoding'].includes(name)))
        response.writeHead(upstreamResponse.statusCode ?? 502, responseHeaders)
        upstreamResponse.pipe(response)
      })
      upstreamRequest.on('error', () => { if (!response.headersSent) response.writeHead(502, { 'Content-Type': 'text/plain; charset=utf-8' }); response.end('API upstream unavailable') })
      request.pipe(upstreamRequest)
      return
    }
    let decodedPath
    try {
      decodedPath = decodeURIComponent(url.pathname)
    } catch {
      response.writeHead(400, { 'Content-Type': 'text/plain; charset=utf-8' }).end('bad path')
      return
    }
    const relative = decodedPath.replace(/^\/+/, '')
    const page = pageAliases.get(relative)
    const target = page?.file ?? relative
    if (target.includes('\\') || target.includes('\0')) {
      response.writeHead(400).end('bad path'); return
    }
    let filename = resolve(dist, target)
    if (!filename.startsWith(dist + sep)) {
      response.writeHead(400).end('bad path'); return
    }
    if ((await stat(filename).catch(() => null))?.isDirectory()) {
      const indexFile = resolve(filename, 'index.html')
      if (!indexFile.startsWith(dist + sep)) {
        response.writeHead(400).end('bad path'); return
      }
      filename = indexFile
    }
    if (extname(filename) === '.html' && !page) {
      response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }).end('not found')
      return
    }
    const data = await readFile(filename)
    const type = mime[extname(filename)] ?? 'application/octet-stream'
    response.setHeader('Content-Type', type)
    if (extname(filename) === '.html') response.setHeader('Cache-Control', 'no-store')
    if (filename.includes(`${sep}_astro${sep}`)) response.setHeader('Cache-Control', 'public, max-age=31536000, immutable')
    if (extname(filename) === '.html') {
      const policies = JSON.parse(await readFile(resolve(dist, 'static-csp.json'), 'utf8'))
      const policy = policies.find((record) => record.route === page.route)?.policy
      if (!policy) throw new Error(`no generated CSP policy for ${page.route}`)
      response.setHeader('Content-Security-Policy', policy)
    }
    response.setHeader('X-Content-Type-Options', 'nosniff')
    response.setHeader('Referrer-Policy', 'no-referrer')
    response.end(data)
  } catch (error) {
    response.writeHead(error?.code === 'ENOENT' ? 404 : 500, { 'Content-Type': 'text/plain; charset=utf-8' })
    response.end(error?.code === 'ENOENT' ? 'not found' : 'static host error')
  }
})
server.listen(port, 'localhost')
const stop = () => server.close(() => process.exit(0))
process.on('SIGTERM', stop)
process.on('SIGINT', stop)
