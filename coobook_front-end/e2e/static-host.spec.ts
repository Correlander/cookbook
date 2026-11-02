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

  const createRecipe = await page.evaluate(async () => {
    const response = await fetch('/api/recipes', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'Browser private recipe', description: 'Created by Playwright', servings: 2,
        prep_time_minutes: 10, cook_time_minutes: 20, tags: [], visibility: 'private',
        ingredients: [{ name: 'Flour', quantity: '1 cup', notes: '' }], steps: [{ text: 'Mix' }],
      }),
    })
    return { status: response.status, body: await response.json() }
  })
  expect(createRecipe.status).toBe(201)
  expect(createRecipe.body.visibility).toBe('private')
  const recipeID = createRecipe.body.id as string
  const update = await page.evaluate(async ({ recipeID }) => {
    const response = await fetch(`/api/recipes/${recipeID}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'Browser private recipe updated', description: 'Updated', servings: 2,
        prep_time_minutes: 10, cook_time_minutes: 20, tags: [], visibility: 'private', version: 1,
        ingredients: [{ name: 'Flour', quantity: '1 cup', notes: '' }], steps: [{ text: 'Mix well' }],
      }),
    })
    return { status: response.status, body: await response.json() }
  }, { recipeID })
  expect(update.status).toBe(200)
  expect(update.body.version).toBe(2)
  const revisions = await page.request.get(`/api/recipes/${recipeID}/revisions`)
  expect(revisions.status()).toBe(200)
  expect((await revisions.json()).length).toBeGreaterThanOrEqual(2)
  const stale = await page.evaluate(async ({ recipeID }) => fetch(`/api/recipes/${recipeID}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: 'Stale overwrite', visibility: 'private', version: 1 }),
  }).then((response) => response.status), { recipeID })
  expect(stale).toBe(409)
  const restore = await page.request.post(`/api/recipes/${recipeID}/revisions/1/restore`, { data: { expected_version: 2 } })
  expect(restore.status()).toBe(200)
  expect((await restore.json()).version).toBe(3)
  const anonymous = await page.context().browser()!.newContext()
  try { expect((await anonymous.request.get(`${origin}/api/recipes/${recipeID}`)).status()).toBe(401) }
  finally { await anonymous.close() }

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
