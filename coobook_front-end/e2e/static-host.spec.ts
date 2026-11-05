import { test, expect, type BrowserContext, type Route } from '@playwright/test'
import { readFile, stat, unlink } from 'node:fs/promises'
import { openAccountMenu, signInWithSyntheticEmail } from './auth'

async function attemptCleanup(failures: string[], label: string, operation: () => Promise<void>): Promise<void> {
  try {
    await operation()
  } catch (error) {
    failures.push(`${label}: ${error instanceof Error ? error.message : 'cleanup failed'}`)
  }
}

test('serves built Astro root with its generated CSP and static metadata', async ({ page }) => {
  const violations: string[] = []
  page.on('console', (message) => { if (/content security policy|refused to execute/i.test(message.text())) violations.push(message.text()) })
  const response = await page.goto('/')
  expect(response?.status()).toBe(200)
  await expect(page).toHaveTitle(/Cookbook/)
  await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /recipes/i)
  await expect(page.locator('meta[name="robots"]')).toHaveCount(0)
  await expect(page.locator('a.skip-link')).toHaveAttribute('href', '#home')
  const skipLink = page.locator('a.skip-link')
  await expect(skipLink).toHaveCSS('position', 'fixed')
  const restingSkipLinkBox = await skipLink.boundingBox()
  expect(restingSkipLinkBox).not.toBeNull()
  expect(restingSkipLinkBox!.y + restingSkipLinkBox!.height).toBeLessThanOrEqual(0)
  await page.setViewportSize({ width: 320, height: 720 })
  await page.keyboard.press('Tab')
  await expect(skipLink).toBeFocused()
  await expect(skipLink).toBeVisible()
  await expect(skipLink).toHaveCSS('color', 'rgb(255, 255, 255)')
  await expect(skipLink).toHaveCSS('z-index', '20')
  const skipLinkBox = await skipLink.boundingBox()
  expect(skipLinkBox).not.toBeNull()
  expect(skipLinkBox!.x).toBeGreaterThanOrEqual(0)
  expect(skipLinkBox!.x + skipLinkBox!.width).toBeLessThanOrEqual(320)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(320)
  await page.keyboard.press('Enter')
  await expect(page.locator('#home')).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: /sign in or create an account/i })).toBeFocused()
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  const desktopRestingBox = await skipLink.boundingBox()
  expect(desktopRestingBox).not.toBeNull()
  expect(desktopRestingBox!.y + desktopRestingBox!.height).toBeLessThanOrEqual(0)
  await page.keyboard.press('Tab')
  await expect(skipLink).toBeFocused()
  await expect(skipLink).toBeVisible()
  const desktopSkipLinkBox = await skipLink.boundingBox()
  expect(desktopSkipLinkBox).not.toBeNull()
  expect(desktopSkipLinkBox!.y).toBeGreaterThanOrEqual(0)
  expect(desktopSkipLinkBox!.y + desktopSkipLinkBox!.height).toBeLessThan(76)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(1280)
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
  await expect(page.locator(':focus')).toHaveAttribute('href', '/')
  await page.keyboard.press('Tab')
  await expect(page.locator(':focus')).toHaveAttribute('href', '/')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/$/)
  await signInWithSyntheticEmail(page, page.request, 'home-hash-reader@example.test', 'Home Hash Reader')
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

test('holds the Home shell neutral until an authenticated session resolves', async ({ page, request }) => {
  await signInWithSyntheticEmail(page, request, 'home-session-flash@example.test', 'Home Reader')
  let release!: () => void
  const blocked = new Promise<void>((resolve) => { release = resolve })
  await page.route('**/api/auth/me', async (route) => {
    await blocked
    await route.continue()
  })
  try {
    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByText('Checking your session…')).toBeVisible()
    await expect(page.getByRole('heading', { name: /A little more/ })).toHaveCount(0)
    release()
    await expect(page.getByRole('heading', { name: /Welcome back, Home Reader/ })).toBeVisible()
  } finally {
    release()
    await page.unroute('**/api/auth/me')
  }
})

