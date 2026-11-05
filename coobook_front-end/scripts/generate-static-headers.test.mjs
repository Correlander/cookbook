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
    for (const file of ['auth/confirm.html', 'auth/enroll.html', 'account.html', 'account/security.html', 'account/creator-profile.html', 'creator.html', 'admin.html', 'admin/settings.html', 'library.html', 'recipe.html', 'cookbooks.html', 'cookbook.html', 'course.html', 'menu.html', 'discover.html', 'create.html']) {
      const directory = file.slice(0, file.lastIndexOf('/'))
      await (await import('node:fs/promises')).mkdir(join(dist, directory), { recursive: true })
      await writeFile(join(dist, file), good())
    }
    return await run(dist)
  } finally { await rm(dist, { recursive: true, force: true }) }
}

test('emits Cloudflare headers and equivalent Caddy policies for every static route', async () => withDist(async (dist) => {
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
  assert.match(headers, /\/auth\/enroll\n  Content-Security-Policy:/)
  assert.match(headers, /\/auth\/enroll\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/account\/security\n  Content-Security-Policy:/)
  assert.match(headers, /\/account\/security\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/account\/creator-profile\n  Content-Security-Policy:/)
  assert.match(headers, /\/account\/creator-profile\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/creator\n  Content-Security-Policy:/)
  assert.match(headers, /\/creator\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/account\/creator-profile\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/creator\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/admin\n  Content-Security-Policy:/)
  assert.match(headers, /\/admin\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/admin\/settings\n  Content-Security-Policy:/)
  assert.match(headers, /\/admin\/settings\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/library\n  Content-Security-Policy:/)
  assert.match(headers, /\/library\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/recipe\n  Content-Security-Policy:/)
  assert.match(headers, /\/recipe\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/cookbooks\n  Content-Security-Policy:/)
  assert.match(headers, /\/cookbooks\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/cookbook\n  Content-Security-Policy:/)
  assert.match(headers, /\/cookbook\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/course\n  Content-Security-Policy:/)
  assert.match(headers, /\/course\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/menu\n  Content-Security-Policy:/)
  assert.match(headers, /\/menu\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/discover\n  Content-Security-Policy:/)
  assert.match(headers, /\/discover\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/create\n  Content-Security-Policy:/)
  assert.match(headers, /\/create\.html\n  Content-Security-Policy:/)
  assert.match(headers, /\/\n  Cache-Control: no-store/)
  assert.match(headers, /\/auth\/confirm\n  Cache-Control: no-store/)
  assert.match(headers, /\/auth\/enroll\n  Cache-Control: no-store/)
  assert.match(headers, /\/auth\/enroll\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/account\n  Cache-Control: no-store/)
  assert.match(headers, /\/account\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/account\/security\n  Cache-Control: no-store/)
  assert.match(headers, /\/account\/creator-profile\n  Cache-Control: no-store/)
  assert.match(headers, /\/creator\n  Cache-Control: no-store/)
  assert.match(headers, /\/admin\n  Cache-Control: no-store/)
  assert.match(headers, /\/admin\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/admin\/settings\n  Cache-Control: no-store/)
  assert.match(headers, /\/library\n  Cache-Control: no-store/)
  assert.match(headers, /\/recipe\n  Cache-Control: no-store/)
  assert.match(headers, /\/cookbooks\n  Cache-Control: no-store/)
  assert.match(headers, /\/cookbook\n  Cache-Control: no-store/)
  assert.match(headers, /\/course\n  Cache-Control: no-store/)
  assert.match(headers, /\/course\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/menu\n  Cache-Control: no-store/)
  assert.match(headers, /\/menu\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/discover\n  Cache-Control: no-store/)
  assert.match(headers, /\/discover\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/create\n  Cache-Control: no-store/)
  assert.match(headers, /\/create\.html\n  Cache-Control: no-store/)
  assert.match(headers, /\/index\.html\n  Cache-Control: no-store/)
  assert.match(caddy, /@html path \/ \/index\.html \/auth\/confirm \/auth\/confirm\.html \/auth\/enroll \/auth\/enroll\.html \/account \/account\.html \/account\/security \/account\/security\.html \/account\/creator-profile \/account\/creator-profile\.html \/creator \/creator\.html \/admin \/admin\.html \/admin\/settings \/admin\/settings\.html \/library \/library\.html \/recipe \/recipe\.html \/cookbooks \/cookbooks\.html \/cookbook \/cookbook\.html \/course \/course\.html \/menu \/menu\.html \/discover \/discover\.html \/create \/create\.html/)
  assert.match(headers, /Cache-Control: no-store/)
  assert.match(caddy, /@document0 path \/ \/index\.html\nheader @document0 Content-Security-Policy/)
  assert.match(caddy, /@document1 path \/auth\/confirm/)
  assert.match(caddy, /@document2 path \/auth\/enroll \/auth\/enroll\.html/)
  assert.match(caddy, /@document3 path \/account \/account\.html/)
  assert.match(caddy, /@document4 path \/account\/security \/account\/security\.html/)
  assert.match(caddy, /@document5 path \/account\/creator-profile \/account\/creator-profile\.html/)
  assert.match(caddy, /@document6 path \/creator \/creator\.html/)
  assert.match(caddy, /@document7 path \/admin \/admin\.html/)
  assert.match(caddy, /@document8 path \/admin\/settings \/admin\/settings\.html/)
  assert.match(caddy, /@document9 path \/library \/library\.html/)
  assert.match(caddy, /@document10 path \/recipe \/recipe\.html/)
  assert.match(caddy, /@document11 path \/cookbooks \/cookbooks\.html/)
  assert.match(caddy, /@document12 path \/cookbook \/cookbook\.html/)
  assert.match(caddy, /@document13 path \/course \/course\.html\nheader @document13 Content-Security-Policy/)
  assert.match(caddy, /@document14 path \/menu \/menu\.html/)
  assert.match(caddy, /@document15 path \/discover \/discover\.html/)
  assert.match(caddy, /@document16 path \/create \/create\.html/)
  assert.equal(JSON.parse(manifest).length, 17)
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
