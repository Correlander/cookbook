import { test, expect } from '@playwright/test'
import { readFile, stat, unlink } from 'node:fs/promises'

test('serves built Astro root with its generated CSP and static metadata', async ({ page }) => {
  const violations: string[] = []
  page.on('console', (message) => { if (/content security policy|refused to execute/i.test(message.text())) violations.push(message.text()) })
  const response = await page.goto('/')
  expect(response?.status()).toBe(200)
  await expect(page).toHaveTitle(/Cookbook/)
  await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /recipes/i)
  await expect(page.locator('a.skip-link')).toHaveAttribute('href', '#home')
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  expect(response?.headers()['content-security-policy']).toBe(policies.find((policy) => policy.route === '/')?.policy)
  await page.waitForTimeout(250)
  expect(violations).toEqual([])
})

test('serves confirmation as a static route and leaves its token untouched until explicit user action', async ({ page }) => {
  const response = await page.goto('/auth/confirm?token=synthetic-unused-token')
  expect(response?.status()).toBe(200)
  expect(page.url()).toContain('/auth/confirm?token=synthetic-unused-token')
  const api = await page.request.get('/api/auth/me')
  expect(api.status()).toBe(401)
  expect(api.headers()['cache-control']).toContain('no-store')
})

test('preserves invite query on the static root without caching the token URL', async ({ page }) => {
  const response = await page.goto('/?invite=synthetic-unused-invite')
  expect(response?.status()).toBe(200)
  expect(new URL(page.url()).searchParams.get('invite')).toBe('synthetic-unused-invite')
  expect(response?.headers()['cache-control']).toBe('no-store')
  expect((await page.request.get('/api/auth/me')).status()).toBe(401)
})

test('email sign-in remains explicit, establishes a session, and rejects replay', async ({ page, request }) => {
  const origin = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:4178'
  const response = await request.post('/api/auth/email', {
    headers: { Origin: origin },
    data: { email: 'cook@example.test', display_name: 'Browser Cook' },
  })
  expect(response.status()).toBe(200)
  const capturePath = process.env.E2E_EMAIL_CAPTURE
  if (!capturePath) throw new Error('E2E_EMAIL_CAPTURE is missing')
  let payload: { sign_in_url: string } | undefined
  for (let attempt = 0; attempt < 50; attempt++) {
    try { await stat(capturePath); payload = JSON.parse(await readFile(capturePath, 'utf8')); break } catch { /* Wait for the email command to create its private capture. */ }
    await page.waitForTimeout(50)
  }
  expect(Boolean(payload?.sign_in_url)).toBe(true)
  await unlink(capturePath)
  try { await page.goto(payload!.sign_in_url) } catch { throw new Error('confirmation page navigation failed') }
  const confirmationURL = new URL(page.url())
  expect(confirmationURL.pathname).toBe('/auth/confirm')
  expect(Boolean(confirmationURL.searchParams.get('token'))).toBe(true)
  expect((await page.request.get('/api/auth/me')).status()).toBe(401)
  await page.getByRole('button', { name: /continue to cookbook/i }).click()
  await expect.poll(async () => (await page.request.get('/api/auth/me')).status()).toBe(200)
  const token = new URL(payload!.sign_in_url).searchParams.get('token')
  expect(token).toBeTruthy()
  await page.request.post('/api/auth/logout', { headers: { Origin: origin } })
  const replay = await page.request.post('/api/auth/consume', { headers: { Origin: origin }, data: { token } })
  expect(replay.status()).toBe(401)
})

test('proxies health and API misses without static-index fallback', async ({ request }) => {
  const health = await request.get('/healthz')
  expect(health.status()).toBe(200)
  expect(health.headers()['content-type']).toContain('application/json')
  const missing = await request.get('/api/__playwright_missing_route__')
  expect(missing.status()).not.toBe(200)
  expect(missing.headers()['content-type']).not.toContain('text/html')
  expect(missing.headers()['content-security-policy']).toContain("script-src 'self'")
})

test('rejects a mismatched browser Origin on a proxied state-changing request', async ({ request }) => {
  const denied = await request.post('/api/auth/email', {
    headers: { Origin: 'http://attacker.invalid' },
    data: { email: 'nobody@example.test' },
  })
  expect(denied.status()).toBe(403)
  expect((await denied.json()).error).toBe('csrf_origin_mismatch')
})

test('keeps static layout and keyboard entry usable at phone, tablet, and desktop widths', async ({ page }) => {
  for (const width of [360, 768, 1280]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/')
    const dimensions = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }))
    expect(dimensions.scroll).toBeLessThanOrEqual(dimensions.width)
    await page.keyboard.press('Tab')
    await expect(page.locator(':focus')).toBeVisible()
  }
})

test('blocks an unapproved inline script under the generated response CSP', async ({ page }) => {
  await page.goto('/')
  await page.evaluate(() => {
    const script = document.createElement('script')
    script.textContent = 'window.__cookbookInlineFixtureRan = true'
    document.body.append(script)
  })
  await expect.poll(() => page.evaluate(() => Boolean((window as Window & { __cookbookInlineFixtureRan?: boolean }).__cookbookInlineFixtureRan))).toBe(false)
})