test('serves direct account and admin route aliases with private CSP and anonymous guards', async ({ page }) => {
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  const routes = [
    { route: '/account', title: /Account/ },
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

test('serves Browse aliases with private headers and route-backed type filters', async ({ page }) => {
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  const policy = policies.find((item) => item.route === '/discover')?.policy
  expect(policy).toBeTruthy()
  await page.route('**/api/auth/me', (route) => route.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"unauthorized"}' }))
  await page.route('**/api/public/config', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '{"auth_methods":{"email_link":true,"passkey":false},"registration_policy":"invite_only","public_instance":true,"courses_enabled":true,"anonymous_home_preset":"library_first"}' }))

  for (const path of ['/discover', '/discover.html']) {
    const response = await page.goto(`${path}?type=cookbooks&keep=query`)
    expect(response?.status()).toBe(200)
    expect(response?.headers()['cache-control']).toBe('no-store')
    expect(response?.headers()['referrer-policy']).toBe('no-referrer')
    expect(response?.headers()['content-security-policy']).toBe(policy)
    await expect(page).toHaveTitle(/Browse/)
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', 'noindex,nofollow')
    await expect(page.getByRole('heading', { name: 'Browse', level: 1 })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Cookbooks' })).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByRole('button', { name: 'Courses' })).toBeVisible()
    await expect(page.getByRole('button', { name: /create/i })).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Administration' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Recipes' }).click()
    await expect(page).toHaveURL(/\/discover\?type=recipes(?:&|$)/)
    await expect(page.getByRole('button', { name: 'Recipes' })).toHaveAttribute('aria-pressed', 'true')
  }
})

test('serves Create aliases with private headers and gates creation on sign-in', async ({ page }) => {
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  const policy = policies.find((item) => item.route === '/create')?.policy
  expect(policy).toBeTruthy()
  await page.route('**/api/auth/me', (route) => route.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"unauthorized"}' }))
  for (const path of ['/create', '/create.html']) {
    const response = await page.goto(`${path}?source=direct`)
    expect(response?.status()).toBe(200)
    expect(response?.headers()['cache-control']).toBe('no-store')
    expect(response?.headers()['referrer-policy']).toBe('no-referrer')
    expect(response?.headers()['content-security-policy']).toBe(policy)
    await expect(page).toHaveTitle(/Create/)
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', 'noindex,nofollow')
    await expect(page.getByRole('heading', { name: 'Create', level: 1 })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Sign in to create' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Recipe' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Cookbook' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Course' })).toHaveCount(0)
  }
})

test('shows course creation only when the public configuration enables the feature', async ({ page, request }) => {
  await signInWithSyntheticEmail(page, request, 'course-feature@example.test', 'Course Feature')
  await page.route('**/api/public/config', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: '{"auth_methods":{"email_link":true,"passkey":false},"registration_policy":"invite_only","public_instance":false,"courses_enabled":true,"anonymous_home_preset":"library_first"}' }))
  await page.goto('/create')
  const courseButton = page.getByRole('button', { name: 'Course', exact: true })
  await expect(courseButton).toBeVisible()
  await courseButton.click()
  await expect(page.getByRole('heading', { name: 'Course details', level: 2 })).toBeVisible()
  const courseDialog = page.getByRole('dialog', { name: 'Create a course' })
  await expect(courseDialog.getByRole('heading', { name: 'Create a course', level: 2 })).toBeVisible()
  await expect(page.getByLabel('Visibility')).toHaveValue('private')
})

