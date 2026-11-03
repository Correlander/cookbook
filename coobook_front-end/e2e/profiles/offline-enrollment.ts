import { expect, test } from '@playwright/test'

const enrollmentGrantPattern = /^[0-9a-f]{32}\.[0-9a-f]{64}$/

test('enrolls the first passkey from an offline grant and signs in with it', async ({ page }) => {
  const grant = process.env.COOKBOOK_OFFLINE_ENROLLMENT_GRANT
  if (!grant || !enrollmentGrantPattern.test(grant)) {
    throw new Error('the isolated offline enrollment profile requires a valid private grant')
  }

  const cdp = await page.context().newCDPSession(page)
  let authenticatorId: string | undefined
  try {
    await cdp.send('WebAuthn.enable')
    const authenticator = await cdp.send('WebAuthn.addVirtualAuthenticator', {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    })
    authenticatorId = authenticator.authenticatorId as string

    const anonymousBefore = await page.request.get('/api/auth/me')
    expect(anonymousBefore.status()).toBe(401)

    await page.goto('/auth/enroll')
    const firstBegin = page.waitForResponse((response) =>
      new URL(response.url()).pathname === '/api/auth/passkey/enrollment/begin',
    )
    const firstFinish = page.waitForResponse((response) =>
      new URL(response.url()).pathname === '/api/auth/passkey/enrollment/finish',
    )
    await page.getByLabel('One-time enrollment code').fill(grant)
    await page.getByRole('button', { name: 'Create passkey' }).click()
    const [beginResponse, finishResponse] = await Promise.all([firstBegin, firstFinish])
    expect(beginResponse.status()).toBe(200)
    expect(finishResponse.status()).toBe(201)
    await expect(page.getByRole('heading', { name: 'Your passkey is ready' })).toBeVisible()
    await expect(page.getByRole('status')).toHaveText('Passkey setup is complete. You can now sign in with this passkey.')

    const anonymousAfter = await page.request.get('/api/auth/me')
    expect(anonymousAfter.status()).toBe(401)

    await page.goto('/auth/enroll')
    await page.getByLabel('One-time enrollment code').fill(grant)
    await page.getByRole('button', { name: 'Create passkey' }).click()
    await expect(page.getByRole('alert')).toHaveText(
      'That enrollment code could not be used. It may be invalid, expired, or already used. Ask your Cookbook operator for a new code and try again.',
    )

    const anonymousAfterReuse = await page.request.get('/api/auth/me')
    expect(anonymousAfterReuse.status()).toBe(401)

    await page.goto('/')
    await page.getByRole('button', { name: 'Sign in', exact: true }).click()
    const loginFinish = page.waitForResponse((response) =>
      new URL(response.url()).pathname === '/api/auth/passkey/login/finish',
    )
    await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
    expect((await loginFinish).status()).toBe(200)

    const authenticated = await page.request.get('/api/auth/me')
    expect(authenticated.status()).toBe(200)
    const user = await authenticated.json() as { email?: string; is_admin?: boolean }
    expect(user.email).toBe('offline-browser@example.test')
    expect(user.is_admin).toBe(false)
  } finally {
    if (authenticatorId) {
      await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId }).catch(() => {})
    }
    await cdp.send('WebAuthn.disable').catch(() => {})
    await cdp.detach().catch(() => {})
  }
})
