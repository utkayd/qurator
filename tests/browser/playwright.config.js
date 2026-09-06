const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: '.',
  testMatch: '*.spec.js',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 30000,
  globalSetup: require.resolve('./setup'),
  use: {
    ...devices['Desktop Chrome'],
    channel: process.env.QURATOR_TEST_BROWSER_CHANNEL || undefined,
    trace: 'retain-on-failure',
  },
});
