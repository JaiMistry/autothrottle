// The end-to-end scenario: a fresh install driven through its web UI
// against a real Jellyfin, Emby or Plex, qBittorrent, Transmission and
// Deluge.
// Checks are made on the download clients too: the page saying
// "throttled" proves nothing if qBittorrent was never told. Run by run.sh.
const fs = require('fs');
const http = require('http');
const path = require('path');
// In the Puppeteer image, the full package and its own Chrome; on a CI
// runner, puppeteer-core and the Chrome already installed there.
const puppeteer = (() => {
  try { return require('puppeteer'); } catch (e) { return require('puppeteer-core'); }
})();

const env = JSON.parse(fs.readFileSync(path.join(__dirname, '.state/env.json')));
const APP = env.autothrottle;
const MEDIA = env.media; // Jellyfin, Emby or Plex, whichever this run provisioned
const PLEX = MEDIA.kind === 'plex';
// The Plex test account's token: from the environment, never from disk.
const PLEX_TOKEN = process.env.PLEX_E2E_TOKEN || '';
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

// Jellyfin and Emby take playback reports on their sessions API; Plex takes
// the timeline a client sends. Either way the server holds a real session.
const AUTH = `MediaBrowser Client="AutoThrottle E2E", Device="Living Room TV", DeviceId="e2e-player", Version="1.0.0", Token="${MEDIA.userToken}"`;
const mediaBrowserPlayer = {
  async send(endpoint, item, positionSeconds, paused) {
    const res = await fetch(MEDIA.base + endpoint, {
      method: 'POST',
      // Jellyfin reads Authorization; Emby reads its own header.
      headers: { 'Content-Type': 'application/json', Authorization: AUTH, 'X-Emby-Authorization': AUTH, 'X-Emby-Token': MEDIA.userToken },
      body: JSON.stringify({
        ItemId: item.id, PlayMethod: 'DirectStream', CanSeek: true,
        PositionTicks: Math.round(positionSeconds * 1e7), IsPaused: paused, PlaySessionId: 'e2e-1',
      }),
    });
    if (!res.ok) throw new Error(`${MEDIA.kind} ${endpoint}: ${res.status}`);
  },
  start(item, pos) { return this.send('/Sessions/Playing', item, pos, false); },
  progress(item, pos, paused) { return this.send('/Sessions/Playing/Progress', item, pos, paused); },
  stop(item, pos) { return this.send('/Sessions/Playing/Stopped', item, pos, false); },
};
const plexPlayer = {
  async send(state, item, positionSeconds) {
    const q = new URLSearchParams({
      ratingKey: item.id, key: `/library/metadata/${item.id}`, state,
      time: String(Math.round(positionSeconds * 1000)), duration: String(item.durationMs),
      identifier: 'com.plexapp.plugins.library', playQueueItemID: '1',
    });
    const res = await fetch(`${MEDIA.base}/:/timeline?${q}`, {
      headers: {
        'X-Plex-Token': PLEX_TOKEN, 'X-Plex-Client-Identifier': 'e2e-player', 'X-Plex-Product': 'Plex Web',
        'X-Plex-Device-Name': 'Living Room TV', 'X-Plex-Platform': 'Chrome', 'X-Plex-Device': 'Linux', 'X-Plex-Version': '4.0',
      },
    });
    if (!res.ok) throw new Error(`plex timeline ${state}: ${res.status}`);
  },
  start(item, pos) { return this.send('playing', item, pos); },
  progress(item, pos, paused) { return this.send(paused ? 'paused' : 'playing', item, pos); },
  stop(item, pos) { return this.send('stopped', item, pos); },
};
const player = PLEX ? plexPlayer : mediaBrowserPlayer;

// The library's film, and its one TV episode (the same video, filed as
// one): what follows an episode decides when its throttle lifts.
const MOVIE = { id: MEDIA.itemId, durationMs: MEDIA.durationMs };
const EPISODE = { id: MEDIA.episodeId, durationMs: MEDIA.episodeDurationMs };

// A player keeps reporting progress while it plays; this does the same.
// from is where it starts, in seconds.
function startPlayer(item = MOVIE, from = 60) {
  let position = from, paused = false;
  player.start(item, position);
  const timer = setInterval(() => {
    if (!paused) position += 2;
    player.progress(item, position, paused).catch(() => {});
  }, 2000);
  return {
    pause() { paused = true; return player.progress(item, position, true); },
    resume() { paused = false; return player.progress(item, position, false); },
    async stop() { clearInterval(timer); await player.stop(item, position); },
  };
}

// Approves a "Sign in with Plex" request the way the account owner would
// on plex.tv, using the test account's token.
async function approvePlexPin(code) {
  const res = await fetch('https://plex.tv/api/v2/pins/link', {
    method: 'PUT',
    headers: {
      'X-Plex-Token': PLEX_TOKEN, 'X-Plex-Client-Identifier': process.env.PLEX_E2E_CLIENT_ID || 'autothrottle-e2e',
      // plex.tv only lets its own web app approve a sign-in.
      'X-Plex-Product': 'Plex Web', Accept: 'application/json', 'Content-Type': 'application/x-www-form-urlencoded',
    },
    body: new URLSearchParams({ code }).toString(),
  });
  if (!res.ok) throw new Error(`approving the Plex sign-in: ${res.status} ${(await res.text()).slice(0, 120)}`);
}

