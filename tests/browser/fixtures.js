const { test: base, expect } = require('@playwright/test');
const { spawn } = require('node:child_process');
const { mkdtemp, rm } = require('node:fs/promises');
const { tmpdir } = require('node:os');
const { once } = require('node:events');
const net = require('node:net');
const path = require('node:path');

const email = 'browser@example.test';
const password = 'browser-test-password-only';

const test = base.extend({
  app: async ({}, use) => {
    const dir = await mkdtemp(path.join(tmpdir(), 'qurator-browser-'));
    const listener = net.createServer();
    listener.listen(0, '127.0.0.1');
    await once(listener, 'listening');
    const port = listener.address().port;
    await new Promise(resolve => listener.close(resolve));
    const origin = `http://localhost:${port}`;
    // Deliberately do not inherit QURATOR_* credentials/config from the operator.
    const child = spawn(path.join(__dirname, '.bin/qurator'), [], {
      cwd: dir,
      env: {
        PATH: process.env.PATH,
        QURATOR_SERVER_LISTEN: `127.0.0.1:${port}`,
        QURATOR_SERVER_BASE_URL: origin,
        QURATOR_AUTH_BOOTSTRAP_EMAIL: email,
        QURATOR_AUTH_BOOTSTRAP_PASSWORD: password,
      },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    let logs = '';
    child.stderr.on('data', chunk => { logs += chunk; });
    const exited = once(child, 'exit');
    try {
      await expect.poll(async () => {
        if (child.exitCode !== null) throw new Error(logs);
        try { return (await fetch(`${origin}/readyz`)).status; } catch { return 0; }
      }, { timeout: 10000 }).toBe(200);
      await use({ origin, dir, email, password });
    } finally {
      child.kill('SIGTERM');
      const timer = setTimeout(() => child.kill('SIGKILL'), 5000);
      await exited;
      clearTimeout(timer);
      await rm(dir, { recursive: true, force: true });
    }
  },
  baseURL: async ({ app }, use) => use(app.origin),
});

async function signIn(page, app) {
  await page.goto('/ui/signin');
  await page.getByLabel('Email', { exact: true }).fill(app.email);
  await page.getByLabel('Password', { exact: true }).fill(app.password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page).toHaveURL(`${app.origin}/ui/`);
}

module.exports = { test, expect, signIn };
