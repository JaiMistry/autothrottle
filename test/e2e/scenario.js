// The end-to-end scenario: a fresh install set up and used entirely
// through its web UI, against a real Jellyfin or Emby, qBittorrent and
// Transmission. Every check that matters is made twice where it can be —
// on the page, and on the download client itself — because the page
// saying "throttled" proves nothing if qBittorrent was never told.
//
// Run by run.sh inside the Puppeteer image with host networking.
const fs = require('fs');
const path = require('path');
const puppeteer = require('puppeteer');

const env = JSON.parse(fs.readFileSync(path.join(__dirname, '.state/env.json')));
const APP = env.autothrottle;
const MEDIA = env.media; // Jellyfin or Emby, whichever this run provisioned
const NAME = MEDIA.label;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let failures = 0;
let step = 0;
function check(ok, what, detail = '') {
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${what}${!ok && detail ? `\n          ${detail}` : ''}`);
  if (!ok) failures++;
}
function section(title) { console.log(`\n${++step}. ${title}`); }

async function until(what, fn, timeoutMs = 90000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try { last = await fn(); if (last) return last; } catch (e) { last = e.message; }
    await sleep(1000);
  }
  return null;
}

// --- the outside world: a player, and the download clients ---------------

const AUTH = `MediaBrowser Client="AutoThrottle E2E", Device="Living Room TV", DeviceId="e2e-player", Version="1.0.0", Token="${MEDIA.userToken}"`;
const player = {
  async report(endpoint, positionSeconds, paused = false) {
    const res = await fetch(MEDIA.base + endpoint, {
      method: 'POST',
      // Jellyfin reads Authorization; Emby reads its own header.
      headers: { 'Content-Type': 'application/json', Authorization: AUTH, 'X-Emby-Authorization': AUTH, 'X-Emby-Token': MEDIA.userToken },
      body: JSON.stringify({
        ItemId: MEDIA.itemId, PlayMethod: 'DirectStream', CanSeek: true,
        PositionTicks: Math.round(positionSeconds * 1e7), IsPaused: paused, PlaySessionId: 'e2e-1',
      }),
    });
    if (!res.ok) throw new Error(`${MEDIA.kind} ${endpoint}: ${res.status}`);
  },
};

// A player keeps reporting progress while it plays; this does the same.
function startPlayer() {
  let position = 60, paused = false;
  player.report('/Sessions/Playing', position);
  const timer = setInterval(() => {
    if (!paused) position += 2;
    player.report('/Sessions/Playing/Progress', position, paused).catch(() => {});
  }, 2000);
  return {
    pause() { paused = true; return player.report('/Sessions/Playing/Progress', position, true); },
    resume() { paused = false; return player.report('/Sessions/Playing/Progress', position, false); },
    async stop() { clearInterval(timer); await player.report('/Sessions/Playing/Stopped', position); },
  };
}

const qbittorrent = {
  async limits() {
    const login = await fetch(env.qbittorrent.base + '/api/v2/auth/login', {
      method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: `username=${env.qbittorrent.username}&password=${env.qbittorrent.password}`,
    });
    const cookie = (login.headers.get('set-cookie') || '').split(';')[0];
    const get = async (p) => Number(await (await fetch(env.qbittorrent.base + p, { headers: { Cookie: cookie } })).text());
    return { down: await get('/api/v2/transfer/downloadLimit'), up: await get('/api/v2/transfer/uploadLimit') };
  },
};

const transmission = {
  sid: '',
  async limits() {
    const auth = 'Basic ' + Buffer.from(`${env.transmission.username}:${env.transmission.password}`).toString('base64');
    for (let attempt = 0; attempt < 2; attempt++) {
      const res = await fetch(env.transmission.base + '/transmission/rpc', {
        method: 'POST', headers: { Authorization: auth, 'X-Transmission-Session-Id': this.sid },
        body: JSON.stringify({ method: 'session-get', arguments: { fields: ['speed-limit-down', 'speed-limit-down-enabled', 'speed-limit-up'] } }),
      });
      if (res.status === 409) { this.sid = res.headers.get('x-transmission-session-id'); continue; }
      const a = (await res.json()).arguments;
      return { down: a['speed-limit-down-enabled'] ? a['speed-limit-down'] : 0, up: a['speed-limit-up'] };
    }
    throw new Error('transmission handshake failed');
  },
};

// --- the browser ------------------------------------------------------------

(async () => {
  const browser = await puppeteer.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 1000 });
  page.setDefaultTimeout(30000);
  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));

  const at = () => new URL(page.url()).pathname;
  const text = (sel) => page.$eval(sel, (e) => e.innerText.replace(/\s+/g, ' ').trim()).catch(() => '');
  const navigate = (p) => page.goto(APP + p, { waitUntil: 'networkidle2' });
  async function fill(sel, value) {
    await page.$eval(sel, (e) => { e.value = ''; });
    await page.type(sel, value);
  }
  // Forms and links are boosted by htmx: a submit swaps the page over XHR
  // rather than navigating, so wait for the network to settle instead.
  async function settle(action) {
    await Promise.all([page.waitForNetworkIdle({ idleTime: 400, timeout: 30000 }), action()]);
  }
  const submit = (formSel) => settle(() => page.click(`${formSel} [data-submit]`));
  async function testConnection(formSel) {
    // A real mouse click, so focus moves the way it does for a person.
    const buttons = await page.$$(`${formSel} button`);
    for (const b of buttons) {
      if (/test connection/i.test(await b.evaluate((el) => el.textContent))) { await b.click(); break; }
    }
    // The card shows a "testing" placeholder the instant the button is
    // pressed; wait for the answer, not for that.
    await page.waitForFunction((f) => {
      const r = document.querySelector(`${f} .at-testresult[data-state]`);
      return r && r.dataset.state !== 'testing';
    }, {}, formSel);
    return {
      state: await page.$eval(`${formSel} .at-testresult`, (e) => e.dataset.state),
      text: await text(`${formSel} .at-testresult`),
      canSave: await page.$eval(`${formSel} [data-submit]`, (b) => !b.disabled),
    };
  }
  // Names in the saved-entries table, not anywhere on the page: the type
  // dropdown also names the server type.
  const savedNames = () => page.$$eval('tbody tr td:first-child', (cells) => cells.map((c) => c.innerText.trim()));
  const dashboard = async () => page.evaluate(() => ({
    hero: document.querySelector('#dash-live p')?.innerText.trim() || '',
    downloaders: [...document.querySelectorAll('.at-dlrow-wrap')].map((r) => r.dataset.state),
    sessionTime: document.querySelector('.at-session-time')?.textContent.trim() || '',
    paused: document.querySelector('.at-session-progress')?.dataset.paused === '1',
  }));

  try {
    section('First run');
    await navigate('/');
    check(at() === '/setup', 'a fresh install opens on the setup screen', at());
    await fill('[name=username]', 'admin');
    await fill('[name=password]', 'e2e-password');
    await fill('[name=confirm]', 'e2e-passwore');
    await settle(() => page.click('button[type=submit]'));
    check((await text('.at-alert')).includes('two passwords'), 'mismatched passwords are refused');
    await fill('[name=password]', 'e2e-password');
    await fill('[name=confirm]', 'e2e-password');
    await settle(() => page.click('button[type=submit]'));
    check(at() === '/', 'setup creates the account and signs in', at());
    check((await page.content()).includes('Nothing configured yet'), 'the empty Dashboard says nothing is configured');

    section('Media server');
    await navigate('/configuration?tab=media-servers');
    await page.select('#ms-form [name=type]', MEDIA.kind);
    await fill('#ms-form [name=name]', NAME);
    await fill('#ms-form [name=url]', MEDIA.internal);
    const keyField = '#ms-form [name=api_token]';
    await fill(keyField, 'not-the-key');
    let result = await testConnection('#ms-form');
    check(result.state === 'failed' && !result.canSave, 'a wrong API key fails the test and blocks saving', JSON.stringify(result));
    await fill(keyField, MEDIA.apiKey);
    check(!(await page.$('#ms-form .at-testresult[data-state="connected"]')), 'editing a field retires an old result');
    result = await testConnection('#ms-form');
    check(result.state === 'connected' && result.canSave, 'the right API key passes and allows saving', JSON.stringify(result));
    await submit('#ms-form');
    check((await savedNames()).includes(NAME), 'the server is saved', JSON.stringify(await savedNames()));

    await page.select('#ms-form [name=type]', MEDIA.kind);
    await fill('#ms-form [name=name]', `${NAME} again`);
    await fill('#ms-form [name=url]', MEDIA.internal + '/');
    await fill(keyField, MEDIA.apiKey);
    await testConnection('#ms-form');
    await submit('#ms-form');
    check((await text('.at-alert')).includes('already added'), 'adding the same server twice is refused with a reason', await text('.at-alert'));
    check((await savedNames()).filter((n) => n.startsWith(NAME)).length === 1, 'and nothing was added');

    section('Download clients');
    for (const [name, type, conf, down, up] of [
      ['qBittorrent', 'qbittorrent', env.qbittorrent, 1500, 300],
      ['Transmission', 'transmission', env.transmission, 900, 180],
    ]) {
      await navigate('/configuration?tab=downloaders');
      await page.select('#dl-form [name=type]', type);
      await fill('#dl-form [name=name]', name);
      await fill('#dl-form [name=url]', conf.internal);
      await fill('#dl-form [name=username]', conf.username);
      await fill('#dl-form [name=password]', conf.password);
      await fill('#dl-form [name=backup_download_limit]', String(down));
      await fill('#dl-form [name=backup_upload_limit]', String(up));
      result = await testConnection('#dl-form');
      check(result.state === 'connected', `${name} connects`, JSON.stringify(result));
      await submit('#dl-form');
      check((await savedNames()).includes(name), `${name} is saved`, JSON.stringify(await savedNames()));
    }

    section('Mappings');
    await navigate('/configuration?tab=mappings');
    check((await page.$$('.at-matrix-cell[aria-pressed="true"]')).length === 2, 'new pairings start as Throttle');
    await page.click(`.at-matrix-cell[title="Leave Transmission at its default rate when ${NAME} plays"]`);
    await page.waitForSelector(`.at-matrix-cell[aria-pressed="false"][title="Throttle Transmission when ${NAME} plays"]`);
    check(true, 'a pairing can be switched off');
    await page.click(`.at-matrix-cell[title="Throttle Transmission when ${NAME} plays"]`);
    await page.waitForSelector(`.at-matrix-cell[aria-pressed="true"][title="Leave Transmission at its default rate when ${NAME} plays"]`);
    check((await page.$$('.at-matrix-cell[aria-pressed="true"]')).length === 2, 'and back on');

    section('Idle');
    await navigate('/');
    const idle = await until('the idle state', async () => {
      const d = await dashboard();
      return d.hero === 'Nothing is throttled' && d.downloaders.every((s) => s === 'normal') ? d : null;
    }, 30000);
    check(!!idle, 'the Dashboard says nothing is throttled', JSON.stringify(await dashboard()));
    check((await qbittorrent.limits()).down === 0, 'qBittorrent is unlimited');
    const health = await until('a healthy /health', async () => {
      const h = await (await fetch(env.autothrottle + '/health')).json();
      return h.status === 'ok' ? h : null;
    }, 30000);
    check(!!health && health.media_servers.length === 1 && health.downloaders.length === 2,
      '/health reports every backend reachable, each checked as its own type');

    section('Playback starts — watched live, no reloads');
    const player = startPlayer();
    const throttled = await until('throttling', async () => {
      const d = await dashboard();
      return d.downloaders.every((s) => s === 'throttled') ? d : null;
    });
    check(!!throttled, 'both clients show as throttled without a reload', JSON.stringify(await dashboard()));
    check(/throttled/.test((await dashboard()).hero), 'the headline says what is throttled');
    const q = await qbittorrent.limits();
    check(q.down === 1500 * 1024 && q.up === 300 * 1024, 'qBittorrent really is limited to 1500/300 KB/s', JSON.stringify(q));
    const t = await transmission.limits();
    check(t.down === 900 && t.up === 180, 'Transmission really is limited to 900/180 KB/s', JSON.stringify(t));
    check((await page.content()).includes(MEDIA.itemName), 'the playing title is shown');

    const before = (await dashboard()).sessionTime;
    await sleep(6000);
    const after = (await dashboard()).sessionTime;
    check(before && after && before !== after, 'the timecode advances while playing', `${before} → ${after}`);

    section('Pause and resume');
    await player.pause();
    const paused = await until('the pause to show', async () => ((await dashboard()).paused ? await dashboard() : null), 30000);
    check(!!paused, 'the session shows as paused');
    const p1 = (await dashboard()).sessionTime; await sleep(6000); const p2 = (await dashboard()).sessionTime;
    check(p1 === p2, 'the timecode holds still while paused', `${p1} → ${p2}`);
    check((await dashboard()).downloaders.every((s) => s === 'throttled'), 'a pause keeps the throttle on');
    await player.resume();

    section('Disable throttling');
    await settle(() => page.click('form[action="/pause-toggle"] button'));
    const overridden = await until('the override', async () => ((await qbittorrent.limits()).down === 0 ? true : null), 30000);
    check(!!overridden, 'turning throttling off restores default rates while still playing');
    await settle(() => page.click('form[action="/pause-toggle"] button'));
    const back = await until('throttling to come back', async () => ((await qbittorrent.limits()).down === 1500 * 1024 ? true : null), 30000);
    check(!!back, 'turning it back on re-throttles immediately');

    section('Playback stops');
    await player.stop();
    const released = await until('the release', async () => {
      const d = await dashboard();
      return d.hero === 'Nothing is throttled' ? d : null;
    });
    check(!!released, 'the Dashboard returns to nothing throttled without a reload', JSON.stringify(await dashboard()));
    check((await qbittorrent.limits()).down === 0, 'qBittorrent is unlimited again');
    check((await transmission.limits()).down === 0, 'Transmission is unlimited again');

    section('Logs');
    await navigate('/logs');
    const logText = await text('#log-panel');
    check(logText.includes(`throttled qBittorrent because ${NAME} is playing`), 'the throttle is logged with its cause');
    check(/restored default rate for qBittorrent/.test(logText), 'the restore is logged');
    await page.click('#log-panel form[action="/logs/clear"] button');
    await page.waitForFunction(() => document.querySelectorAll('.at-logrow').length === 0);
    check(true, 'Clear empties the log, with no confirmation');
    const p2player = startPlayer();
    const relogged = await until('a new event to appear live', async () =>
      (await page.$$eval('.at-logrow', (r) => r.length)) > 0 ? true : null);
    check(!!relogged, 'a new event appears without a reload after clearing');
    await p2player.stop();

    section('Navigation');
    await navigate('/');
    await settle(() => page.click('.at-nav-item[href="/logs"]'));
    check(at() === '/logs' && (await page.$$('.at-shell')).length === 1, 'moving between pages swaps cleanly', at());
    check(pageErrors.length === 0, 'no JavaScript errors on any page', pageErrors.join(' | '));
  } catch (e) {
    check(false, 'the scenario ran to the end', e.stack);
    await page.screenshot({ path: path.join(__dirname, '.state/failure.png'), fullPage: true }).catch(() => {});
  } finally {
    await browser.close();
  }

  console.log(`\n${failures === 0 ? 'ALL PASSED' : `${failures} FAILED`}`);
  process.exit(failures === 0 ? 0 : 1);
})();