const qbittorrent = {
  async login() {
    const res = await fetch(env.qbittorrent.base + '/api/v2/auth/login', {
      method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: `username=${env.qbittorrent.username}&password=${env.qbittorrent.password}`,
    });
    return (res.headers.get('set-cookie') || '').split(';')[0];
  },
  async limits() {
    const cookie = await this.login();
    const get = async (p) => Number(await (await fetch(env.qbittorrent.base + p, { headers: { Cookie: cookie } })).text());
    return { down: await get('/api/v2/transfer/downloadLimit'), up: await get('/api/v2/transfer/uploadLimit') };
  },
  // Changes the limits behind AutoThrottle's back.
  async unlimit() {
    const cookie = await this.login();
    for (const p of ['/api/v2/transfer/setDownloadLimit', '/api/v2/transfer/setUploadLimit']) {
      await fetch(env.qbittorrent.base + p, {
        method: 'POST', headers: { Cookie: cookie, 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'limit=0',
      });
    }
  },
};

// Stops and starts containers through the Docker socket run.sh mounts in.
// What Docker's HEALTHCHECK last said of a container.
function dockerHealth(container = 'e2e-autothrottle') {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: '/var/run/docker.sock', path: `/containers/${container}/json` }, (res) => {
      let body = '';
      res.on('data', (d) => { body += d; });
      res.on('end', () => {
        try { resolve(JSON.parse(body).State?.Health?.Status || 'none'); } catch (e) { reject(e); }
      });
    });
    req.on('error', reject);
    req.end();
  });
}

function docker(action, container = 'e2e-autothrottle') {
  return new Promise((resolve, reject) => {
    const req = http.request({ socketPath: '/var/run/docker.sock', method: 'POST', path: `/containers/${container}/${action}` }, (res) => {
      res.resume();
      res.on('end', () => (res.statusCode < 300 ? resolve() : reject(new Error(`docker ${action}: ${res.statusCode}`))));
    });
    req.on('error', reject);
    req.end();
  });
}

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

// Deluge's Web UI JSON-RPC, signed in with its password alone. In KiB/s,
// as AutoThrottle counts; -1 is unlimited.
const deluge = {
  async call(cookie, method, params = []) {
    const res = await fetch(env.deluge.base + '/json', {
      method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: cookie },
      body: JSON.stringify({ method, params, id: 1 }),
    });
    return { cookie: (res.headers.get('set-cookie') || '').split(';')[0], body: await res.json() };
  },
  async limits() {
    const { cookie } = await this.call('', 'auth.login', [env.deluge.password]);
    const { body } = await this.call(cookie, 'core.get_config_values', [['max_download_speed', 'max_upload_speed']]);
    if (body.error) throw new Error('deluge: ' + body.error.message);
    const r = body.result;
    return { down: r.max_download_speed < 0 ? 0 : r.max_download_speed, up: r.max_upload_speed < 0 ? 0 : r.max_upload_speed };
  },
  // Changes the limits behind AutoThrottle's back.
  async unlimit() {
    const { cookie } = await this.call('', 'auth.login', [env.deluge.password]);
    const { body } = await this.call(cookie, 'core.set_config', [{ max_download_speed: -1, max_upload_speed: -1 }]);
    if (body.error) throw new Error('deluge: ' + body.error.message);
  },
};

// --- the browser ------------------------------------------------------------

