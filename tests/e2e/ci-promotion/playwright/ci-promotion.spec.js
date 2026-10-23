// Browser checks of the e2e harness.
//
// E2E_RUN_ID     run id to inspect on ermite (required)
// E2E_LOGIN_URL  one-time ermite login link (ermite web login), required
// E2E_EXPECT     "release" (default) or "probe": what the run must show
// E2E_VERSION    version promoted to web by the release run (required for
//                release; playwright.sh reads it from run.sh release)
// E2E_BASE       version the api service must keep (default v1)
const { test, expect } = require('playwright/test');

// The browser maps these names to the edge on 127.0.0.1 (playwright.config.js).
const ermite = 'https://ermite.localtest.me';
const repo = process.env.E2E_REPO || 'e2e-demo';
const runId = process.env.E2E_RUN_ID;
const expectKind = process.env.E2E_EXPECT || 'release';
const version = process.env.E2E_VERSION;
const base = process.env.E2E_BASE || 'v1';
const JWT = /eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{20,}/;

test.describe.configure({ mode: 'serial' });

test('ermite run page shows success and the job log has no JWT', async ({ page }) => {
  test.skip(!runId || !process.env.E2E_LOGIN_URL, 'E2E_RUN_ID and E2E_LOGIN_URL are required');

  // The login page signs the browser in by script (POST, same origin).
  const signedIn = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes('/-/login/'));
  await page.goto(process.env.E2E_LOGIN_URL);
  expect((await signedIn).status()).toBeLessThan(400);

  // Pages keep a live event stream open: never wait for network idle.
  const runUrl = `${ermite}/${repo}/actions/runs/${runId}`;
  const res = await page.goto(runUrl, { waitUntil: 'domcontentloaded' });
  expect(res.status()).toBe(200);
  const body = page.locator('body');
  await expect(body).toContainText(/success/i);
  await expect(body).not.toContainText(/failure/i);

  // Every job link of the run page, then its raw log.
  const jobHrefs = await page
    .locator(`a[href^="/${repo}/actions/runs/${runId}/job/"]`)
    .evaluateAll((as) => [...new Set(as.map((a) => a.getAttribute('href').split('?')[0]))]);
  expect(jobHrefs.length).toBeGreaterThan(0);
  for (const href of jobHrefs.filter((h) => !h.endsWith('/raw') && !h.endsWith('/stream'))) {
    // Fetched by the page itself: same origin, same resolver rules, and no
    // request headers (session cookie) in a failure report.
    const { status, log } = await page.evaluate(async (url) => {
      const r = await fetch(url, { credentials: 'same-origin' });
      return { status: r.status, log: await r.text() };
    }, `${href}/raw`);
    expect(status).toBe(200);
    expect(log.length).toBeGreaterThan(0);
    expect(JWT.test(log), `JWT in ${href}`).toBe(false);
    expect(/ermiteoidc_[A-Za-z0-9]|ermitegh_[A-Za-z0-9]/.test(log), `job token in ${href}`).toBe(false);
    if (expectKind === 'probe') {
      expect(log).toContain('CHECK sub PASS');
      expect(log).toContain('ci-edge-ok');
    } else {
      expect(log).toMatch(/"outcome":\s*"success"/);
    }
  }
});

test('app shows the promoted version, the other service is unchanged', async ({ page }) => {
  test.skip(expectKind !== 'release', 'only after a release run');
  expect(version, 'E2E_VERSION is required').toBeTruthy();
  await page.goto('https://app.localtest.me/');
  await expect(page.locator('body')).toContainText(`service=web version=${version}`);
  await page.goto('https://api.localtest.me/');
  await expect(page.locator('body')).toContainText(`service=api version=${base}`);
});
