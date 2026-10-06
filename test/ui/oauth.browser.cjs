// Invoked by TestOAuthBrowserUI against a disposable Go test fixture.
const assert = require('node:assert/strict');
const { mkdirSync } = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.HMD_PLAYWRIGHT_MODULE);
const base = process.env.HMD_UI_URL;

(async () => {
  const browser = await chromium.launch({
    executablePath: process.env.HMD_CHROMIUM || undefined,
    args: process.env.HMD_CHROMIUM_NO_SANDBOX === '1' ? ['--no-sandbox'] : []
  });
  try {
    const context = await browser.newContext({ viewport: { width: 1360, height: 1000 }, colorScheme: 'dark' });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('console', msg => {
      if (msg.type() === 'error' && /Content Security Policy|Refused to/.test(msg.text())) errors.push(msg.text());
    });
    // Never follow a test callback onto the public network.
    await context.route('https://client.example.test/**', route => route.fulfill({ contentType: 'text/html', body: '<p>Callback received</p>' }));
    const screenshots = process.env.HMD_UI_SCREENSHOTS;
    async function shot(name) {
      // The shell scrolls <main>, not the document. Wait for the mobile
      // sidebar transition after resizing and capture from the top.
      await page.evaluate(() => { document.querySelector('main').scrollTop = 0; });
      await page.waitForTimeout(300);
      assert.equal(await page.evaluate(() => {
        const main = document.querySelector('main');
        return document.documentElement.scrollWidth > innerWidth || main.scrollWidth > main.clientWidth;
      }), false, `${name}: horizontal overflow`);
      if (screenshots) {
        mkdirSync(screenshots, { recursive: true });
        await page.screenshot({ path: path.join(screenshots, name + '.png'), fullPage: true });
      }
    }
    async function consent(scope = 'read write') {
      const params = new URLSearchParams({
        client_id: process.env.HMD_UI_CLIENT_ID, redirect_uri: 'https://client.example.test/callback',
        response_type: 'code', state: 'browser-security-check', scope,
        resource: 'https://wiki.example.test/_/mcp',
        code_challenge: '7Cf9_Fvrp7a3sr2lTFtL8Y7unb12dzT_5c1NvGm2HSo', code_challenge_method: 'S256'
      });
      const response = await page.goto(base + '/_/oauth/authorize?' + params);
      assert.equal(response.status(), 200);
      assert.equal(response.headers()['cache-control'], 'no-store');
      assert.equal(response.headers()['referrer-policy'], 'same-origin');
      assert.match(response.headers()['content-security-policy'], /frame-ancestors 'none'/);
    }
    await page.goto(base + '/_/login');
    await page.locator('input[name="username"]').fill('admin');
    await page.locator('input[name="password"]').fill('password12345');
    await Promise.all([page.waitForURL(url => !url.pathname.includes('/login')), page.locator('.login-form button[type="submit"]').click()]);

    await page.goto(base + '/_/admin/oauth');
    await shot('clients-desktop-dark');
    await page.locator('#oauth-register > summary').click();
    await page.locator('#client-name').fill('Example research client');
    await page.locator('#client-redirects').fill('https://client.example.test/callback');
    await page.locator('input[name="scope"][value="write"]').check();
    const created = page.waitForResponse(r => r.url() === base + '/_/admin/oauth' && r.request().method() === 'POST');
    await page.getByRole('button', { name: 'Register client', exact: true }).click();
    const response = await created;
    assert.equal(response.status(), 201);
    assert.equal(response.headers()['cache-control'], 'no-store');
    assert.equal(response.headers()['content-encoding'], undefined, 'one-time secrets must not be compressed');
    assert.match(await page.locator('#oauth-created-secret').inputValue(), /^hmd_cs_/);
    await page.getByRole('button', { name: 'Copy secret', exact: true }).click();
    await page.waitForFunction(() => /Copied|Selected for copying/.test(document.querySelector('[data-oauth-copy-status]').textContent));
    assert.match(await page.locator('[data-oauth-copy-status]').textContent(), /Copied|Selected for copying/);
    // Do not capture one-time credentials, even in synthetic screenshots.
    await page.getByRole('link', { name: 'Done — return to client list' }).click();
    assert.equal(await page.locator('#oauth-created-secret').count(), 0);
    const research = page.locator('article').filter({ has: page.getByRole('heading', { name: 'Example research client' }) });
    page.once('dialog', dialog => dialog.dismiss());
    await research.getByRole('button', { name: /Disable/ }).click();
    assert.equal(await research.locator('.oauth-status').textContent(), 'Active');
    page.once('dialog', dialog => dialog.accept());
    await Promise.all([page.waitForNavigation(), research.getByRole('button', { name: /Disable/ }).click()]);
    assert.equal(await research.locator('.oauth-status').textContent(), 'Disabled');
    await shot('clients-disabled-desktop-dark');
    await page.setViewportSize({ width: 390, height: 844 });
    await shot('clients-mobile-dark');

    await consent();
    const approve = page.locator('button[value="approve"]');
    assert.equal(await approve.isDisabled(), true);
    await page.locator('input[name="namespace"][value="notes"]').check();
    assert.equal(await approve.isDisabled(), false);
    await shot('consent-mobile-dark');
    await page.setViewportSize({ width: 1360, height: 1000 });
    await shot('consent-desktop-dark');
    await page.emulateMedia({ colorScheme: 'light' });
    await shot('consent-desktop-light');
    await Promise.all([page.waitForURL('https://client.example.test/**'), approve.click()]);
    assert.match(page.url(), /code=/);
    await page.goto(base + '/_/connections');
    assert.equal(await page.locator('.oauth-status').textContent(), 'Connected');
    await shot('connections-desktop-light');
    await page.setViewportSize({ width: 390, height: 844 });
    await shot('connections-mobile-light');
    page.once('dialog', dialog => dialog.accept());
    await Promise.all([page.waitForNavigation(), page.getByRole('button', { name: /Disconnect Example/ }).click()]);
    assert.equal(await page.locator('.oauth-status').textContent(), 'Disconnected');

    await consent('read write settings');
    const admin = page.locator('input[value="settings"]');
    assert.equal(await admin.isChecked(), false, 'administrator access must be explicitly selected');
    assert.equal(await approve.isDisabled(), true);
    await admin.check();
    assert.equal(await page.locator('#oauth-admin-warning').isVisible(), true);
    assert.equal(await page.locator('#oauth-namespace-access').isVisible(), false);
    assert.equal(await approve.isDisabled(), false);
    await admin.uncheck();
    assert.equal(await page.locator('input[name="namespace_mode"][value="selected"]').isChecked(), true);
    assert.equal(await approve.isDisabled(), true, 'removing settings must not silently allow all namespaces');
    await page.locator('input[name="namespace_mode"][value="all"]').check();
    assert.equal(await approve.isDisabled(), false);
    await page.locator('input[name="scope"][value="read"]').uncheck();
    await page.locator('input[name="scope"][value="write"]').uncheck();
    assert.equal(await approve.isDisabled(), true);
    await Promise.all([page.waitForURL('https://client.example.test/**'), page.locator('button[value="deny"]').click()]);
    assert.match(page.url(), /error=access_denied/);
    assert.deepEqual(errors, []);
    console.log('OAuth browser checks passed: native forms, consent, administrator opt-in, copy fallback, confirmations, revocation, CSP, dark/light and mobile layouts.');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