(async () => {
  const browser = await puppeteer.launch({
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
    executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
  });
  const page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 1000 });
  page.setDefaultTimeout(30000);
  const pageErrors = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));
  // The nesting guard warning means a page was aimed at part of a page.
  page.on('console', (m) => { if (m.text().includes('a full page was aimed at')) pageErrors.push(m.text()); });
  const oneShell = () => page.evaluate(() => document.querySelectorAll('.at-shell').length === 1 && document.querySelectorAll('.at-rail').length === 1);
  // Counts live-block fetches, to tell pushed updates from polling.
  let liveFetches = 0;
  page.on('request', (r) => { if (r.url().includes('/dashboard/live')) liveFetches++; });
  const stream = () => page.evaluate(() => document.documentElement.dataset.liveStream || '');

  const at = () => new URL(page.url()).pathname;
  const text = (sel) => page.$eval(sel, (e) => e.innerText.replace(/\s+/g, ' ').trim()).catch(() => '');
  const navigate = (p) => page.goto(APP + p, { waitUntil: 'networkidle2' });
  async function fill(sel, value) {
    await page.$eval(sel, (e) => { e.value = ''; });
    await page.type(sel, value);
  }
  // htmx-boosted submits swap over XHR; wait for the network to settle.
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
    // Wait past the "testing" placeholder for the answer.
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
  // Add and edit forms open in a drawer over the Connections tables.
  async function openSheet(trigger) {
    if (!page.url().includes('tab=connections')) await navigate('/configuration?tab=connections');
    await settle(() => page.click(trigger));
    await page.waitForSelector('#sheet.open');
    await sleep(300); // let it finish sliding in before clicking inside it
  }
  const addServer = '.at-addrow[hx-get="/configuration/media-servers/new"]';
  const addDownloader = '.at-addrow[hx-get="/configuration/downloaders/new"]';
  const sheetOpen = () => page.$eval('#sheet', (e) => e.classList.contains('open'));
  // Names in the saved-entries table; the type dropdown names types too.
  const savedNames = () => page.$$eval('tbody tr td:first-child', (cells) => cells.map((c) => c.innerText.trim()));
  const dashboard = async () => page.evaluate(() => ({
    hero: document.querySelector('#dash-live p')?.innerText.trim() || '',
    // Outside the live block: it rides along with each refresh, out of band.
    // textContent: CSS shows the label in capitals.
    pill: document.querySelector('#dash-pill')?.textContent.trim() || '',
    downloaders: [...document.querySelectorAll('.at-dlrow-wrap')].map((r) => r.dataset.state),
    sessionTime: document.querySelector('.at-session-time')?.textContent.trim() || '',
    paused: document.querySelector('.at-session-progress')?.dataset.paused === '1',
  }));

  try {
    section('First run');
    await navigate('/');
    check(at() === '/setup', 'a fresh install opens on the setup screen', at());
    check((await page.$eval('[data-submit]', (b) => b.disabled)), 'Create account waits for both fields');
    await fill('[name=username]', 'admin');
    await fill('[name=password]', 'e2e-password');
    await page.click('[data-reveal]');
    check((await page.$eval('[name=password]', (i) => i.type)) === 'text' && (await text('[data-reveal]')) === 'Hide',
      'Show reveals the password');
    await page.click('[data-reveal]');
    await settle(() => page.click('button[type=submit]'));
    check(at() === '/', 'setup creates the account and signs in', at());
    check((await page.content()).includes('Nothing configured yet'), 'the empty Dashboard says nothing is configured');

    section('Media server');
    await openSheet(addServer);
    await page.select('#ms-form [name=type]', MEDIA.kind);
    await fill('#ms-form [name=name]', NAME);
    await fill('#ms-form [name=url]', MEDIA.internal);
    // Plex's token goes in its X-Plex-Token pane; Jellyfin and Emby take an API key.
    const keyField = PLEX ? '#ms-form [name=token]' : '#ms-form [name=api_token]';
    const goodKey = PLEX ? PLEX_TOKEN : MEDIA.apiKey;
    await fill(keyField, 'not-the-key');
    let result = await testConnection('#ms-form');
    check(result.state === 'failed' && !result.canSave, `a wrong ${PLEX ? 'token' : 'API key'} fails the test and blocks saving`, JSON.stringify(result));
    if (PLEX) {
      // Sign in with Plex, approved on plex.tv as the account owner would.
      await page.$eval(keyField, (e) => { e.value = ''; });
      await page.click('#ms-form [data-credtab="signin"]');
      await page.click('#ms-form [data-plexstart]');
      const authUrl = await page.waitForSelector('.at-plexsignin[data-authurl]').then((el) => el.evaluate((e) => e.dataset.authurl));
      const pin = new URLSearchParams(authUrl.split('#?')[1]);
      // For cleanup, which removes only this run's sign-ins while another
      // run may share the account. Without it they go once an hour old.
      try {
        fs.appendFileSync(path.join(__dirname, '.state/plex-signins'), pin.get('clientID') + '\n');
      } catch (err) {
        console.log(`      (could not note the sign-in for cleanup: ${err.message})`);
      }
      await approvePlexPin(pin.get('code'));
      const signedIn = await page.waitForSelector('.at-plexsignin[data-state="done"]', { timeout: 30000 }).catch(() => null);
      check(!!signedIn, 'Sign in with Plex picks up the approval by itself');
      // Plex closes its window once approved; the sign-in must survive that.
      // The app may already have closed it, between listing and closing.
      for (const p of await browser.pages()) if (p !== page) await p.close().catch(() => {});
      await sleep(4500);
      check(!!(await page.$('.at-plexsignin[data-state="done"]')), 'and survives the Plex window closing');
    } else {
      await fill(keyField, goodKey);
      check(!(await page.$('#ms-form .at-testresult[data-state="connected"]')), 'editing a field retires an old result');
    }
    result = await testConnection('#ms-form');
    check(result.state === 'connected' && result.canSave, `the right ${PLEX ? 'sign-in' : 'API key'} passes and allows saving`, JSON.stringify(result));
    await submit('#ms-form');
    check((await savedNames()).includes(NAME), 'the server is saved', JSON.stringify(await savedNames()));
    await sleep(300);
    check(!(await sheetOpen()), 'saving closes the drawer');

    await openSheet(addServer);
    await page.select('#ms-form [name=type]', MEDIA.kind);
    await fill('#ms-form [name=name]', `${NAME} again`);
    await fill('#ms-form [name=url]', MEDIA.internal + '/');
    await fill(keyField, goodKey);
    await testConnection('#ms-form');
    await submit('#ms-form');
    check((await text('#sheet-error .at-alert')).includes('already added'), 'adding the same server twice is refused with a reason', await text('#sheet-error .at-alert'));
    check(await sheetOpen() && (await page.$eval('#ms-form [name=name]', (e) => e.value)) === `${NAME} again`,
      'the refusal leaves the drawer open with what was typed');
    await page.keyboard.press('Escape');
    await sleep(300);
    check(!(await sheetOpen()), 'Esc closes the drawer');
    check((await savedNames()).filter((n) => n.startsWith(NAME)).length === 1, 'and nothing was added');

    section('Download clients');
    for (const [name, type, conf, down, up] of [
      ['qBittorrent', 'qbittorrent', env.qbittorrent, 1500, 300],
      ['Transmission', 'transmission', env.transmission, 900, 180],
      ['Deluge', 'deluge', env.deluge, 1200, 240],
    ]) {
      await openSheet(addDownloader);
      await page.select('#dl-form [name=type]', type);
      await fill('#dl-form [name=name]', name);
      await fill('#dl-form [name=url]', conf.internal);
      if (conf.username) await fill('#dl-form [name=username]', conf.username);
      else {
        check(await page.$eval('#dl-form [name=username]', (e) => e.disabled && !!e.closest('[hidden]')), `${name} asks for no username`);
        await fill('#dl-form [name=password]', 'wrong-password');
        result = await testConnection('#dl-form');
        check(result.state === 'failed' && !result.canSave && /the password was rejected/.test(result.text) && !/username/i.test(result.text),
          `a wrong ${name} password fails, says so, and blocks saving`, JSON.stringify(result));
        check(await page.$eval('#dl-form [name=password]', (e) => e.classList.contains('at-input--invalid')), 'and outlines the password');
      }
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
    check((await page.$$('.at-matrix-cell[aria-pressed="true"]')).length === 3, 'new pairings start as Throttle');
    for (const name of ['Transmission', 'Deluge']) {
      await page.click(`.at-matrix-cell[title="Leave ${name} at its default rate when ${NAME} plays"]`);
      await page.waitForSelector(`.at-matrix-cell[aria-pressed="false"][title="Throttle ${name} when ${NAME} plays"]`);
      check((await page.$$('.at-matrix-cell[aria-pressed="true"]')).length === 2, `a pairing can be switched off (${name})`);
      await page.click(`.at-matrix-cell[title="Throttle ${name} when ${NAME} plays"]`);
      await page.waitForSelector(`.at-matrix-cell[aria-pressed="true"][title="Leave ${name} at its default rate when ${NAME} plays"]`);
      check((await page.$$('.at-matrix-cell[aria-pressed="true"]')).length === 3, 'and back on');
    }

    section('Idle');
    await navigate('/');
    const idle = await until('the idle state', async () => {
      const d = await dashboard();
      return d.hero === 'Nothing is throttled' && d.downloaders.every((s) => s === 'normal') ? d : null;
    }, 30000);
    check(!!idle, 'the Dashboard says nothing is throttled', JSON.stringify(await dashboard()));
    const healthy = await until('Docker to call it healthy', async () => ((await dockerHealth()) === 'healthy' ? true : null), 60000);
    check(!!healthy, 'Docker\'s health check calls it healthy', await dockerHealth());
    check((await qbittorrent.limits()).down === 0, 'qBittorrent is unlimited');
    check((await transmission.limits()).down === 0, 'Transmission is unlimited');
    check((await deluge.limits()).down === 0, 'Deluge is unlimited');

    section('Live stream');
    const opened = await until('the stream to open', async () => (await stream()) === 'open', 15000);
    check(!!opened, 'the Dashboard opens a live stream', await stream());
    liveFetches = 0;
    // Longer than one 5-second poll: a page polling would show it.
    await sleep(7000);
    check(liveFetches === 0, 'with the stream up and nothing happening, the Dashboard does not poll', `${liveFetches} fetches in 7s`);
    // A tab in the background lets its stream go, and picks it up again.
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    check((await stream()) === 'closed', 'a hidden tab closes its stream', await stream());
    liveFetches = 0;
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    const reopened = await until('the stream to reopen', async () => (await stream()) === 'open', 15000);
    check(!!reopened && liveFetches >= 1, 'coming back reopens it and catches up at once', `${await stream()}, ${liveFetches} fetches`);

    section('Playback starts — watched live, no reloads');
    const player = startPlayer();
    // From qBittorrent being limited to the page saying so: the app's own latency.
    let appliedAt = 0;
    while (!appliedAt) {
      if ((await qbittorrent.limits()).down === 1500 * 1024) appliedAt = Date.now();
      else await sleep(100);
    }
    let shownAt = 0;
    while (!shownAt && Date.now() - appliedAt < 30000) {
      if ((await dashboard()).downloaders.every((s) => s === 'throttled')) shownAt = Date.now();
      else await sleep(100);
    }
    const throttled = shownAt ? await dashboard() : null;
    check(!!throttled, 'every client shows as throttled without a reload', JSON.stringify(await dashboard()));
    check(shownAt && shownAt - appliedAt < 1500, 'the page shows the throttle within 1.5s of it landing', `${shownAt - appliedAt}ms`);
    console.log(`        (${shownAt - appliedAt}ms after qBittorrent was limited)`);
    console.log(`      (shown ${shownAt - appliedAt}ms after it landed)`);
    check(/throttled/.test((await dashboard()).hero), 'the headline says what is throttled');
    check((await dashboard()).pill === 'Throttling', 'so does the status beside the title', (await dashboard()).pill);
    const q = await qbittorrent.limits();
    check(q.down === 1500 * 1024 && q.up === 300 * 1024, 'qBittorrent really is limited to 1500/300 KB/s', JSON.stringify(q));
    const t = await transmission.limits();
    check(t.down === 900 && t.up === 180, 'Transmission really is limited to 900/180 KB/s', JSON.stringify(t));
    const dl = await deluge.limits();
    check(dl.down === 1200 && dl.up === 240, 'Deluge really is limited to 1200/240 KB/s', JSON.stringify(dl));
    check((await page.content()).includes(MEDIA.itemName), 'the playing title is shown');
    const clock = () => page.$eval('.at-timer-clock', (e) => e.textContent.replace(/\s+/g, '')).catch(() => '');
    const t1 = await clock(); await sleep(2100); const t2 = await clock();
    check(t1 && t2 && t1 !== t2, 'a session timer counts while throttled', `${t1} → ${t2}`);

    const before = (await dashboard()).sessionTime;
    await sleep(4000);
    const after = (await dashboard()).sessionTime;
    check(before && after && before !== after, 'the timecode advances while playing', `${before} → ${after}`);

    section('Restart while throttled');
    await docker('stop');
    await qbittorrent.unlimit();
    await deluge.unlimit();
    await docker('start');
    const reasserted = await until('the throttle to be re-applied', async () =>
      ((await qbittorrent.limits()).down === 1500 * 1024 ? true : null), 60000);
    check(!!reasserted, 'a restart re-applies the throttle the client lost');
    const delugeReasserted = await until('Deluge\'s throttle to be re-applied', async () =>
      ((await deluge.limits()).down === 1200 ? true : null), 60000);
    check(!!delugeReasserted, 'Deluge\'s too', JSON.stringify(await deluge.limits()));
    await until('AutoThrottle to answer again', async () => ((await fetch(APP + '/login')).ok ? true : null), 30000);
    // The page that was open across the restart reconnects by itself.
    const rejoined = await until('the stream to come back', async () => (await stream()) === 'open', 30000);
    check(!!rejoined, 'the open page reconnects its stream after the restart', await stream());
    await navigate('/');
    check(at() === '/', 'the browser is still signed in after the restart', at());
    const healthyAgain = await until('Docker to call it healthy again', async () => ((await dockerHealth()) === 'healthy' ? true : null), 60000);
    check(!!healthyAgain, 'and healthy again', await dockerHealth());
    const resumed = await until('the Dashboard to show it', async () =>
      ((await dashboard()).downloaders.every((s) => s === 'throttled') ? true : null), 30000);
    check(!!resumed, 'the Dashboard shows the clients throttled again');

    section('Pause and resume');
    await player.pause();
    const paused = await until('the pause to show', async () => ((await dashboard()).paused ? await dashboard() : null), 30000);
    check(!!paused, 'the session shows as paused');
    const p1 = (await dashboard()).sessionTime; await sleep(4000); const p2 = (await dashboard()).sessionTime;
    check(p1 === p2, 'the timecode holds still while paused', `${p1} → ${p2}`);
    check((await dashboard()).downloaders.every((s) => s === 'throttled'), 'a pause keeps the throttle on');
    await player.resume();

    section('Without the stream');
    // A proxy that will not pass the stream: the page falls back to
    // polling and still keeps up.
    const blocked = await browser.newPage();
    await blocked.setViewport({ width: 1440, height: 1000 });
    await blocked.setRequestInterception(true);
    let blockedFetches = 0;
    blocked.on('request', (r) => {
      if (r.url().includes('/events')) return r.abort();
      if (r.url().includes('/dashboard/live')) blockedFetches++;
      r.continue();
    });
    await blocked.goto(APP + '/', { waitUntil: 'networkidle2' });
    await sleep(6500);
    const state = await blocked.evaluate(() => document.documentElement.dataset.liveStream || '');
    check(state !== 'open' && blockedFetches >= 1, 'with the stream blocked, the Dashboard polls instead', `${state}, ${blockedFetches} fetches in 6.5s`);
    await blocked.close();

    section('Disable throttling');
    await settle(() => page.click('form[action="/pause-toggle"] button'));
    const overridden = await until('the override', async () => ((await qbittorrent.limits()).down === 0 ? true : null), 30000);
    check(!!overridden, 'turning throttling off restores default rates while still playing');
    const delugeOverridden = await until('Deluge\'s override', async () => ((await deluge.limits()).down === 0 ? true : null), 30000);
    check(!!delugeOverridden, 'Deluge\'s too', JSON.stringify(await deluge.limits()));
    for (const p of ['/configuration', '/logs']) {
      await navigate(p);
      check(/Throttling disabled since \d{1,2}:\d{2} [AP]M/.test(await text('.at-paused')), `the banner is on ${p}`, await text('.at-paused'));
    }
    await settle(() => page.click('.at-paused form button'));
    check(at() === '/logs' && !(await page.$('.at-paused')), 'Resume on the banner stays on the page it was pressed on', at());
    await navigate('/');
    // Off and on again: the page must never nest a second sidebar.
    for (const p of ['/', '/', '/logs']) {
      if (p !== '/') await navigate(p);
      if (p === '/') await settle(() => page.click('#dash-actions form button'));
      else {
        await page.evaluate(async () => {
          const t = document.querySelector('input[name=csrf_token]').value;
          await fetch('/pause-toggle', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'csrf_token=' + encodeURIComponent(t) });
        });
        await page.waitForSelector('.at-paused', { timeout: 15000 });
      }
      const offOk = (await oneShell()) && !!(await page.$('.at-paused'));
      await settle(() => page.click('.at-paused form button'));
      check(offOk && (await oneShell()) && !(await page.$('.at-paused')) && at() === p,
        `disabling and resuming from ${p} leaves one page, one sidebar`, at());
    }
    await navigate('/');
    const back = await until('throttling to come back', async () => ((await qbittorrent.limits()).down === 1500 * 1024 ? true : null), 30000);
    check(!!back, 'turning it back on re-throttles immediately');
    const delugeBack = await until('Deluge to be throttled again', async () => ((await deluge.limits()).down === 1200 ? true : null), 30000);
    check(!!delugeBack, 'Deluge too', JSON.stringify(await deluge.limits()));

    section('Playback stops');
    // A film: nothing follows it, so the downloads come back at once.
    await player.stop();
    const movieStopped = Date.now();
    const released = await until('the release', async () => {
      const d = await dashboard();
      return d.hero === 'Nothing is throttled' ? d : null;
    }, 30000);
    check(!!released, 'the Dashboard returns to nothing throttled without a reload', JSON.stringify(await dashboard()));
    const movieWait = Math.round((Date.now() - movieStopped) / 1000);
    check(movieWait <= 15, 'at once, for a film', `${movieWait}s`);
    check((await dashboard()).pill === 'Idle', 'and the status beside the title is back to Idle', (await dashboard()).pill);
    check((await qbittorrent.limits()).down === 0, 'qBittorrent is unlimited again');
    check((await transmission.limits()).down === 0, 'Transmission is unlimited again');
    check((await deluge.limits()).down === 0, 'Deluge is unlimited again');

    section('Between episodes');
    const throttledAgain = () => until('the throttle', async () => ((await qbittorrent.limits()).down === 1500 * 1024 ? true : null), 30000);
    const releasedAgain = () => until('the release', async () => ((await dashboard()).hero === 'Nothing is throttled' ? true : null), 60000);
    const closingStretch = Math.round(EPISODE.durationMs / 1000) - 150;
    // An episode left midway was left by choice: it releases at once too.
    const midway = startPlayer(EPISODE, 60);
    check(!!(await throttledAgain()), 'an episode throttles like anything else');
    await midway.stop();
    const midwayStopped = Date.now();
    check(!!(await releasedAgain()) && Date.now() - midwayStopped <= 15000, 'one stopped midway releases at once',
      `${Math.round((Date.now() - midwayStopped) / 1000)}s`);
    // One stopped in its closing stretch may have autoplay's next coming
    // seconds later: the throttle holds, and qBittorrent itself is watched
    // all the way through.
    const ending = startPlayer(EPISODE, closingStretch);
    await throttledAgain();
    await sleep(3000); // its type and position reach AutoThrottle
    await ending.stop();
    const samples = [];
    const watch = async (ms) => {
      for (const end = Date.now() + ms; Date.now() < end; await sleep(250)) samples.push((await qbittorrent.limits()).down);
    };
    // Plex can take a few seconds to drop the session.
    let hint = '';
    for (const end = Date.now() + 20000; Date.now() < end && !/An episode just ended/.test(hint);) {
      await watch(1000);
      hint = await text('#dash-live .at-hero-reason');
    }
    check(/An episode just ended/.test(hint), 'one stopped near its end holds the throttle, and the Dashboard says why', hint);
    const nextEpisode = startPlayer(EPISODE, closingStretch);
    await watch(4000);
    const nextShown = await until('the next episode to show', async () => ((await dashboard()).hero.includes('throttled') && !(await text('#dash-live .at-hero-reason')) ? true : null), 30000);
    check(!!nextShown, 'the next episode carries on the throttle', await text('#dash-live .at-hero-reason'));
    check(samples.length > 10 && samples.every((d) => d === 1500 * 1024), 'with qBittorrent never restored in between', JSON.stringify([...new Set(samples)]));
    await sleep(3000);
    await nextEpisode.stop();
    const stoppedAt = Date.now();
    check(!!(await releasedAgain()), 'the last episode releases by itself');
    const waited = Math.round((Date.now() - stoppedAt) / 1000);
    check(waited >= 25 && waited <= 45, 'about 30 seconds after it ended', `${waited}s`);
    check((await qbittorrent.limits()).down === 0 && (await deluge.limits()).down === 0, 'and the clients are unlimited again');

    section('A download client restarting');
    // qBittorrent still coming up when rates are applied must not stay Unreachable.
    await docker('stop', 'e2e-qbittorrent');
    await docker('stop', 'e2e-deluge');
    await docker('restart');
    await until('AutoThrottle to answer again', async () => ((await fetch(APP + '/login')).ok ? true : null), 30000);
    await navigate('/configuration?tab=connections');
    const dlDot = (name) => page.evaluate((n) => {
      const row = [...document.querySelectorAll('#connections tbody tr')].find((r) => r.innerText.includes(n));
      return row ? row.querySelector('.at-st').dataset.label : '';
    }, name);
    for (const name of ['qBittorrent', 'Deluge']) {
      const red = await until(`${name} to read Unreachable`, async () => ((await dlDot(name)) === 'Unreachable' ? true : null), 60000);
      check(!!red, `a client down at startup reads Unreachable (${name})`, await dlDot(name));
    }
    await docker('start', 'e2e-qbittorrent');
    await docker('start', 'e2e-deluge');
    const cameBack = Date.now();
    for (const name of ['qBittorrent', 'Deluge']) {
      const green = await until(`${name} to come back`, async () => ((await dlDot(name)) === 'Default rates' ? true : null), 120000);
      check(!!green, `once it is back, its dot turns green on the open page, without a reload (${name})`, `${await dlDot(name)} after ${Math.round((Date.now() - cameBack) / 1000)}s`);
    }
    check((await qbittorrent.limits()).down === 0, 'and its rates were applied again');
    const logText0 = await (await fetch(APP + '/logs', { headers: { Cookie: (await page.cookies()).map((c) => `${c.name}=${c.value}`).join('; ') } })).text();
    for (const name of ['qBittorrent', 'Deluge']) {
      check((logText0.match(new RegExp(`failed to apply rate limits to ${name}`, 'g')) || []).length === 1, `the outage is logged once, not once per retry (${name})`);
    }
    // A restarted Deluge's Web UI comes back disconnected from its daemon:
    // throttling it again proves AutoThrottle connects it.
    const afterRestart = startPlayer();
    const delugeAgain = await until('the restarted Deluge to be throttled', async () => ((await deluge.limits()).down === 1200 ? true : null), 60000);
    check(!!delugeAgain, 'a restarted Deluge is throttled again', JSON.stringify(await deluge.limits()));
    await afterRestart.stop();
    const delugeFree = await until('the restarted Deluge to be released', async () => ((await deluge.limits()).down === 0 ? true : null), 60000);
    check(!!delugeFree, 'and released again', JSON.stringify(await deluge.limits()));

    section('A media server going away');
    await navigate('/');
    const serverState = () => page.$eval('.at-serverrow', (r) => r.dataset.state).catch(() => '');
    await docker('stop', `e2e-${MEDIA.kind}`);
    const down = await until('the Dashboard to notice', async () => ((await serverState()) === 'connecting' ? true : null), 45000);
    check(!!down, 'a stopped media server reads Connecting, not Idle', await serverState());
    await docker('start', `e2e-${MEDIA.kind}`);
    const reconnected = await until('it to reconnect', async () => ((await serverState()) === 'idle' ? true : null), 120000);
    check(!!reconnected, 'and Idle again once it is back, without a reload', await serverState());

    section('Local playback exemption');
    // The player runs on this machine, so the server sees local playback.
    async function setExempt(on) {
      await navigate('/configuration?tab=connections');
      await openSheet('button[hx-get^="/configuration/media-servers/"][hx-get$="/edit"]');
      const box = await page.$('#ms-form [name=skip_local_playback]');
      if ((await box.evaluate((b) => b.checked)) !== on) await box.click();
      // A server restarted moments ago can still answer 503 while it
      // finishes starting, though it already reads as Idle.
      let r = await testConnection('#ms-form');
      for (let i = 0; i < 20 && r.state !== 'connected' && /\b503\b/.test(r.text); i++) {
        await sleep(3000);
        await page.$eval('#ms-form .at-testresult', (e) => e.removeAttribute('data-state'));
        r = await testConnection('#ms-form');
      }
      if (r.state !== 'connected') throw new Error('re-testing the server failed: ' + JSON.stringify(r));
      await submit('#ms-form');
    }
    await setExempt(true);
    await navigate('/');
    const local = startPlayer();
    // Well past the ~1s a counted session takes to throttle.
    await sleep(7000);
    const d = await dashboard();
    check(d.hero === 'Nothing is throttled' && (await qbittorrent.limits()).down === 0,
      'local playback is ignored when the exemption is on', JSON.stringify(d));
    await local.stop();
    await setExempt(false);

    section('Editing');
    // Only the connection's own fields need a new test; nothing is saved here.
    await navigate('/configuration?tab=connections');
    await openSheet('button[hx-get^="/configuration/media-servers/"][hx-get$="/edit"]');
    const canSave = (form) => page.$eval(`${form} [data-submit]`, (b) => !b.disabled);
    const savedUrl = await page.$eval('#ms-form [name=url]', (e) => e.value);
    await fill('#ms-form [name=name]', `${NAME} renamed`);
    check(await canSave('#ms-form'), 'renaming a server needs no new test');
    await page.click('#ms-form [name=skip_local_playback]');
    check(await canSave('#ms-form'), 'nor does the local playback switch');
    await fill('#ms-form [name=url]', 'http://192.0.2.1:8096');
    check(!(await canSave('#ms-form')), 'changing its address does');
    await fill('#ms-form [name=url]', savedUrl);
    check(await canSave('#ms-form'), 'and changing it back does not');
    await page.keyboard.press('Escape');
    await sleep(300);
    const qbRow = await page.$$eval('button[hx-get^="/configuration/downloaders/"][hx-get$="/edit"]',
      (bs) => bs.find((b) => b.closest('tr').innerText.includes('qBittorrent')).getAttribute('hx-get'));
    await openSheet(`button[hx-get="${qbRow}"]`);
    await fill('#dl-form [name=name]', 'qBittorrent renamed');
    await fill('#dl-form [name=backup_download_limit]', '1234');
    check(await canSave('#dl-form'), 'renaming a downloader or changing its rates needs no new test');
    await fill('#dl-form [name=username]', 'someone-else');
    check(!(await canSave('#dl-form')), 'changing its username does');
    await page.keyboard.press('Escape');
    await sleep(300);
    const delugeRow = await page.$$eval('button[hx-get^="/configuration/downloaders/"][hx-get$="/edit"]',
      (bs) => bs.find((b) => b.closest('tr').innerText.includes('Deluge')).getAttribute('hx-get'));
    await openSheet(`button[hx-get="${delugeRow}"]`);
    check(await page.$eval('#dl-form [name=username]', (e) => e.disabled && !!e.closest('[hidden]')), 'editing Deluge shows no username');
    check(await canSave('#dl-form'), 'and opens ready to save');
    await fill('#dl-form [name=name]', 'Deluge renamed');
    await fill('#dl-form [name=backup_upload_limit]', '250');
    check(await canSave('#dl-form'), 'renaming Deluge or changing its rates needs no new test');
    await fill('#dl-form [name=password]', 'another-password');
    check(!(await canSave('#dl-form')), 'a new password does');
    await fill('#dl-form [name=password]', '');
    await page.$eval('#dl-form [name=password]', (e) => e.dispatchEvent(new Event('input', { bubbles: true })));
    check(await canSave('#dl-form'), 'and clearing it again does not');
    await fill('#dl-form [name=url]', 'http://192.0.2.1:8112');
    check(!(await canSave('#dl-form')), 'changing its address does');
    await page.keyboard.press('Escape');
    await sleep(300);

    section('Logs');
    // Every client's lines, not just the newest twenty.
    await navigate('/logs?per=50');
    const logText = await text('#log-panel');
    check(logText.includes(`throttled qBittorrent because ${NAME} is playing`), 'the throttle is logged with its cause');
    check(/restored default rate for qBittorrent/.test(logText), 'the restore is logged');
    check(logText.includes(`throttled Deluge because ${NAME} is playing`) && /restored default rate for Deluge/.test(logText),
      'so are Deluge\'s');
    await page.click('#log-panel form[action="/logs/clear"] button');
    await page.waitForFunction(() => document.querySelectorAll('.at-logrow').length === 0);
    check(true, 'Clear empties the log, with no confirmation');
    const p2player = startPlayer();
    const relogged = await until('a new event to appear live', async () =>
      (await page.$$eval('.at-logrow', (r) => r.length)) > 0 ? true : null);
    check(!!relogged, 'a new event appears without a reload after clearing');
    await p2player.stop();

    section('Delete');
    await navigate('/configuration?tab=connections');
    for (const name of ['Transmission', 'Deluge']) {
      const row = await page.$$eval('button[hx-get^="/configuration/downloaders/"][hx-get$="/edit"]',
        (bs, n) => bs.find((b) => b.closest('tr').innerText.includes(n)).getAttribute('hx-get'), name);
      await openSheet(`button[hx-get="${row}"]`);
      await page.click('#sheet .at-sheet-bar .at-btn--critical');
      await page.waitForSelector('dialog[open]');
      check((await text('dialog[open] .at-dialog-title')) === `Delete ${name}?`, `Delete asks first, naming what goes (${name})`, await text('dialog[open] .at-dialog-title'));
      await settle(() => page.click('dialog[open] .at-btn--danger'));
      check(!(await savedNames()).includes(name) && !(await page.$('#sheet.open')), 'confirming removes it and closes the drawer', JSON.stringify(await savedNames()));
    }

    section('Navigation');
    await navigate('/');
    await settle(() => page.click('.at-nav-item[href="/logs"]'));
    check(at() === '/logs' && (await page.$$('.at-shell')).length === 1, 'moving between pages swaps cleanly', at());
    check((await page.title()) === 'Logs', 'the tab is named after the page', await page.title());
    const missing = await page.goto(APP + '/no-such-page', { waitUntil: 'networkidle2' });
    check(missing.status() === 404 && (await page.title()) === 'Page not found', 'an unknown address gets the 404 page', `${missing.status()} ${await page.title()}`);
    check((await page.$$('.at-shell')).length === 0 && (await text('.at-nf-msg')) === 'no route for GET /no-such-page',
      'it stands alone and names the address', await text('.at-nf-msg'));
    const shownTime = await text('.at-nf-time');
    await navigate('/logs');
    const logged = await page.$$eval('.at-logrow', (rows) => rows.map((r) => ({
      time: r.querySelector('.at-logrow-time').textContent.trim(),
      level: r.dataset.level,
      msg: r.querySelector('.at-logrow-msg').textContent.trim(),
    })).find((r) => r.msg === 'no route for GET /no-such-page'));
    check(!!logged && logged.level === 'warn' && logged.time === shownTime, 'the 404 is in the log as shown, at the same time', JSON.stringify(logged) + ' vs ' + shownTime);
    await page.goto(APP + '/no-such-page', { waitUntil: 'networkidle2' });
    await settle(() => page.click('.at-nf-actions .at-btn--primary'));
    check(at() === '/' && (await page.title()) === 'AutoThrottle' && (await page.$$('.at-shell')).length === 1,
      'and its button goes back to the Dashboard', `${at()} ${await page.title()}`);

    section('Signed out');
    const anon = await (await browser.createBrowserContext()).newPage();
    await anon.setViewport({ width: 1440, height: 1000 });
    const anonAt = () => new URL(anon.url()).pathname + new URL(anon.url()).search;
    const lost = await anon.goto(APP + '/no-such-page', { waitUntil: 'networkidle2' });
    const anonButtons = await anon.$$eval('.at-nf-actions a', (as) => as.map((a) => a.textContent.trim()));
    check(lost.status() === 404 && JSON.stringify(anonButtons) === '["Log in"]', 'a dead link signed out gets the 404 with only Log in', `${lost.status()} ${JSON.stringify(anonButtons)}`);
    await Promise.all([anon.waitForNavigation(), anon.click('.at-nf-actions a')]);
    await anon.type('[name=username]', 'admin');
    await anon.type('[name=password]', 'e2e-password');
    await Promise.all([anon.waitForNavigation(), anon.click('button[type=submit]')]);
    check(anonAt() === '/no-such-page' && (await anon.$$('.at-nf-actions a')).length === 2, 'signing in from it comes back to the same address', anonAt());
    const anon2 = await (await browser.createBrowserContext()).newPage();
    await anon2.goto(APP + '/logs?level=warn', { waitUntil: 'networkidle2' });
    check(new URL(anon2.url()).pathname === '/login', 'a real page signed out asks for a sign-in first', anon2.url());
    await anon2.type('[name=username]', 'admin');
    await anon2.type('[name=password]', 'e2e-password');
    await Promise.all([anon2.waitForNavigation(), anon2.click('button[type=submit]')]);
    check(new URL(anon2.url()).pathname + new URL(anon2.url()).search === '/logs?level=warn', 'and lands on that page after', anon2.url());
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
