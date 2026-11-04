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
    if (request.url === '/api/missing') {
      response.writeHead(404, { 'content-type': 'application/json' }).end('{"error":"not_found"}')
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
    await mkdir(join(dist, 'account'), { recursive: true })
    await mkdir(join(dist, 'admin'), { recursive: true })
    await writeFile(join(dist, 'creator.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Creator profile</title><meta name="description" content="View a local creator profile"></head></html>')
    await writeFile(join(dist, 'account/creator-profile.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Creator profile</title><meta name="description" content="Manage a local creator profile"></head></html>')
    await writeFile(join(dist, 'index.html'), '<!doctype html><html><head><title>Root</title></head></html>')
    await writeFile(join(dist, 'auth/confirm.html'), '<!doctype html><html><head><meta name="robots" content="noindex,follow"><title>Confirm</title></head></html>')
    await writeFile(join(dist, 'auth/enroll.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Passkey enrollment</title></head></html>')
    await writeFile(join(dist, 'account.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Account</title></head></html>')
    await writeFile(join(dist, 'account/security.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Account security</title></head></html>')
    await writeFile(join(dist, 'admin.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Instance workbench</title></head></html>')
    await writeFile(join(dist, 'admin/settings.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Admin settings</title></head></html>')
    await writeFile(join(dist, 'library.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Library</title></head></html>')
    await writeFile(join(dist, 'recipe.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Recipe</title></head></html>')
    await writeFile(join(dist, 'cookbooks.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Cookbooks</title></head></html>')
    await writeFile(join(dist, 'cookbook.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Cookbook</title></head></html>')
    await writeFile(join(dist, 'course.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Course</title></head></html>')
    await writeFile(join(dist, 'discover.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Browse</title></head></html>')
    await writeFile(join(dist, 'create.html'), '<!doctype html><html><head><meta name="robots" content="noindex,nofollow"><title>Create</title></head></html>')
    await writeFile(join(dist, 'static-csp.json'), JSON.stringify([
      { route: '/', policy: "default-src 'self'; script-src 'self'" },
      { route: '/auth/confirm', policy: "default-src 'self'; style-src 'self'" },
      { route: '/auth/enroll', policy: "default-src 'self'; script-src 'self'" },
      { route: '/account', policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
      { route: '/account/security', policy: "default-src 'self'; img-src 'self'" },
      { route: '/account/creator-profile', policy: "default-src 'self'; connect-src 'self'" },
      { route: '/admin', policy: "default-src 'self'; script-src 'self'" },
      { route: '/creator', policy: "default-src 'self'; img-src 'self'" },
      { route: '/admin/settings', policy: "default-src 'self'; connect-src 'self'" },
      { route: '/library', policy: "default-src 'self'; script-src 'self'" },
      { route: '/recipe', policy: "default-src 'self'; img-src 'self'" },
      { route: '/cookbooks', policy: "default-src 'self'; script-src 'self'" },
      { route: '/cookbook', policy: "default-src 'self'; img-src 'self'" },
      { route: '/course', policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
      { route: '/discover', policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
      { route: '/create', policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
    ]))
    await writeFile(join(temp, 'secret.txt'), 'outside static root')
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
    const confirmHTML = await confirm.text()
    assert.match(confirmHTML, /Confirm/)
    assert.match(confirmHTML, /<meta name="robots" content="noindex,follow">/)
    assert.equal(confirm.headers.get('cache-control'), 'no-store')
    assert.equal(confirm.headers.get('content-security-policy'), "default-src 'self'; style-src 'self'")
    for (const alias of ['/auth/enroll', '/auth/enroll.html']) {
      const enrollment = await fetch(`${base}${alias}`)
      assert.equal(enrollment.status, 200, alias)
      assert.match(await enrollment.text(), /<meta name="robots" content="noindex,nofollow">/)
      assert.equal(enrollment.headers.get('cache-control'), 'no-store')
      assert.equal(enrollment.headers.get('content-security-policy'), "default-src 'self'; script-src 'self'")
    }
    const rootPage = await fetch(base)
    assert.equal(rootPage.status, 200)
    assert.doesNotMatch(await rootPage.text(), /<meta name="robots"/)
    assert.equal(rootPage.headers.get('content-security-policy'), "default-src 'self'; script-src 'self'")
    for (const page of [
      { path: '/account', title: /Account/, policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
      { path: '/account/security', title: /Account security/, policy: "default-src 'self'; img-src 'self'" },
      { path: '/account/creator-profile', title: /Creator profile/, policy: "default-src 'self'; connect-src 'self'", description: 'Manage a local creator profile' },
      { path: '/admin', title: /Instance workbench/, policy: "default-src 'self'; script-src 'self'" },
      { path: '/admin/settings', title: /Admin settings/, policy: "default-src 'self'; connect-src 'self'" },
    ]) {
      for (const alias of [page.path, `${page.path}.html`]) {
        const response = await fetch(`${base}${alias}?view=security`)
        assert.equal(response.status, 200, alias)
        assert.equal(new URL(response.url).search, '?view=security')
        const html = await response.text()
        assert.match(html, page.title)
        assert.equal(response.headers.get('cache-control'), 'no-store')
        assert.equal(response.headers.get('content-security-policy'), page.policy)
        assert.equal(response.headers.get('referrer-policy'), 'no-referrer')
        if (page.path === '/account/creator-profile') assert.match(html, /<meta name="robots" content="noindex,nofollow">/)
        if (page.description) assert.match(html, new RegExp(`<meta name="description" content="${page.description}">`))
        if (page.description) {
          const reload = await fetch(`${base}${alias}?view=security`)
          assert.equal(reload.status, 200, `${alias} reload`)
          assert.equal(new URL(reload.url).search, '?view=security')
          assert.equal(reload.headers.get('cache-control'), 'no-store')
          assert.equal(reload.headers.get('content-security-policy'), page.policy)
        }
      }
    }
    for (const page of [
      { path: '/discover', title: /Browse/, policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
      { path: '/create', title: /Create/, policy: "default-src 'self'; script-src 'self'; style-src 'self'" },
      { path: '/creator', title: /Creator profile/, policy: "default-src 'self'; img-src 'self'", description: 'View a local creator profile' },
    ]) {
      for (const alias of [page.path, `${page.path}.html`]) {
        const response = await fetch(`${base}${alias}?type=courses&keep=query`)
        assert.equal(response.status, 200, alias)
        assert.equal(new URL(response.url).search, '?type=courses&keep=query')
        const html = await response.text()
        assert.match(html, page.title)
        assert.match(html, /noindex,nofollow/)
        assert.equal(response.headers.get('cache-control'), 'no-store')
        assert.equal(response.headers.get('referrer-policy'), 'no-referrer')
        assert.equal(response.headers.get('content-security-policy'), page.policy)
        if (page.description) assert.match(html, new RegExp(`<meta name="description" content="${page.description}">`))
        if (page.description) {
          const reload = await fetch(`${base}${alias}?type=courses&keep=query`)
          assert.equal(reload.status, 200, `${alias} reload`)
          assert.equal(new URL(reload.url).search, '?type=courses&keep=query')
          assert.equal(reload.headers.get('cache-control'), 'no-store')
          assert.equal(reload.headers.get('content-security-policy'), page.policy)
        }
      }
    }
    for (const page of [
      { path: '/library', policy: "default-src 'self'; script-src 'self'", title: /Library/ },
      { path: '/recipe', policy: "default-src 'self'; img-src 'self'", title: /Recipe/ },
      { path: '/cookbooks', policy: "default-src 'self'; script-src 'self'", title: /Cookbooks/ },
      { path: '/cookbook', policy: "default-src 'self'; img-src 'self'", title: /Cookbook/ },
      { path: '/course', policy: "default-src 'self'; script-src 'self'; style-src 'self'", title: /Course/ },
    ]) {
      for (const alias of [page.path, `${page.path}.html`]) {
        const response = await fetch(`${base}${alias}?id=synthetic-id`)
        assert.equal(response.status, 200, alias)
        const html = await response.text()
        assert.match(html, page.title)
        if (page.path === '/course') assert.match(html, /<meta name="robots" content="noindex,nofollow">/)
        assert.equal(response.headers.get('cache-control'), 'no-store')
        assert.equal(response.headers.get('content-security-policy'), page.policy)
      }
    }
    const traversal = await fetch(`${base}/%2e%2e%2fsecret.txt`)
    assert.equal(traversal.status, 400)
    assert.equal(await traversal.text(), 'bad path')
    const malformedPath = await fetch(`${base}/%zz`)
    assert.equal(malformedPath.status, 400)
    assert.equal((await fetch(`${base}/unknown.html`)).status, 404)
    const origin = `${base}`
    const apiResponse = await fetch(`${base}/api/probe?invite=opaque-test-value`, { method: 'POST', headers: { origin, 'content-type': 'application/json' }, body: '{"probe":true}' })
    assert.equal(apiResponse.status, 418)
    assert.equal(apiResponse.headers.get('content-security-policy'), "default-src 'self'; script-src 'self'")
    assert.equal(apiResponse.headers.get('cache-control'), 'no-store')
    assert.match(apiResponse.headers.get('set-cookie'), /session=private/)
    assert.equal(apiResponse.headers.get('x-forwarded-check'), `localhost:${staticPort}|${origin}|/api/probe?invite=opaque-test-value|{"probe":true}`)
    const missing = await fetch(`${base}/api/missing`)
    assert.equal(missing.status, 404)
    assert.equal(missing.headers.get('content-type'), 'application/json')
    assert.equal(await missing.text(), '{"error":"not_found"}')
  } finally {
    if (child?.exitCode === null) { child.kill('SIGTERM'); await new Promise((done) => child.once('exit', done)) }
    await new Promise((done) => api.close(done))
    await rm(temp, { recursive: true, force: true })
  }
})
