const { test, expect, signIn } = require('./fixtures');
const { execFileSync } = require('node:child_process');
const { writeFile } = require('node:fs/promises');
const path = require('node:path');

test('validation recovery, downloads, repeated edits and stale conflicts', async ({ page, app }) => {
  const external = [];
  const scriptErrors = [];
  page.on('pageerror', err => scriptErrors.push(err.message));
  await page.route('**/*', route => {
    if (new URL(route.request().url()).origin !== app.origin) {
      external.push(route.request().url());
      return route.abort();
    }
    return route.continue();
  });
  await signIn(page, app);
  await page.goto('/ui/codes/new');
  await page.getByLabel('Destination URL', { exact: true }).fill(`${app.origin}/r/loop`);
  await page.getByRole('button', { name: 'Create code', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('cannot point back');
  await expect(page.getByLabel('Destination URL', { exact: true })).toHaveValue(`${app.origin}/r/loop`);
  await page.getByLabel('Destination URL', { exact: true }).fill('https://example.com/first');
  await page.locator('#fg_color').fill('#ffffff');
  await page.getByRole('button', { name: 'Create code', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText(/contrast/i);
  await page.locator('#fg_color').fill('#101828');
  await expect(page.locator('[data-swatch-value="fg_color"]')).toHaveText('#101828');
  await expect(page.locator('[data-preview-image]')).toBeVisible();
  await page.getByRole('button', { name: 'Create code', exact: true }).click();
  await expect(page).toHaveURL(/\/ui\/codes\/cod_/);
  const detailURL = page.url();
  const imageURL = await page.getByRole('link', { name: 'Download image' }).getAttribute('href');
  const response = await page.request.get(imageURL);
  expect(response.status()).toBe(200);
  const image = path.join(app.dir, 'download.png');
  await writeFile(image, await response.body());
  const scanURL = execFileSync(path.join(__dirname, '.bin/qrdecode'), [image], { encoding: 'utf8' }).trim();
  expect(scanURL).toMatch(new RegExp(`^${app.origin}/r/`));
  const stale = await page.context().newPage();
  await stale.goto(detailURL);
  for (const destination of ['https://example.com/second', 'https://example.com/third']) {
    await page.getByLabel('Destination URL', { exact: true }).fill(destination);
    await page.getByRole('button', { name: 'Save destination' }).click();
    await expect(page.locator('#destination-status')).toContainText(`Destination updated to ${destination}`);
    const scan = await page.request.get(scanURL, { maxRedirects: 0 });
    expect(scan.status()).toBe(302);
    expect(scan.headers().location).toBe(destination);
  }
  await stale.getByLabel('Destination URL', { exact: true }).fill('https://example.com/stale');
  await stale.getByRole('button', { name: 'Save destination' }).click();
  await expect(stale.getByRole('alert')).toContainText('Someone else changed');
  await stale.close();
  await page.getByRole('button', { name: 'Delete this code' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Destination', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Delete this code' }).click();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page).toHaveURL(`${app.origin}/ui/`);
  expect(external).toEqual([]);
  expect(scriptErrors).toEqual([]);
});

test('token controls work after swap, clipboard failures retain text, revoke confirms', async ({ page, app }) => {
  await page.addInitScript(() => {
    window.copyOutcome = 'reject';
    Object.defineProperty(navigator, 'clipboard', { configurable: true, get: () => {
      if (window.copyOutcome === 'missing') return undefined;
      return { writeText: value => {
        if (window.copyOutcome === 'reject') return Promise.reject(new Error('Permission denied'));
        window.copiedValue = value;
        return Promise.resolve();
      } };
    } });
  });
  await signIn(page, app);
  await page.goto('/ui/tokens');
  await page.getByLabel('Name', { exact: true }).fill('browser-token');
  await page.getByRole('button', { name: 'Create token', exact: true }).click();
  const secret = page.locator('[data-secret-value]');
  await expect(secret).toBeVisible();
  const value = await secret.textContent();
  for (const outcome of ['reject', 'missing']) {
    await page.evaluate(v => { window.copyOutcome = v; }, outcome);
    await page.getByRole('button', { name: 'Copy to clipboard' }).click();
    await expect(page.getByRole('status')).toContainText(/copy.*manually/i);
    await expect(secret).toHaveText(value);
  }
  await page.evaluate(() => { window.copyOutcome = 'success'; });
  await page.getByRole('button', { name: 'Copy to clipboard' }).click();
  await expect(secret).toHaveCount(0);
  expect(await page.evaluate(() => window.copiedValue)).toBe(value);
  const before = await page.request.get('/v1/codes', { headers: { Authorization: `Bearer ${value}` } });
  expect(before.status()).toBe(200);
  await page.getByRole('link', { name: 'Back to tokens' }).click();
  const row = page.getByRole('row').filter({ hasText: 'browser-token' });
  await row.getByRole('button', { name: 'Revoke' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(row).toContainText('revoked');
  // The existing positive credential cache permits up to 60s propagation (001 SC-011).
  test.setTimeout(65000);
  await expect.poll(async () => (await page.request.get('/v1/codes', {
    headers: { Authorization: `Bearer ${value}` },
  })).status(), { timeout: 60000, intervals: [1000, 2000, 5000] }).toBe(401);
});

test('console and API share sign-in allowance', async ({ page, app }) => {
  for (let i = 0; i < 10; i++) {
    const response = i % 2 === 0
      ? await page.request.post('/v1/auth/signin', { data: { email: app.email, password: 'wrong' } })
      : await page.request.post('/ui/signin', { form: { email: app.email, password: 'wrong' } });
    expect(response.status()).toBe(401);
  }
  const response = await page.request.post('/v1/auth/signin', { data: { email: app.email, password: app.password } });
  expect(response.status()).toBe(429);
  expect(response.headers()['retry-after']).toBeTruthy();
  await page.goto('/ui/signin');
  await page.getByLabel('Email', { exact: true }).fill(app.email);
  await page.getByLabel('Password', { exact: true }).fill(app.password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText(/too many/i);
  expect((await page.request.get('/healthz')).status()).toBe(200);
});


test('unexpected server and network errors preserve the form', async ({ page, app }) => {
  await signIn(page, app);
  await page.goto('/ui/codes/new');
  await page.getByLabel('Destination URL', { exact: true }).fill('https://example.com/recover');
  await page.route('**/ui/codes', route => route.fulfill({
    status: 500, contentType: 'text/html', body: '<p>private internal detail</p>',
  }));
  await page.getByRole('button', { name: 'Create code', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('Could not complete the request. Please try again.');
  await expect(page.getByLabel('Destination URL', { exact: true })).toHaveValue('https://example.com/recover');
  await expect(page.locator('body')).not.toContainText('private internal detail');
  await page.unroute('**/ui/codes');
  await page.route('**/ui/codes', route => route.abort());
  await page.getByRole('button', { name: 'Create code', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('Could not reach the server. Please try again.');
  await page.unroute('**/ui/codes');
  await page.getByRole('button', { name: 'Create code', exact: true }).click();
  await expect(page).toHaveURL(/\/ui\/codes\/cod_/);
});
