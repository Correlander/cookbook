import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'

const script = new URL('./generate-static-headers.mjs', import.meta.url)
const scriptPath = decodeURIComponent(script.pathname)
const good = (body = '') => `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self' 'sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='; style-src 'self' 'sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'self'; form-action 'self'">${body}`

async function withDist(run) {
  const dist = await mkdtemp(join(tmpdir(), 'cookbook-csp-'))
  try {
    await writeFile(join(dist, 'index.html'), good())
    await writeFile(join(dist, 'auth-confirm-placeholder'), '')
    await (await import('node:fs/promises')).mkdir(join(dist, 'auth'))
    await writeFile(join(dist, 'auth/confirm.html'), good())
    return await run(dist)
  } finally { await rm(dist, { recursive: true, force: true }) }
}

test('emits Cloudflare headers and equivalent Caddy policies for each page', async () => withDist(async (dist) => {
  const result = spawnSync(process.execPath, [scriptPath, dist], { encoding: 'utf8' })
  assert.equal(result.status, 0, result.stderr)
  const [headers, caddy, manifest] = await Promise.all([
    readFile(join(dist, '_headers'), 'utf8'),
    readFile(join(dist, 'static-csp.caddy'), 'utf8'),
    readFile(join(dist, 'static-csp.json'), 'utf8'),
  ])
  assert.match(headers, /\/\n  Content-Security-Policy:/)
  assert.match(headers, /\/auth\/confirm\n  Content-Security-Policy:/)
  assert.match(headers, /\/auth\/confirm\.html\n  Content-Security-Policy:/)
  assert.match(headers, /Cache-Control: no-store/)
  assert.match(caddy, /@document0 path \/\nheader @document0 Content-Security-Policy/)
  assert.match(caddy, /@document1 path \/auth\/confirm/)
  assert.equal(JSON.parse(manifest).length, 2)
}))

for (const [name, bad] of [
  ['missing confirm page', null],
  ['unsafe inline source', good().replace("'self' 'sha256-", "'self' 'unsafe-inline' 'sha256-")],
  ['missing script hash', good().replace(" 'sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='", '')],
  ['duplicate policy', `${good()}<meta http-equiv="Content-Security-Policy" content="default-src 'self'">`],
  ['event handler', good('<button onclick="alert(1)">x</button>')],
]) {
  test(`rejects ${name}`, async () => withDist(async (dist) => {
    if (bad === null) await rm(join(dist, 'auth/confirm.html'))
    else await writeFile(join(dist, 'index.html'), bad)
    const result = spawnSync(process.execPath, [scriptPath, dist], { encoding: 'utf8' })
    assert.notEqual(result.status, 0)
    assert.match(result.stderr, /static header generation/)
  }))
}
