import { expect, test } from '@playwright/test'

function contrastRatio(first: readonly number[], second: readonly number[]): number {
  const luminance = (color: readonly number[]) => {
    const channels = color.slice(0, 3).map((channel) => {
      const value = channel / 255
      return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
    })
    return 0.2126 * channels[0]! + 0.7152 * channels[1]! + 0.0722 * channels[2]!
  }
  const values = [luminance(first), luminance(second)].sort((left, right) => right - left)
  return (values[0]! + 0.05) / (values[1]! + 0.05)
}

async function tabTo(page: import('@playwright/test').Page, target: import('@playwright/test').Locator): Promise<void> {
  for (let step = 0; step < 30; step++) {
    if (await target.evaluate((element) => element === document.activeElement)) return
    await page.keyboard.press('Tab')
  }
  await expect(target).toBeFocused()
}

const publicConfig = (palette: unknown) => ({
  auth_methods: { email_link: true, passkey: false },
  registration_policy: 'invite_only',
  public_instance: true,
  courses_enabled: false,
  anonymous_home_preset: 'library_first',
  branding: { site_name: 'Palette Kitchen', palette },
})

test('static document begins with garden colors before the shared config request resolves', async ({ page }) => {
  await page.route('**/api/public/config', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 250))
    await route.fulfill({ json: publicConfig('plum') })
  })
  const staticResponse = await page.request.get('/')
  const staticHTML = await staticResponse.text()
  expect(staticHTML).not.toMatch(/<html[^>]*data-palette=/i)

  await page.goto('/')
  expect(await page.locator('html').getAttribute('data-palette')).toBeNull()
  expect(await page.locator('html').evaluate((root) => getComputedStyle(root).getPropertyValue('--green').trim())).toBe('#214b3c')
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'plum')
  await expect(page.locator('html')).toHaveCSS('--green', '#56375f')
})

