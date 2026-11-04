import { expect, test, type BrowserContext, type Locator, type Page } from '@playwright/test'

const grantPattern = /^[0-9a-f]{32}\.[0-9a-f]{64}$/
const ownerGrantKey = 'COOKBOOK_OWNER_WORKBENCH_OWNER_GRANT'
const recipientGrantKey = 'COOKBOOK_OWNER_WORKBENCH_RECIPIENT_GRANT'
const ownerEmail = 'workbench-owner@example.test'
const recipientEmail = 'workbench-recipient@example.test'
const disposableRunMarker = 'isolated-owner-workbench-v1'

function ownerWorkbenchBaseURL(): string {
  if (process.env.COOKBOOK_OWNER_WORKBENCH_DISPOSABLE_RUN !== disposableRunMarker) {
    throw new Error('owner-workbench requires the isolated disposable harness marker')
  }
  const value = process.env.PLAYWRIGHT_BASE_URL
  if (!value) throw new Error('owner-workbench requires an explicit PLAYWRIGHT_BASE_URL')
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error('owner-workbench PLAYWRIGHT_BASE_URL is invalid')
  }
  if (
    url.protocol !== 'http:' ||
    !['localhost', '127.0.0.1', '[::1]', '::1'].includes(url.hostname) ||
    !url.port ||
    url.port === '8000' ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error('owner-workbench requires a non-demo loopback disposable origin')
  }
  return url.origin
}

type AuthenticatorLease = {
  authenticatorId: string
  cdp: Awaited<ReturnType<BrowserContext['newCDPSession']>>
}

async function attachAuthenticator(context: BrowserContext, page: Page): Promise<AuthenticatorLease> {
  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })
  await cdp.send('WebAuthn.setAutomaticPresenceSimulation', { authenticatorId, enabled: true })
  await cdp.send('WebAuthn.setUserVerified', { authenticatorId, isUserVerified: true })
  return { authenticatorId: authenticatorId as string, cdp }
}

async function removeAuthenticator(lease: AuthenticatorLease | undefined): Promise<void> {
  if (!lease) return
  await lease.cdp.send('WebAuthn.removeVirtualAuthenticator', {
    authenticatorId: lease.authenticatorId,
  }).catch(() => {})
  await lease.cdp.send('WebAuthn.disable').catch(() => {})
  await lease.cdp.detach().catch(() => {})
}

async function enroll(page: Page, grant: string): Promise<void> {
  await page.goto('/auth/enroll')
  const begin = page.waitForResponse((response) =>
    new URL(response.url()).pathname === '/api/auth/passkey/enrollment/begin',
  )
  const finish = page.waitForResponse((response) =>
    new URL(response.url()).pathname === '/api/auth/passkey/enrollment/finish',
  )
  await page.getByLabel('One-time enrollment code').fill(grant)
  await page.getByRole('button', { name: 'Create passkey' }).click()
  const [beginResponse, finishResponse] = await Promise.all([begin, finish])
  expect(beginResponse.status()).toBe(200)
  expect(finishResponse.status()).toBe(201)
  await expect(page.getByRole('heading', { name: 'Your passkey is ready' })).toBeVisible()
}

async function signInWithEnrolledPasskey(page: Page, expectedEmail: string): Promise<string> {
  await page.goto('/')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  const finish = page.waitForResponse((response) =>
    new URL(response.url()).pathname === '/api/auth/passkey/login/finish',
  )
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
  expect((await finish).status()).toBe(200)
  const response = await page.request.get('/api/auth/me')
  expect(response.status()).toBe(200)
  const user = await response.json() as { id: string; email: string; is_admin: boolean }
  expect(user.email).toBe(expectedEmail)
  expect(user.is_admin).toBe(false)
  return user.id
}

async function acceptConfirmation(page: Page, button: Locator, text: string) {
  const dialogEvent = page.waitForEvent('dialog')
  const click = button.click()
  const dialog = await dialogEvent
  expect(dialog.type()).toBe('confirm')
  expect(dialog.message()).toContain(text)
  await dialog.accept()
  await click
}

type SettingsSnapshot = {
  public_instance: boolean
  courses_enabled: boolean
  registration_policy: string
  revision_retention_days: number
  audit_retention_days: number
  federation_enabled: boolean
  federation_origin: string
}

async function readSettingsSnapshot(page: Page): Promise<SettingsSnapshot> {
  const beforeResponse = await page.request.get('/api/admin/settings')
  expect(beforeResponse.status()).toBe(200)
  const settings = await beforeResponse.json() as SettingsSnapshot
  return {
    public_instance: settings.public_instance,
    courses_enabled: settings.courses_enabled,
    registration_policy: settings.registration_policy,
    revision_retention_days: settings.revision_retention_days,
    audit_retention_days: settings.audit_retention_days,
    federation_enabled: settings.federation_enabled,
    federation_origin: settings.federation_origin,
  }
}

async function patchCoursesEnabled(page: Page, enabled: boolean): Promise<number> {
  return page.evaluate(async (coursesEnabled) => {
    const response = await fetch('/api/admin/settings', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ courses_enabled: coursesEnabled }),
    })
    return response.status
  }, enabled)
}

