import { test, expect } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { signInWithSyntheticEmail } from './auth'

test('serves private noindex instance shell beside the admin settings child route', async ({ page }) => {
  const policies = JSON.parse(await readFile(`${process.env.PLAYWRIGHT_DIST}/static-csp.json`, 'utf8')) as Array<{ route: string; policy: string }>
  const policy = policies.find((item) => item.route === '/admin')?.policy
  expect(policy).toBeTruthy()

  for (const path of ['/admin', '/admin.html']) {
    const response = await page.goto(path)
    expect(response?.status(), path).toBe(200)
    expect(response?.headers()['cache-control'], path).toBe('no-store')
    expect(response?.headers()['content-security-policy'], path).toBe(policy)
    await expect(page).toHaveTitle(/Instance — Cookbook/)
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', 'noindex,nofollow')
    await expect(page.getByRole('heading', { name: 'Sign in to view instance access' })).toBeVisible()
    const shell = await response!.text()
    expect(shell).not.toMatch(/is_instance_owner|can_manage_instance_roles|counterparty_display_name|owner@example\.test/)
  }

  const settings = await page.goto('/admin/settings')
  expect(settings?.status()).toBe(200)
  await expect(page).toHaveTitle(/Instance settings/)
})

test('ordinary signed-in account sees no Instance entry or account list', async ({ page, request }) => {
  const user = await signInWithSyntheticEmail(page, request, 'workbench-reader@example.test', 'Workbench Reader')
  expect(user.is_admin).toBe(false)
  const accountListRequests: string[] = []
  page.on('request', (browserRequest) => {
    if (new URL(browserRequest.url()).pathname === '/api/admin/users')
      accountListRequests.push(browserRequest.url())
  })

  for (const path of ['/admin', '/admin.html']) {
    await page.goto(path)
    await expect(page.getByRole('heading', { name: 'No instance actions are available' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Instance' })).toHaveCount(0)
  }

  expect(accountListRequests).toEqual([])
})

test('pre-claim administrator sees role management but no browser owner claim or transfer action', async ({ page, request }) => {
  const user = await signInWithSyntheticEmail(page, request, 'admin@example.test', 'Workbench Admin')
  expect(user.is_admin).toBe(true)
  const authorityResponse = await page.request.get('/api/instance/authority')
  expect(authorityResponse.status()).toBe(200)
  const authority = await authorityResponse.json() as {
    is_admin: boolean;
    is_instance_owner: boolean;
    owner_claimed: boolean;
    can_manage_instance_roles: boolean;
  }
  expect(authority).toMatchObject({
    is_admin: true,
    is_instance_owner: false,
    owner_claimed: false,
    can_manage_instance_roles: true,
  })

  for (const path of ['/admin', '/admin.html']) {
    await page.goto(path)
    await expect(page.getByRole('heading', { name: 'Instance owner not assigned' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Accounts' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Instance' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Administration' })).toBeVisible()
    await expect(page.getByRole('button', { name: /claim owner/i })).toHaveCount(0)
    await expect(page.getByRole('button', { name: /transfer ownership/i })).toHaveCount(0)
  }
})
