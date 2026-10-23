// Uses the system Playwright (no npm install) and the system Chromium.
// Run through ../playwright.sh, which sets the environment below.
const port = process.env.E2E_EDGE_PORT || '18443';
const names = ['ermite', 'app', 'api', 'ci', 'gordon', 'files'].map((n) => `${n}.localtest.me`);

module.exports = {
  testDir: '.',
  timeout: 60_000,
  retries: 0,
  reporter: [['list']],
  use: {
    browserName: 'chromium',
    headless: true,
    // The edge uses Caddy's internal CA; the browser trusts it through
    // ignoreHTTPSErrors only, the API checks in the spec use the CA file.
    ignoreHTTPSErrors: true,
    launchOptions: {
      executablePath: process.env.CHROMIUM || '/usr/sbin/chromium',
      // The edge listens on 127.0.0.1:<port>. Mapping name:443 there keeps
      // the public URLs (and so the Origin ermite checks) unchanged.
      args: [`--host-resolver-rules=${names.map((n) => `MAP ${n}:443 127.0.0.1:${port}`).join(',')}`],
    },
  },
  metadata: { port },
};
