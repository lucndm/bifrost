import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Placeholder and Enterprise Pages', () => {
  test('should load prompt-repo coming soon page', async ({ page }) => {
    await page.goto('/workspace/prompt-repo')
    await expect(page.getByText(/Prompt repository is coming soon/i)).toBeVisible({ timeout: 10000 })
  })

  test('should load alerting page', async ({ page }) => {
    await page.goto('/workspace/alerting')
    await page.waitForLoadState('networkidle')
    await expect(page.getByTestId('alert-rules-title')).toBeVisible()
    const readMore = page.getByTestId('alert-rules-read-more')
    await expect(readMore).toBeVisible()
    const [popup] = await Promise.all([page.waitForEvent('popup'), readMore.click()])
    await expect(popup).toHaveURL(/^https:\/\/docs\.getbifrost\.ai\/enterprise\/alerting\/alert-rules(\?|$)/)
    await popup.close()
  })

  test('should load guardrails page', async ({ page }) => {
    await page.goto('/workspace/guardrails')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails(?:\?.*)?$/)
  })

  test('should load audit-logs page', async ({ page }) => {
    await page.goto('/workspace/audit-logs')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/audit-logs(?:\?.*)?$/)
  })

  test('should load cluster page', async ({ page }) => {
    await page.goto('/workspace/cluster')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/cluster(?:\?.*)?$/)
  })

  test('should load custom-pricing page', async ({ page }) => {
    await page.goto('/workspace/custom-pricing')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/custom-pricing(?:\?.*)?$/)
  })

  test('should load rbac page', async ({ page }) => {
    await page.goto('/workspace/rbac')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/governance\/rbac(?:\?.*)?$/)
  })

  test('should load scim page', async ({ page }) => {
    await page.goto('/workspace/scim')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/scim(?:\?.*)?$/)
  })

  test('should load adaptive-routing page', async ({ page }) => {
    await page.goto('/workspace/adaptive-routing')
    await page.waitForLoadState('networkidle')
    // OSS ships a real dashboard for the adaptive load balancer (the upsell
    // page is enterprise-only now). A fresh gateway has served no traffic, so
    // the snapshot renders its empty states.
    await expect(page.getByTestId('adaptive-routing-dashboard')).toBeVisible()
    await expect(page.getByText('Adaptive Routing')).toBeVisible()
    await expect(page.getByTestId('adaptive-directions-empty')).toBeVisible()
    await expect(page.getByTestId('adaptive-routes-empty')).toBeVisible()
    const settingsLink = page.getByTestId('adaptive-routing-settings-link')
    await expect(settingsLink).toBeVisible()
    await settingsLink.click()
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/adaptive-routing\/settings(?:\?.*)?$/)
    await expect(page.getByTestId('adaptive-settings')).toBeVisible()
    await expect(page.getByTestId('adaptive-settings-direction-selection-enabled-switch')).toBeVisible()
  })

  test('should load guardrails configuration page', async ({ page }) => {
    await page.goto('/workspace/guardrails/configuration')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails\/configuration(?:\?.*)?$/)
  })

  test('should load guardrails providers page', async ({ page }) => {
    await page.goto('/workspace/guardrails/providers')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails\/providers(?:\?.*)?$/)
  })
})