test('finite palette applies on shared routes and keeps fixed focus and product colors', async ({ page }) => {
  await page.route('**/api/public/config', (route) => route.fulfill({ json: publicConfig('plum') }))

  for (const path of ['/', '/discover', '/library', '/create']) {
    await page.goto(path)
    await expect(page.locator('html'), path).toHaveAttribute('data-palette', 'plum')
    await expect(page.locator('html'), path).toHaveCSS('--green', '#56375f')
    await expect(page.getByRole('contentinfo').getByRole('link', { name: 'Cookbook', exact: true })).toHaveText('Cookbook')
  }

  await page.setViewportSize({ width: 320, height: 720 })
  await page.goto('/')
  await page.route('**/api/auth/email', (route) => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.getByRole('button', { name: /sign in or create an account/i }).click()
  const signinSubmit = page.getByRole('button', { name: 'Send sign-in link' })
  await expect(signinSubmit).toBeVisible()
  await signinSubmit.hover()
  await expect(signinSubmit).toHaveCSS('background-color', 'rgb(53, 37, 59)')
  await page.getByRole('textbox', { name: 'Email address' }).fill('palette@example.test')
  await signinSubmit.click()
  const statusMessage = page.getByRole('status').filter({ hasText: /could not|unable|try again|email/i }).last()
  await expect(statusMessage).toBeVisible()
  const semanticMessageStyle = await statusMessage.evaluate((element) => {
    const style = getComputedStyle(element)
    return { color: style.color, backgroundColor: style.backgroundColor }
  })
  expect(semanticMessageStyle).toEqual({ color: 'rgb(33, 75, 60)', backgroundColor: 'rgb(231, 234, 223)' })

  await page.goto('/')
  await page.keyboard.press('Tab')
  const skipLink = page.locator('.skip-link')
  await expect(skipLink).toBeFocused()
  await expect(skipLink).toBeInViewport()
  await page.setViewportSize({ width: 640, height: 900 })
  const cdp = await page.context().newCDPSession(page)
  try {
    await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 2 })
    const scaledFocus = await skipLink.evaluate((element) => {
      const rect = element.getBoundingClientRect()
      const viewport = window.visualViewport
      if (!viewport) return null
      const style = getComputedStyle(element)
      return {
        scale: viewport.scale,
        width: viewport.width,
        height: viewport.height,
        left: rect.left,
        top: rect.top,
        right: rect.right,
        bottom: rect.bottom,
        outlineWidth: style.outlineWidth,
        outlineOffset: style.outlineOffset,
        boxShadow: style.boxShadow,
      }
    })
    expect(scaledFocus).not.toBeNull()
    expect(scaledFocus!.scale).toBe(2)
    expect(scaledFocus!.width).toBeGreaterThan(0)
    expect(scaledFocus!.left).toBeGreaterThanOrEqual(0)
    expect(scaledFocus!.right).toBeLessThanOrEqual(scaledFocus!.width)
    expect(scaledFocus!.top).toBeGreaterThanOrEqual(0)
    expect(scaledFocus!.bottom).toBeLessThanOrEqual(scaledFocus!.height)
    expect(scaledFocus!.outlineWidth).toBe('3px')
    expect(scaledFocus!.outlineOffset).toBe('3px')
    expect(scaledFocus!.boxShadow).toBe('rgb(255, 255, 255) 0px 0px 0px 3px')
  } finally {
    await cdp.send('Emulation.resetPageScaleFactor')
    await cdp.detach()
  }

  await page.setViewportSize({ width: 320, height: 720 })
  await page.keyboard.press('Enter')
  await expect(page.locator('#home')).toBeFocused()
  const focusedControl = page.getByRole('button', { name: /sign in or create an account/i })
  let actionReachedByKeyboard = false
  for (let step = 0; step < 30; step++) {
    await page.keyboard.press('Tab')
    if (await focusedControl.evaluate((element) => element === document.activeElement)) {
      actionReachedByKeyboard = true
      break
    }
  }
  expect(actionReachedByKeyboard).toBe(true)
  await expect(focusedControl).toBeInViewport()
  const focusStyle = await focusedControl.evaluate((element) => {
    const style = getComputedStyle(element)
    return { outlineWidth: style.outlineWidth, outlineStyle: style.outlineStyle, outlineColor: style.outlineColor, outlineOffset: style.outlineOffset, boxShadow: style.boxShadow }
  })
  expect(focusStyle).toEqual({
    outlineWidth: '3px',
    outlineStyle: 'solid',
    outlineColor: 'rgb(0, 0, 0)',
    outlineOffset: '3px',
    boxShadow: 'rgb(255, 255, 255) 0px 0px 0px 3px',
  })
  const focusedBounds = await focusedControl.boundingBox()
  expect(focusedBounds).not.toBeNull()
  expect(focusedBounds!.x).toBeGreaterThanOrEqual(6)
  expect(focusedBounds!.x + focusedBounds!.width).toBeLessThanOrEqual(314)
  expect(focusedBounds!.y).toBeGreaterThanOrEqual(6)
  expect(focusedBounds!.y + focusedBounds!.height).toBeLessThanOrEqual(714)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(320)

  await page.unroute('**/api/public/config')
  await page.route('**/api/public/config', (route) => route.fulfill({ json: publicConfig('untrusted-selector') }))
  await page.goto('/discover')
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'garden')
  await expect(page.locator('html')).toHaveCSS('--green', '#214b3c')
})