test('signed-in account users can open Account Security and cannot open instance settings', async ({ page, request }) => {
  const user = await signInWithSyntheticEmail(page, request, 'route-user@example.test', 'Route User')
  expect(user.is_admin).toBe(false)

  for (const path of ['/account/security', '/account/security.html']) {
    await page.goto(path)
    await expect(page.getByRole('heading', { name: 'Account security', level: 1 })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Browser sessions' })).toBeVisible()
    await openAccountMenu(page)
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
  const user = await signInWithSyntheticEmail(page, request, 'admin-static-host@example.test', 'E2E Admin')
  expect(user.is_admin).toBe(true)

  const initialSettingsResponse = await page.request.get('/api/admin/settings')
  expect(initialSettingsResponse.status()).toBe(200)
  const initialSettings = await initialSettingsResponse.json() as {
    public_instance: boolean
    anonymous_home_preset?: string
    site_name?: string
  }
  const originalPreset = initialSettings.anonymous_home_preset === 'discovery_first'
    ? 'discovery_first'
    : 'library_first'
  const originalSiteName = initialSettings.site_name ?? 'Cookbook'
  let guestContext: BrowserContext | undefined
  let primaryFailure: unknown
  let primaryFailed = false
  const cleanupFailures: string[] = []

  try {
    for (const path of ['/admin/settings', '/admin/settings.html']) {
      const response = await page.goto(path)
      expect(response?.status()).toBe(200)
      expect(response?.headers()['cache-control']).toBe('no-store')
      await expect(page).toHaveTitle(/Instance settings/)
      await expect(page.getByRole('heading', { name: 'Instance administration', level: 1 })).toBeVisible()
      await expect(page.getByText('Enable public browsing')).toBeVisible()
      await openAccountMenu(page)
      await expect(page.getByRole('link', { name: 'Instance', exact: true })).toBeVisible()
      await expect(page.getByRole('link', { name: 'Administration' })).toHaveCount(0)
    }

    const siteName = page.getByRole('textbox', { name: 'Instance name in navigation' })
    await siteName.fill('E2E Kitchen')
    await page.getByRole('button', { name: 'Save instance settings' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Instance settings saved.' })).toBeVisible()
    const namedSettings = await page.request.get('/api/admin/settings')
    expect((await namedSettings.json()).site_name).toBe('E2E Kitchen')
    await page.goto('/discover')
    const headerBrand = page.getByRole('link', { name: 'Cookbook home' })
    const footerBrand = page.getByRole('contentinfo').getByRole('link', { name: 'Cookbook', exact: true })
    await expect(headerBrand).toContainText('E2E Kitchen')
    await expect(footerBrand).toHaveText('Cookbook')

    await page.goto('/admin/settings')
    await page.getByRole('button', { name: 'Reset site name' }).click()
    await page.getByRole('button', { name: 'Save instance settings' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Instance settings saved.' })).toBeVisible()
    const resetSettings = await page.request.get('/api/admin/settings')
    expect((await resetSettings.json()).site_name).toBe('Cookbook')

    const homePreset = page.getByRole('combobox', { name: /homepage presentation/i })
    await homePreset.selectOption('discovery_first')
    await page.getByRole('button', { name: 'Save instance settings' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Instance settings saved.' })).toBeVisible()
    const savedSettings = await page.request.get('/api/admin/settings')
    expect((await savedSettings.json()).anonymous_home_preset).toBe('discovery_first')

    const privateSettings = await page.request.patch('/api/admin/settings', {
      headers: { Origin: process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:4178' },
      data: { public_instance: false },
    })
    expect(privateSettings.status()).toBe(200)
    guestContext = await page.context().browser()!.newContext()
    const guest = await guestContext.newPage()
    const privateCatalogRequests: string[] = []
    guest.on('request', (browserRequest) => {
      const pathname = new URL(browserRequest.url()).pathname
      if (/^\/api\/(?:public\/)?(?:recipes|remote-recipes|cookbooks|courses)(?:\/|$)/.test(pathname))
        privateCatalogRequests.push(pathname)
    })
    await guest.goto('/')
    await expect(guest.getByText(/this instance keeps its recipes private/i)).toBeVisible()
    await expect(guest.getByRole('button', { name: /sign in or create an account/i })).toBeVisible()
    await expect(guest.getByRole('link', { name: 'Browse recipes' })).toHaveCount(0)
    expect(privateCatalogRequests).toEqual([])

    const publicSettings = await page.request.patch('/api/admin/settings', {
      headers: { Origin: process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:4178' },
      data: { public_instance: true, confirm_publication: true },
    })
    expect(publicSettings.status()).toBe(200)
    const publicCatalogRequests: string[] = []
    guest.on('request', (browserRequest) => {
      const pathname = new URL(browserRequest.url()).pathname
      if (/^\/api\/public\/(?:recipes|cookbooks|remote-recipes)(?:\/|$)/.test(pathname))
        publicCatalogRequests.push(pathname)
    })
    await guest.reload()
    await expect(guest.getByRole('heading', { name: /find something good to make/i })).toBeVisible()
    await expect(guest.getByRole('link', { name: 'Browse recipes' })).toHaveAttribute('href', '/discover')
    await expect.poll(() => publicCatalogRequests.length).toBeGreaterThan(0)

    await page.goto('/admin')
    await expect(page.getByRole('heading', { name: 'People and permissions', level: 1 })).toBeVisible()
    await page.getByRole('link', { name: 'Settings', exact: true }).click()
    await expect(page).toHaveURL(/\/admin\/settings#visibility$/)
    await expect(page.getByRole('heading', { name: 'Instance administration', level: 1 })).toBeVisible()
  } catch (error) {
    primaryFailed = true
    primaryFailure = error
  }

  await attemptCleanup(cleanupFailures, 'Restore instance settings', async () => {
    const response = await page.request.patch('/api/admin/settings', {
      headers: { Origin: process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:4178' },
      data: {
        public_instance: initialSettings.public_instance,
        anonymous_home_preset: originalPreset,
        site_name: originalSiteName,
        confirm_publication: true,
      },
    })
    expect(response.status()).toBe(200)
  })
  if (guestContext) {
    await attemptCleanup(cleanupFailures, 'Close guest browser context', () => guestContext!.close())
  }

  if (primaryFailed) {
    if (cleanupFailures.length) {
      const cleanupMessage = `\nCleanup also reported: ${cleanupFailures.join('; ')}`
      if (primaryFailure instanceof Error) {
        primaryFailure.message += cleanupMessage
        throw primaryFailure
      }
      throw new Error(`Primary test failure: ${String(primaryFailure)}${cleanupMessage}`, { cause: primaryFailure })
    }
    throw primaryFailure
  }
  if (cleanupFailures.length) throw new Error(cleanupFailures.join('; '))
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
  let consumeRequestFailure: string | null = null
  let consumeResponseStatus: number | null = null
  page.on('request', (browserRequest) => {
    const requestURL = new URL(browserRequest.url())
    if (requestURL.origin === origin && requestURL.pathname === '/api/auth/consume' && browserRequest.method() === 'POST') consumePosts += 1
  })
  page.on('requestfailed', (browserRequest) => {
    const requestURL = new URL(browserRequest.url())
    if (requestURL.origin === origin && requestURL.pathname === '/api/auth/consume' && browserRequest.method() === 'POST') {
      consumeRequestFailure = browserRequest.failure()?.errorText ?? 'unknown request failure'
    }
  })
  page.on('response', (browserResponse) => {
    const responseURL = new URL(browserResponse.url())
    if (responseURL.origin === origin && responseURL.pathname === '/api/auth/consume' && browserResponse.request().method() === 'POST') {
      consumeResponseStatus = browserResponse.status()
    }
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
    const consumeResponsePromise = page.waitForResponse((browserResponse) => {
      const responseURL = new URL(browserResponse.url())
      return responseURL.origin === origin && responseURL.pathname === '/api/auth/consume' && browserResponse.request().method() === 'POST'
    }, { timeout: 10_000 })
    await page.getByRole('button', { name: /continue to cookbook/i }).click()
    let consumeStatus: number
    try {
      consumeStatus = (await consumeResponsePromise).status()
    } catch {
      const sessionResponse = await page.request.get('/api/auth/me')
      const observation = {
        phase: 'session establishment after explicit Continue',
        request: { method: 'POST', path: '/api/auth/consume', observed: consumePosts > 0, failure: consumeRequestFailure },
        responseStatus: consumeResponseStatus,
        sessionStatus: sessionResponse.status(),
        tokenRemains: new URL(page.url()).searchParams.has('token'),
      }
      throw new Error(`No same-origin consume response was observed: ${JSON.stringify(observation)}`)
    }
    expect(consumeStatus, 'explicit Continue consumes the sign-in challenge').toBe(200)
    await expect.poll(async () => (await page.request.get('/api/auth/me')).status()).toBe(200)
    expect(consumePosts).toBe(1)
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
