import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer } from 'node:net'
import { createServer as createHttpServer } from 'node:http'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = fileURLToPath(new URL('../..', import.meta.url))
async function port() {
  const server = createServer()
  await new Promise((done) => server.listen(0, '127.0.0.1', done))
  const result = server.address().port
  await new Promise((done) => server.close(done))
  return result
}

test('static host preserves confirmation query and proxies public Host, Origin, cookies, status and cache headers', async () => {
  const temp = await mkdtemp(join(tmpdir(), 'cookbook-static-host-'))
  const api = createHttpServer((request, response) => {
    if (request.url === '/healthz') {
      response.writeHead(200, { 'content-type': 'application/json', 'cache-control': 'no-store' }).end('{"ok":true}')
      return
    }
    const chunks = []
    request.on('data', (chunk) => chunks.push(chunk))
    request.on('end', () => {
    response.writeHead(418, {
      'content-type': 'application/json',
      'content-security-policy': "default-src 'self'; script-src 'self'",
      'cache-control': 'no-store',
      'set-cookie': 'session=private; HttpOnly; SameSite=Lax',
      'x-forwarded-check': `${request.headers.host}|${request.headers.origin}|${request.url}|${Buffer.concat(chunks).toString()}`,
    }).end('{"error":"teapot"}')
    })
  })
  let child
  try {
    const apiPort = await new Promise((done) => api.listen(0, '127.0.0.1', () => done(api.address().port)))
    const staticPort = await port()
    const dist = join(temp, 'dist')
    await mkdir(join(dist, 'auth'), { recursive: true })
    await writeFile(join(dist, 'index.html'), '<!doctype html><title>Root</title>')
    await writeFile(join(dist, 'auth/confirm.html'), '<!doctype html><title>Confirm</title>')
    await writeFile(join(dist, 'static-csp.json'), JSON.stringify([
      { route: '/', policy: "default-src 'self'" },
      { route: '/auth/confirm', policy: "default-src 'self'" },
    ]))
    child = spawn(process.execPath, [join(root, 'coobook_front-end/scripts/static-host.mjs')], {
      env: { ...process.env, STATIC_DIST: dist, STATIC_PORT: String(staticPort), API_ORIGIN: `http://127.0.0.1:${apiPort}` },
      stdio: ['ignore', 'ignore', 'pipe'],
    })
    let hostError = ''
    child.stderr.setEncoding('utf8').on('data', (chunk) => { hostError += chunk })
    const base = `http://localhost:${staticPort}`
    let ready = false
    for (let attempt = 0; attempt < 50; attempt++) {
      try { if ((await fetch(`${base}/healthz`)).ok) { ready = true; break } } catch { /* Wait for the private local host to start. */ }
      await new Promise((done) => setTimeout(done, 30))
    }
    assert.equal(ready, true, hostError)
    const confirm = await fetch(`${base}/auth/confirm?token=opaque-test-value`)
    assert.equal(confirm.status, 200)
    assert.match(await confirm.text(), /Confirm/)
    assert.equal(confirm.headers.get('cache-control'), 'no-store')
    const origin = `${base}`
    const apiResponse = await fetch(`${base}/api/probe?invite=opaque-test-value`, { method: 'POST', headers: { origin, 'content-type': 'application/json' }, body: '{"probe":true}' })
    assert.equal(apiResponse.status, 418)
    assert.equal(apiResponse.headers.get('content-security-policy'), "default-src 'self'; script-src 'self'")
    assert.equal(apiResponse.headers.get('cache-control'), 'no-store')
    assert.match(apiResponse.headers.get('set-cookie'), /session=private/)
    assert.equal(apiResponse.headers.get('x-forwarded-check'), `localhost:${staticPort}|${origin}|/api/probe?invite=opaque-test-value|{"probe":true}`)
    const missing = await fetch(`${base}/api/missing`)
    assert.notEqual(missing.headers.get('content-type'), 'text/html')
  } finally {
    if (child?.exitCode === null) { child.kill('SIGTERM'); await new Promise((done) => child.once('exit', done)) }
    await new Promise((done) => api.close(done))
    await rm(temp, { recursive: true, force: true })
  }
})