async function expectSettingsReadWrite(page: Page): Promise<void> {
  const before = await readSettingsSnapshot(page)
  const changedCoursesEnabled = !before.courses_enabled

  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { name: 'Instance administration' })).toBeVisible()
  await expect(page.getByText('Enable public browsing')).toBeVisible()
  await expect(page.getByLabel('Enable courses')).toBeChecked({ checked: before.courses_enabled })
  let writeAttempted = false
  let operationError: unknown
  let restorationError: Error | undefined
  try {
    writeAttempted = true
    expect(await patchCoursesEnabled(page, changedCoursesEnabled)).toBe(200)
    expect(await readSettingsSnapshot(page)).toEqual({ ...before, courses_enabled: changedCoursesEnabled })
    await page.reload()
    await expect(page.getByLabel('Enable courses')).toBeChecked({ checked: changedCoursesEnabled })
  } catch (error) {
    operationError = error
  } finally {
    if (writeAttempted) {
      try {
        const restoreStatus = await patchCoursesEnabled(page, before.courses_enabled)
        if (restoreStatus !== 200) {
          restorationError = new Error(`restore returned HTTP ${restoreStatus}`)
        } else {
          const restored = await readSettingsSnapshot(page)
          if (JSON.stringify(restored) !== JSON.stringify(before)) {
            restorationError = new Error('authorized read returned a different settings snapshot')
          } else {
            await page.reload()
            await expect(page.getByLabel('Enable courses')).toBeChecked({ checked: before.courses_enabled })
          }
        }
      } catch (error) {
        restorationError = error instanceof Error ? error : new Error('unknown restoration error')
      }
    }
  }
  if (restorationError) {
    throw new AggregateError(
      operationError === undefined ? [restorationError] : [operationError, restorationError],
      'settings exercise failed to restore the disposable courses_enabled value',
    )
  }
  if (operationError !== undefined) throw operationError
}

async function expectSettingsDenied(page: Page): Promise<void> {
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { name: 'Instance settings access required' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save instance settings' })).toHaveCount(0)
  const settingsRead = await page.request.get('/api/admin/settings')
  expect(settingsRead.status()).toBe(403)
}

async function expectSettingsWriteDenied(page: Page, observerPage: Page): Promise<void> {
  const before = await readSettingsSnapshot(observerPage)
  const response = await page.evaluate(async () => fetch('/api/admin/settings', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      public_instance: false,
      courses_enabled: false,
      registration_policy: 'disabled',
      revision_retention_days: 365,
      federation_enabled: false,
    }),
  }))
  expect(response.status).toBe(403)
  expect(await readSettingsSnapshot(observerPage)).toEqual(before)
}

