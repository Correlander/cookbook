import { test, expect, type Route } from '@playwright/test'
import { readFile, stat, unlink } from 'node:fs/promises'
import { signInWithSyntheticEmail } from './auth'

test('serves built Astro root with its generated CSP and static metadata', async ({ page }) => {
  const violations: string[] = []
  page.on('console', (message) => { if (/content security policy|refused to execute/i.test(message.text())) violations.push(message.text()) })
  const response = await page.goto('/')
  expect(response?.status()).toBe(200)
  await expect(page).toHaveTitle(/Cookbook/)
  await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /recipes/i)
  await expect(page.locator('meta[name="robots"]')).toHaveCount(0)
  await expect(page.locator('a.skip-link')).toHaveAttribute('href', '#home')
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  expect(response?.headers()['content-security-policy']).toBe(policies.find((policy) => policy.route === '/')?.policy)
  const loadedJavaScriptBytes = await page.evaluate(() => performance.getEntriesByType('resource')
    .filter((entry) => /\/_astro\/.*\.js(?:\?|$)/.test(entry.name))
    .reduce((sum, entry) => sum + (entry as PerformanceResourceTiming).decodedBodySize, 0))
  test.info().annotations.push({ type: 'metric', description: `anonymous decoded JavaScript resource bytes: ${loadedJavaScriptBytes}` })
  await page.waitForTimeout(250)
  expect(violations).toEqual([])
})

test('serves confirmation aliases without consuming the token or loading Home content', async ({ page }) => {
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  const policy = policies.find((item) => item.route === '/auth/confirm')?.policy
  expect(policy).toBeTruthy()
  expect(policy).not.toMatch(/unsafe-inline|unsafe-eval/)

  const consumes: string[] = []
  const sessionStatuses: number[] = []
  const homeContentRequests: string[] = []
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path === '/api/auth/consume' && request.method() === 'POST') consumes.push(path)
    if (/^\/api\/(recipes|cookbooks|courses)(?:\/|$)/.test(path) || /^\/api\/public\/(recipes|cookbooks|remote-recipes|courses)(?:\/|$)/.test(path)) homeContentRequests.push(path)
  })
  page.on('response', (response) => {
    if (new URL(response.url()).pathname === '/api/auth/me') sessionStatuses.push(response.status())
  })

  await page.setViewportSize({ width: 320, height: 720 })
  for (const route of ['/auth/confirm', '/auth/confirm.html']) {
    const priorSessionChecks = sessionStatuses.length
    const priorHomeRequests = homeContentRequests.length
    const response = await page.goto(`${route}?token=synthetic-unused-token`)
    expect(response?.status(), route).toBe(200)
    expect(new URL(page.url()).pathname, route).toBe(route)
    expect(new URL(page.url()).searchParams.has('token'), route).toBe(true)
    expect(response?.headers()['cache-control'], route).toBe('no-store')
    expect(response?.headers()['referrer-policy'], route).toBe('no-referrer')
    expect(response?.headers()['content-security-policy'], route).toBe(policy)
    expect(await response?.text()).not.toContain('synthetic-unused-token')
    await expect(page).toHaveTitle(/Confirm your sign-in/)
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', 'noindex,follow')
    await expect(page.getByRole('heading', { name: 'Sign-in confirmation', level: 1 })).toBeVisible()
    await expect(page.getByRole('button', { name: /continue to cookbook/i })).toBeVisible()
    await expect.poll(() => sessionStatuses.length).toBe(priorSessionChecks + 1)
    expect(sessionStatuses.at(-1)).toBe(401)
    expect(consumes, route).toEqual([])
    expect(homeContentRequests.slice(priorHomeRequests), route).toEqual([])

    await page.keyboard.press('Tab')
    await expect(page.locator(':focus')).toHaveAttribute('href', '#home')
    await page.keyboard.press('Enter')
    await expect(page.locator('#home')).toBeFocused()
    const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)
    expect(horizontalOverflow, route).toBe(false)
  }
})

test('opens the shared sign-in dialog from a route link without consuming a token', async ({ page }) => {
  await page.goto('/?signin=1')
  await expect(page.getByRole('dialog', { name: /Sign in with email/i })).toBeVisible()
  expect(new URL(page.url()).searchParams.get('signin')).toBe('1')
})