test('keyboard focus reaches search, active and selected controls, and passkey enrollment', async ({ page }) => {
  await page.route('**/api/public/config', (route) => route.fulfill({ json: publicConfig('plum') }))
  await page.setViewportSize({ width: 1280, height: 900 })

  await page.goto('/discover')
  const search = page.getByRole('textbox', { name: 'Search recipes' })
  await tabTo(page, search)
  await expect(search).toBeVisible()
  await expect(search).toBeInViewport()
  await expect(search).toHaveCSS('outline-width', '3px')
  await expect(search).toHaveCSS('outline-color', 'rgb(0, 0, 0)')
  await expect(search).toHaveCSS('outline-offset', '3px')
  await expect(search).toHaveCSS('box-shadow', 'rgb(255, 255, 255) 0px 0px 0px 3px')

  await page.goto('/discover')
  const activeBrowse = page.getByRole('link', { name: 'Browse', exact: true })
  await expect(activeBrowse).toHaveAttribute('aria-current', 'page')
  await tabTo(page, activeBrowse)
  await expect(activeBrowse).toBeVisible()
  const activeStyle = await activeBrowse.evaluate((element) => {
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    return { outlineWidth: style.outlineWidth, outlineColor: style.outlineColor, outlineOffset: style.outlineOffset, boxShadow: style.boxShadow, left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom }
  })
  expect(activeStyle).toMatchObject({ outlineWidth: '3px', outlineColor: 'rgb(0, 0, 0)', outlineOffset: '3px', boxShadow: 'rgb(255, 255, 255) 0px 0px 0px 3px' })
  const activeScreenshot = await page.screenshot({ type: 'png' })
  const edgePoints = [
    { x: Math.floor((activeStyle.left + activeStyle.right) / 2), y: Math.floor(activeStyle.bottom + 1) },
    { x: Math.floor((activeStyle.left + activeStyle.right) / 2), y: Math.floor(activeStyle.bottom + 4) },
    { x: Math.floor((activeStyle.left + activeStyle.right) / 2), y: Math.floor(activeStyle.bottom + 8) },
  ]
  const edgePixels = await page.evaluate(async ({ encodedImage, points }) => {
    const binary = atob(encodedImage)
    const imageBytes = Uint8Array.from(binary, (character) => character.charCodeAt(0))
    const bitmap = await createImageBitmap(new Blob([imageBytes], { type: 'image/png' }))
    const canvas = document.createElement('canvas')
    canvas.width = bitmap.width
    canvas.height = bitmap.height
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Could not read the rendered focus screenshot.')
    context.drawImage(bitmap, 0, 0)
    const pixels = points.map(({ x, y }) => Array.from(context.getImageData(x, y, 1, 1).data))
    bitmap.close()
    return pixels
  }, { encodedImage: activeScreenshot.toString('base64'), points: edgePoints })
  expect(edgePixels[0]).toEqual([255, 255, 255, 255])
  expect(edgePixels[1]?.slice(0, 3)).toEqual([0, 0, 0])
  const innerOuterContrast = contrastRatio(edgePixels[0]!, edgePixels[1]!)
  const outerBackdropContrast = contrastRatio(edgePixels[1]!, edgePixels[2]!)
  expect(innerOuterContrast).toBe(21)
  expect(outerBackdropContrast).toBeGreaterThanOrEqual(3)
  test.info().annotations.push({
    type: 'metric',
    description: `active Browse ring pixels white ${edgePixels[0]!.slice(0, 3).join(',')} vs black ${edgePixels[1]!.slice(0, 3).join(',')}: ${innerOuterContrast.toFixed(6)}:1; black vs adjacent rendered backdrop ${edgePixels[2]!.slice(0, 3).join(',')}: ${outerBackdropContrast.toFixed(6)}:1`,
  })

  await page.goto('/discover')
  const selectedCookbooks = page.getByRole('group', { name: 'Browse type' }).getByRole('button', { name: 'Cookbooks' })
  await selectedCookbooks.click()
  await expect(selectedCookbooks).toHaveAttribute('aria-pressed', 'true')
  await page.goto('/discover?type=cookbooks')
  const selectedControl = page.getByRole('group', { name: 'Browse type' }).getByRole('button', { name: 'Cookbooks' })
  await expect(selectedControl).toHaveAttribute('aria-pressed', 'true')
  await tabTo(page, selectedControl)
  await expect(selectedControl).toHaveCSS('outline-width', '3px')
  await expect(selectedControl).toHaveCSS('outline-offset', '3px')
  await expect(selectedControl).toHaveCSS('box-shadow', 'rgb(255, 255, 255) 0px 0px 0px 3px')

  await page.goto('/auth/enroll')
  const enrollmentCode = page.getByLabel('One-time enrollment code')
  await tabTo(page, enrollmentCode)
  await expect(enrollmentCode).toBeVisible()
  await expect(enrollmentCode).toBeInViewport()
  await expect(enrollmentCode).toHaveCSS('outline-width', '3px')
  await expect(enrollmentCode).toHaveCSS('outline-color', 'rgb(0, 0, 0)')
  await expect(enrollmentCode).toHaveCSS('outline-offset', '3px')
  await expect(enrollmentCode).toHaveCSS('box-shadow', 'rgb(255, 255, 255) 0px 0px 0px 3px')
})

test('config errors and malformed public configuration fall back to garden', async ({ page }) => {
  await page.route('**/api/public/config', (route) => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.goto('/')
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'garden')

  await page.unroute('**/api/public/config')
  await page.route('**/api/public/config', (route) => route.fulfill({ json: { auth_methods: {}, registration_policy: 'unknown' } }))
  await page.goto('/discover')
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'garden')
  await expect(page.locator('html')).toHaveCSS('--green', '#214b3c')
})