test('ordinary owner transfers ownership to an ordinary recipient through the live workbench', async ({ page, browser }) => {
  const baseURL = ownerWorkbenchBaseURL()
  const grants: { owner?: string; recipient?: string } = {
    owner: process.env[ownerGrantKey],
    recipient: process.env[recipientGrantKey],
  }
  let ownerLease: AuthenticatorLease | undefined
  let recipientLease: AuthenticatorLease | undefined
  let recipientContext: BrowserContext | undefined

  try {
    expect(grantPattern.test(grants.owner ?? '')).toBe(true)
    expect(grantPattern.test(grants.recipient ?? '')).toBe(true)

    ownerLease = await attachAuthenticator(page.context(), page)
    await enroll(page, grants.owner!)
    grants.owner = undefined
    delete process.env[ownerGrantKey]
    const ownerID = await signInWithEnrolledPasskey(page, ownerEmail)
    const initialOwnerAuthority = await page.request.get('/api/instance/authority')
    expect(initialOwnerAuthority.status()).toBe(200)
    expect(await initialOwnerAuthority.json()).toMatchObject({
      is_instance_owner: true,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: true,
      can_manage_instance_settings: true,
    })
    expect((await page.request.get('/api/admin/users')).status()).toBe(200)

    await page.goto('/admin')
    await expect(page.getByRole('heading', { name: 'Instance access' })).toBeVisible()
    const initialSettingsLink = page.getByRole('link', { name: 'Open Instance Settings' })
    await expect(initialSettingsLink).toBeVisible()
    await initialSettingsLink.click()
    await expect(page).toHaveURL(/\/admin\/settings$/)
    await expectSettingsReadWrite(page)

    await page.goto('/admin')
    await expect(page.getByRole('heading', { name: 'Instance access' })).toBeVisible()
    const transferTargetButton = page.getByRole('button', { name: 'Transfer ownership to Workbench Recipient' })
    await expect(transferTargetButton).toBeVisible()
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname === '/api/instance/owner-transfers',
    )
    await acceptConfirmation(
      page,
      transferTargetButton,
      'Workbench Recipient',
    )
    const createResponse = await createResponsePromise
    expect(createResponse.status()).toBe(201)
    await expect(page.getByText(/Ownership request created/)).toBeVisible()
    await expect(page.getByText('Request sent to Workbench Recipient')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Cancel request', exact: true })).toBeVisible()

    const outgoingResponse = await page.request.get('/api/instance/owner-transfers')
    expect(outgoingResponse.status()).toBe(200)
    const outgoingBody = await outgoingResponse.json() as {
      transfers: Array<{ direction: string; counterparty_display_name: string; id: string }>
    }
    expect(outgoingBody.transfers).toHaveLength(1)
    expect(outgoingBody.transfers[0]).toMatchObject({
      direction: 'outgoing',
      counterparty_display_name: 'Workbench Recipient',
    })
    const transferID = outgoingBody.transfers[0].id

    recipientContext = await browser.newContext({
      baseURL,
    })
    const recipientPage = await recipientContext.newPage()
    recipientLease = await attachAuthenticator(recipientContext, recipientPage)
    await enroll(recipientPage, grants.recipient!)
    grants.recipient = undefined
    delete process.env[recipientGrantKey]
    const recipientID = await signInWithEnrolledPasskey(recipientPage, recipientEmail)
    expect(recipientID).not.toBe(ownerID)

    const recipientAuthority = await recipientPage.request.get('/api/instance/authority')
    expect(recipientAuthority.status()).toBe(200)
    expect(await recipientAuthority.json()).toMatchObject({
      is_instance_owner: false,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: false,
      can_manage_instance_settings: false,
    })
    expect((await recipientPage.request.get('/api/admin/users')).status()).toBe(403)
    await recipientPage.goto('/admin')
    await expect(recipientPage.getByRole('link', { name: 'Open Instance Settings' })).toHaveCount(0)
    await expectSettingsDenied(recipientPage)
    await expectSettingsWriteDenied(recipientPage, page)
    const incomingRead = await recipientPage.request.get('/api/instance/owner-transfers')
    expect(incomingRead.status()).toBe(200)
    const incomingBody = await incomingRead.json() as {
      transfers: Array<{ id: string; direction: string; counterparty_display_name: string }>
    }
    expect(incomingBody.transfers).toEqual([{
      id: transferID,
      direction: 'incoming',
      counterparty_display_name: 'Workbench Owner',
      state: 'pending',
      created_at: expect.any(String),
      expires_at: expect.any(String),
    }])

    await recipientPage.goto('/admin')
    await expect(recipientPage.getByRole('heading', { name: /Workbench Owner wants to transfer ownership to you/ })).toBeVisible()
    const acceptResponsePromise = recipientPage.waitForResponse((response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname === `/api/instance/owner-transfers/${transferID}/accept`,
    )
    await acceptConfirmation(
      recipientPage,
      recipientPage.getByRole('button', { name: 'Accept ownership' }),
      'Workbench Owner',
    )
    const acceptResponse = await acceptResponsePromise
    expect(acceptResponse.status()).toBe(200)
    await expect(recipientPage.getByText('Ownership transfer accepted. Your account is now the instance owner.')).toBeVisible()
    await expect.poll(async () => {
      const response = await recipientPage.request.get('/api/auth/me')
      return response.status() === 200 ? (await response.json() as { is_admin: boolean }).is_admin : null
    }).toBe(false)
    const newOwnerAuthority = await recipientPage.request.get('/api/instance/authority')
    expect(newOwnerAuthority.status()).toBe(200)
    expect(await newOwnerAuthority.json()).toMatchObject({
      is_instance_owner: true,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: true,
      can_manage_instance_settings: true,
    })
    expect((await recipientPage.request.get('/api/admin/users')).status()).toBe(200)
    await expect(recipientPage.getByRole('heading', { name: 'Accounts' })).toBeVisible()
    await expect(recipientPage.getByRole('link', { name: 'Open Instance Settings' })).toBeVisible()
    await recipientPage.getByRole('link', { name: 'Open Instance Settings' }).click()
    await expect(recipientPage).toHaveURL(/\/admin\/settings$/)
    await expectSettingsReadWrite(recipientPage)

    const oldOwnerSession = await page.request.get('/api/auth/me')
    expect(oldOwnerSession.status()).toBe(200)
    expect(await oldOwnerSession.json()).toMatchObject({ id: ownerID, email: ownerEmail, is_admin: false })
    const oldOwnerAuthority = await page.request.get('/api/instance/authority')
    expect(oldOwnerAuthority.status()).toBe(200)
    expect(await oldOwnerAuthority.json()).toMatchObject({
      is_instance_owner: false,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: false,
      can_manage_instance_settings: false,
    })
    expect((await page.request.get('/api/admin/users')).status()).toBe(403)
    await page.reload()
    await expect(page.getByRole('heading', { name: 'No instance actions are available' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Accounts' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: /Transfer ownership/ })).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Open Instance Settings' })).toHaveCount(0)
    await expectSettingsDenied(page)
    await expectSettingsWriteDenied(page, recipientPage)
  } finally {
    grants.owner = undefined
    grants.recipient = undefined
    delete process.env[ownerGrantKey]
    delete process.env[recipientGrantKey]
    await removeAuthenticator(recipientLease)
    await removeAuthenticator(ownerLease)
    await recipientContext?.close().catch(() => {})
  }
})