test('real email confirmation can continue to Home while optional auth checks are pending', async ({ page, request }) => {
  const origin = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:4178'
  const capturePath = process.env.E2E_EMAIL_CAPTURE
  if (!capturePath) throw new Error('E2E_EMAIL_CAPTURE is missing')

  let releaseOptionalChecks!: () => void
  const optionalChecks = new Promise<void>((resolve) => { releaseOptionalChecks = resolve })
  let pendingRoutes = 0
  let resolveRoutesDrained!: () => void
  const routesDrained = new Promise<void>((resolve) => { resolveRoutesDrained = resolve })
  let resolveConfigHeld!: () => void
  const configHeld = new Promise<void>((resolve) => { resolveConfigHeld = resolve })
  let resolvePasskeysHeld!: () => void
  const passkeysHeld = new Promise<void>((resolve) => { resolvePasskeysHeld = resolve })
  const holdOptionalRoute = async (route: Route) => {
    pendingRoutes += 1
    const path = new URL(route.request().url()).pathname
    if (path === '/api/public/config') resolveConfigHeld()
    if (path === '/api/auth/passkeys') resolvePasskeysHeld()
    try {
      await optionalChecks
      await route.continue()
    } catch {
      // The browser can cancel a held fetch while leaving the confirmation document.
    } finally {
      pendingRoutes -= 1
      if (pendingRoutes === 0) resolveRoutesDrained()
    }
  }
  await page.route('**/api/public/config', holdOptionalRoute)
  await page.route('**/api/auth/passkeys', holdOptionalRoute)

  let consumePosts = 0
  const consumeStatuses: number[] = []
  const homeContentRequests: string[] = []
  page.on('request', (browserRequest) => {
    const path = new URL(browserRequest.url()).pathname
    if (path === '/api/auth/consume' && browserRequest.method() === 'POST') consumePosts += 1
    if (/^\/api\/(recipes|cookbooks|courses)(?:\/|$)/.test(path) || /^\/api\/public\/(recipes|cookbooks|remote-recipes|courses)(?:\/|$)/.test(path)) homeContentRequests.push(path)
  })
  page.on('response', (response) => {
    const browserRequest = response.request()
    if (new URL(response.url()).pathname === '/api/auth/consume' && browserRequest.method() === 'POST') consumeStatuses.push(response.status())
  })

  let captureRemoved = false
  try {
    const response = await request.post('/api/auth/email', {
      headers: { Origin: origin },
      data: { email: 'home-confirm@example.test', display_name: 'Home Browser' },
    })
    expect(response.status()).toBe(200)

    let payload: { sign_in_url?: string } | undefined
    for (let attempt = 0; attempt < 50; attempt++) {
      try {
        await stat(capturePath)
        payload = JSON.parse(await readFile(capturePath, 'utf8')) as { sign_in_url?: string }
        await unlink(capturePath)
        captureRemoved = true
        break
      } catch {
        await page.waitForTimeout(50)
      }
    }
    expect(Boolean(payload?.sign_in_url)).toBe(true)
    const signInURL = new URL(payload!.sign_in_url!)
    expect(signInURL.pathname).toBe('/auth/confirm')

    await page.goto(payload!.sign_in_url!)
    await configHeld
    expect(new URL(page.url()).searchParams.has('token')).toBe(true)
    expect(consumePosts).toBe(0)
    expect(homeContentRequests).toEqual([])
    await expect(page.getByRole('button', { name: /continue to cookbook/i })).toBeVisible()
    expect((await page.request.get('/api/auth/me')).status()).toBe(401)

    await page.getByRole('button', { name: /continue to cookbook/i }).click()
    await expect.poll(async () => (await page.request.get('/api/auth/me')).status()).toBe(200)
    await passkeysHeld
    expect(consumePosts).toBe(1)
    expect(consumeStatuses).toEqual([200])
    expect(new URL(page.url()).searchParams.has('token')).toBe(false)
    await expect(page.getByRole('button', { name: 'Continue to Home' })).toBeVisible()
    expect(pendingRoutes).toBeGreaterThanOrEqual(2)

    await page.getByRole('button', { name: 'Continue to Home' }).click()
    await expect(page).toHaveURL(/\/$/)
    await expect(page.getByRole('heading', { name: 'Welcome back, Home Browser.', level: 1 })).toBeVisible()
  } finally {
    releaseOptionalChecks()
    await page.unroute('**/api/public/config', holdOptionalRoute)
    await page.unroute('**/api/auth/passkeys', holdOptionalRoute)
    if (pendingRoutes > 0) await routesDrained
    if (!captureRemoved) {
      try { await unlink(capturePath) } catch { /* No private mail payload was created. */ }
    }
  }
})

test('keeps the root navigation keyboard reachable and retains its legacy hash targets', async ({ page }) => {
  await page.goto('/')
  await page.keyboard.press('Tab')
  await expect(page.locator(':focus')).toHaveAttribute('href', '#home')
  await page.keyboard.press('Tab')
  await expect(page.locator(':focus')).toHaveAttribute('href', '/#home')
  await page.keyboard.press('Tab')
  await expect(page.locator(':focus')).toHaveAttribute('href', '/#home')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/#home$/)
  await page.goto('/#cookbooks')
  await expect(page.locator('#cookbooks')).toBeVisible()
  await page.goto('/#recipes')
  await expect(page.locator('#recipes')).toBeVisible()
  await page.goto('/#my-cookbooks')
  await expect(page).toHaveURL(/\/#my-cookbooks$/)
})

test('preserves invite query on the static root without caching the token URL', async ({ page }) => {
  const response = await page.goto('/?invite=synthetic-unused-invite')
  expect(response?.status()).toBe(200)
  expect(new URL(page.url()).searchParams.get('invite')).toBe('synthetic-unused-invite')
  expect(response?.headers()['cache-control']).toBe('no-store')
  expect((await page.request.get('/api/auth/me')).status()).toBe(401)
})

test('serves direct account and admin route aliases with private CSP and anonymous guards', async ({ page }) => {
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  const routes = [
    { route: '/account/security', title: /Account security/ },
    { route: '/account/creator-profile', title: /Creator profile/, description: 'Manage your local creator profile on this Cookbook instance.' },
    { route: '/admin/settings', title: /Instance settings/ },
    { route: '/creator', title: /Creator profile/, description: 'View a local creator profile on this Cookbook instance.' },
  ]

  for (const route of routes) {
    const policy = policies.find((item) => item.route === route.route)?.policy
    expect(policy, `generated CSP for ${route.route}`).toBeTruthy()
    expect(policy).toContain("script-src 'self'")
    expect(policy).not.toMatch(/unsafe-inline|unsafe-eval/)

    for (const path of [route.route, `${route.route}.html`]) {
      const response = await page.goto(path)
      expect(response?.status(), path).toBe(200)
      expect(response?.headers()['cache-control'], path).toBe('no-store')
      expect(response?.headers()['content-security-policy'], path).toBe(policy)
      expect(response?.headers()['referrer-policy'], path).toBe('no-referrer')
      await expect(page).toHaveURL(new RegExp(`${path.replaceAll('/', '\\/')}$`))
      await expect(page).toHaveTitle(route.title)
      if (route.description) await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', route.description)
      await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', 'noindex,nofollow')
      await expect(page.getByRole('heading', { name: 'Sign in to continue' })).toBeVisible()
    }
  }
})

test('signed-in account users can open Account Security and cannot open instance settings', async ({ page, request }) => {
  const user = await signInWithSyntheticEmail(page, request, 'route-user@example.test', 'Route User')
  expect(user.is_admin).toBe(false)

  for (const path of ['/account/security', '/account/security.html']) {
    await page.goto(path)
    await expect(page.getByRole('heading', { name: 'Account security', level: 2 })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Browser sessions' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Account', exact: true })).toBeVisible()
  }

  const adminSettingsRequests: string[] = []
  page.on('request', (browserRequest) => {
    const url = new URL(browserRequest.url())
    if (url.pathname === '/api/admin/settings') adminSettingsRequests.push(url.pathname)
  })
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { name: 'Instance settings access required' })).toBeVisible()
  expect(adminSettingsRequests).toEqual([])
})

test('signed-in instance administrators can open settings directly and through the HTML alias', async ({ page, request }) => {
  const user = await signInWithSyntheticEmail(page, request, 'admin@example.test', 'E2E Admin')
  expect(user.is_admin).toBe(true)

  for (const path of ['/admin/settings', '/admin/settings.html']) {
    const response = await page.goto(path)
    expect(response?.status()).toBe(200)
    expect(response?.headers()['cache-control']).toBe('no-store')
    await expect(page).toHaveTitle(/Instance settings/)
    await expect(page.getByRole('heading', { name: 'Instance administration', level: 1 })).toBeVisible()
    await expect(page.getByText('Enable public browsing')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Instance', exact: true })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Administration' })).toHaveCount(0)
  }

  await page.goto('/admin')
  await expect(page.getByRole('heading', { name: 'Instance access', level: 1 })).toBeVisible()
  await page.getByRole('link', { name: 'Open Instance Settings' }).click()
  await expect(page).toHaveURL(/\/admin\/settings$/)
  await expect(page.getByRole('heading', { name: 'Instance administration', level: 1 })).toBeVisible()
})

test('email sign-in remains explicit, establishes a session, and rejects replay', async ({ page, request }) => {
  const origin = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:4178'
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2', transport: 'internal', hasResidentKey: true,
      hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
    },
  })
  await cdp.send('WebAuthn.setAutomaticPresenceSimulation', { authenticatorId, enabled: true })
  await cdp.send('WebAuthn.setUserVerified', { authenticatorId, isUserVerified: true })
  await page.addInitScript(() => {
    const state = { create: 'not-called', options: null as unknown, result: null as unknown }
    ;(window as Window & { __passkeyDiagnostic?: typeof state }).__passkeyDiagnostic = state
    const original = navigator.credentials.create.bind(navigator.credentials)
    navigator.credentials.create = async (options) => {
      const publicKey = options?.publicKey
      state.create = 'started'
      state.options = publicKey ? {
        keys: Object.keys(publicKey),
        rpId: publicKey.rp?.id ?? location.hostname,
        authenticatorSelection: publicKey.authenticatorSelection ? {
          ...publicKey.authenticatorSelection,
        } : null,
      } : { keys: Object.keys(options ?? {}) }
      try {
        const result = await original(options)
        state.create = 'resolved'
        state.result = result ? {
          type: result.type,
          idPresent: Boolean(result.id),
          responseType: result instanceof PublicKeyCredential ? result.response.constructor.name : null,
        } : null
        return result
      } catch (error) {
        state.create = 'rejected'
        state.result = { name: error instanceof DOMException ? error.name : 'Error' }
        throw error
      }
    }
  })
  let consumePosts = 0
  const consumeStatuses: number[] = []
  page.on('request', (browserRequest) => {
    if (new URL(browserRequest.url()).pathname === '/api/auth/consume' && browserRequest.method() === 'POST') consumePosts += 1
  })
  page.on('response', (response) => {
    if (new URL(response.url()).pathname === '/api/auth/consume' && response.request().method() === 'POST') consumeStatuses.push(response.status())
  })
  let payload: { sign_in_url: string } | undefined
  await test.step('Email delivery', async () => {
    const response = await request.post('/api/auth/email', {
      headers: { Origin: origin },
      data: { email: 'cook@example.test', display_name: 'Browser Cook' },
    })
    expect(response.status()).toBe(200)
    const capturePath = process.env.E2E_EMAIL_CAPTURE
    if (!capturePath) throw new Error('E2E_EMAIL_CAPTURE is missing')
    for (let attempt = 0; attempt < 50; attempt++) {
      try { await stat(capturePath); payload = JSON.parse(await readFile(capturePath, 'utf8')); break } catch { /* Wait for the email command to create its private capture. */ }
      await page.waitForTimeout(50)
    }
    expect(Boolean(payload?.sign_in_url)).toBe(true)
    await unlink(capturePath)
  })
  await test.step('Confirmation page', async () => {
    await page.goto(payload!.sign_in_url)
    const confirmationURL = new URL(page.url())
    expect(confirmationURL.pathname).toBe('/auth/confirm')
    expect(confirmationURL.searchParams.has('token')).toBe(true)
    await expect(page).toHaveTitle(/Confirm your sign-in/)
    await expect(page.getByRole('heading', { name: 'Sign-in confirmation', level: 1 })).toBeVisible()
    expect(consumePosts).toBe(0)
    expect((await page.request.get('/api/auth/me')).status()).toBe(401)
  })
  await test.step('Session establishment', async () => {
    await page.getByRole('button', { name: /continue to cookbook/i }).click()
    await expect.poll(async () => (await page.request.get('/api/auth/me')).status()).toBe(200)
    expect(consumePosts).toBe(1)
    expect(consumeStatuses).toEqual([200])
    expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  })

  const passkeyDialog = page.getByRole('dialog', { name: 'Add a passkey?' })
  const passkeyRequests: Array<{ path: string; status?: number }> = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (/^\/api\/auth\/passkey\/(register|login)\/(begin|finish)$/.test(url.pathname)) passkeyRequests.push({ path: url.pathname })
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (/^\/api\/auth\/passkey\/(register|login)\/(begin|finish)$/.test(url.pathname)) {
      const found = [...passkeyRequests].reverse().find((item) => item.path === url.pathname && item.status === undefined)
      if (found) found.status = response.status()
    }
  })
  await test.step('Passkey registration', async () => {
    await page.getByRole('button', { name: 'Create passkey' }).click()
    try {
      await expect.poll(() => passkeyRequests.find((item) => item.path === '/api/auth/passkey/register/begin')?.status ?? 0, { timeout: 12_000 }).toBeGreaterThan(0)
    } catch {
      const diagnostic = await page.evaluate(() => ({
        passkey: (window as Window & { __passkeyDiagnostic?: unknown }).__passkeyDiagnostic,
        submit: (() => {
          const form = document.querySelector('[role="dialog"] form')
          const input = form?.querySelector('input')
          return { valid: form instanceof HTMLFormElement ? form.checkValidity() : null, inputRequired: input?.required ?? null, inputValuePresent: Boolean(input?.value) }
        })(),
      }))
      throw new Error(`passkey registration did not begin: ${JSON.stringify({ requests: passkeyRequests, diagnostic })}`)
    }
    const beginResponse = passkeyRequests.find((item) => item.path === '/api/auth/passkey/register/begin')
    if (!beginResponse || beginResponse.status !== 200) throw new Error(`passkey register begin failed: ${JSON.stringify({ requests: passkeyRequests, webauthn: await page.evaluate(() => (window as Window & { __passkeyDiagnostic?: unknown }).__passkeyDiagnostic) })}`)
    expect(beginResponse.status, 'passkey registration ceremony begins').toBe(200)
    const registrationAlerts = passkeyDialog.getByRole('alert')
    await expect.poll(async () => {
      if (await passkeyDialog.isHidden()) return true
      const messages = await registrationAlerts.allTextContents()
      return messages.some((message) => message.trim().length > 0)
    }, { timeout: 15_000 }).toBe(true)
    if (await passkeyDialog.isVisible()) {
      const message = (await registrationAlerts.allTextContents())
        .map((text) => text.trim())
        .filter(Boolean)
        .join(' | ') || null
      const webauthn = await page.evaluate(() => (window as Window & { __passkeyDiagnostic?: unknown }).__passkeyDiagnostic)
      throw new Error(`virtual passkey registration failed: ${JSON.stringify({ message, requests: passkeyRequests, webauthn })}`)
    }
    await expect.poll(() => passkeyRequests.find((item) => item.path === '/api/auth/passkey/register/finish')?.status ?? 0).toBe(201)
  })
  await test.step('Recipe create and edit', async () => {
    await page.getByRole('button', { name: 'Add a recipe', exact: true }).first().click()
    const form = page.getByRole('dialog', { name: 'Add a recipe' })
    await form.getByLabel('Title').fill('Browser private recipe')
    await form.getByLabel('Description').fill('Created by the browser journey')
    await form.getByLabel(/Ingredients/).fill('Flour | 1 cup | sifted')
    await form.locator('textarea[id^="recipe-step-"]').first().fill('Mix')
    const creatorCredit = form.getByLabel('Creator credit')
    await expect(creatorCredit).toBeVisible()
    await expect(creatorCredit).toBeEnabled()
    await creatorCredit.selectOption('local_profile')
    await expect(
      form.getByText(/private profile with an empty name is created if needed/i),
    ).toBeVisible()
    await form.getByRole('button', { name: 'Save recipe' }).click()
    await expect(form).toBeHidden()
    const recipeCard = page.getByRole('button', { name: 'Open Browser private recipe' })
    await expect(recipeCard).toBeVisible()
    await recipeCard.click()
    await page.getByRole('button', { name: 'Edit recipe' }).click()
    const edit = page.getByRole('dialog', { name: 'Edit recipe' })
    await edit.getByLabel('Title').fill('Browser private recipe updated')
    await edit.locator('textarea[id^="recipe-step-"]').first().fill('Mix well')
    await edit.getByRole('button', { name: 'Save changes' }).click()
    await expect(edit).toBeHidden()
  })
  await test.step('Recipe history and stale update conflict', async () => {
    await page.getByRole('button', { name: 'Open Browser private recipe updated' }).click()
    await page.getByRole('button', { name: 'Version history' }).click()
    const history = page.getByLabel('Recipe version history')
    await expect(history.getByText(/Version 1: Browser private recipe/)).toBeVisible()
    await expect(history.getByText(/Version 2: Browser private recipe updated/)).toBeVisible()
    page.once('dialog', (dialog) => dialog.accept())
    await history.getByRole('button', { name: 'Restore' }).click()
    await expect(page.getByRole('heading', { name: 'Browser private recipe' })).toBeVisible()
    const recipes = await page.request.get('/api/recipes')
    const currentRecipes = await recipes.json()
    const privateRecipe = currentRecipes.find((recipe: { title: string }) => recipe.title === 'Browser private recipe')
    expect(privateRecipe?.visibility).toBe('private')
    const recipeID = privateRecipe.id as string
    const stale = await page.evaluate(async ({ recipeID }) => fetch(`/api/recipes/${recipeID}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'Stale overwrite', description: 'stale', servings: 2, prep_time_minutes: 10,
        cook_time_minutes: 20, tags: [], visibility: 'private', version: 2,
        ingredients: [{ name: 'Flour', quantity: '1 cup', notes: '' }], steps: [{ text: 'Mix' }],
      }),
    }).then((response) => response.status), { recipeID })
    expect(stale, 'stale recipe update is rejected').toBe(409)
    const anonymous = await page.context().browser()!.newContext()
    try { expect((await anonymous.request.get(`${origin}/api/recipes/${recipeID}`)).status(), 'private recipe requires an authenticated session').toBe(401) }
    finally { await anonymous.close() }
  })

  await test.step('Logout closes the editor', async () => {
    await page.getByRole('button', { name: 'Edit recipe' }).click()
    await page.getByRole('button', { name: 'Sign out' }).click()
    await expect.poll(async () => (await page.request.get('/api/auth/me')).status()).toBe(401)
    const editDialog = page.getByRole('dialog', { name: 'Edit recipe' })
    await expect(editDialog, 'signing out closes the auth-bound editor').toBeHidden()
  })
  await test.step('Passkey sign-in', async () => {
    await page.getByRole('button', { name: 'Sign in', exact: true }).click()
    await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
    await expect.poll(() => passkeyRequests.find((item) => item.path === '/api/auth/passkey/login/finish')?.status ?? 0).toBe(200)
    await expect.poll(async () => (await page.request.get('/api/auth/me')).status()).toBe(200)
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId })
    await cdp.send('WebAuthn.disable')
  })

  await test.step('Logout and reject token replay', async () => {
    const token = new URL(payload!.sign_in_url).searchParams.get('token')
    expect(token).toBeTruthy()
    await page.request.post('/api/auth/logout', { headers: { Origin: origin } })
    const replay = await page.request.post('/api/auth/consume', { headers: { Origin: origin }, data: { token } })
    expect(replay.status()).toBe(400)
    expect((await replay.json()).error).toBe('invalid_or_expired_token')
  })
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
