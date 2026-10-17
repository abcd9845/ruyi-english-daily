#!/usr/bin/env node
// Headless-Chrome check of the generic practice page (/m/practice/?set=<id>),
// driven over --remote-debugging-pipe (Node built-ins only). Usage:
//   BROWSER_CODE=<family code> [BROWSER_FLOW=full|archive] node test/browser-practice.js <base-url> <out-dir> [chrome-binary]
// BROWSER_FLOW=full (default): page checks + submit flow against a site whose brain GRADES (mode graded).
// BROWSER_FLOW=archive: submit flow against a site WITHOUT grading (mode archive_only): the page must never say the brain recorded it.
// BROWSER_FLOW=contract (needs MOCK_URL = the mock brain, for its /_mock/bump-rev hook): final-contract states: rev, recorded:false, errors, brain picks.
// BROWSER_FLOW=notready: the brain's grade endpoint is not deployed (404): page must say archive-only, never recorded.
// BROWSER_FLOW=smoke: the local test set ?set=smoke-test (needs a sets file with it): answer, 检查, submit; archive-only wording, never 已提交到大脑.
// BROWSER_FLOW=dailylimit (needs the mock brain with the real daily cap + a site with grading): 「今天约还剩N次」, the confirm
// dialog, 3 successes then a 429 daily_cap (archived, not recorded), rate_limited wording, 「不入大脑」 badges.
// BROWSER_FLOW=queue (needs MOCK_URL = a mock brain started with MOCK_QUEUE=1, and a site with RUYI_QUEUE_ENABLED=1): the AI first-pass
// status under each queued answer: 待解答 → AI 判卷中 → AI初评·待家长核 (full text, escaped), timeout copy, purged, polling schedule, reload.
// Against the mock brain's synthetic questions. Logs in through the real form, fills
// answers, reloads, clears; writes mobile-viewport screenshots to <out-dir>.
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const base = process.argv[2], outDir = process.argv[3], chromeBin = process.argv[4] || 'google-chrome';
const code = process.env.BROWSER_CODE || '';
const flow = process.env.BROWSER_FLOW || 'full';
if (!base || !outDir || !code) { console.error('usage: BROWSER_CODE=… node browser.js <base> <outdir> [chrome]'); process.exit(2); }
fs.mkdirSync(outDir, { recursive: true });

const prof = fs.mkdtempSync(path.join(os.tmpdir(), 'ruyi-chrome-'));
const chrome = spawn(chromeBin, ['--headless=new', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage', '--hide-scrollbars',
  '--remote-debugging-pipe', '--user-data-dir=' + prof, 'about:blank'], { stdio: ['ignore', 'ignore', 'ignore', 'pipe', 'pipe'] });
const toChrome = chrome.stdio[3], fromChrome = chrome.stdio[4];
let nextId = 1; const pending = new Map(); let buf = '';
fromChrome.on('data', d => {
  buf += d.toString('utf8');
  let i;
  while ((i = buf.indexOf('\0')) >= 0) {
    const msg = JSON.parse(buf.slice(0, i)); buf = buf.slice(i + 1);
    if (msg.id && pending.has(msg.id)) { const { res, rej } = pending.get(msg.id); pending.delete(msg.id); msg.error ? rej(new Error(msg.error.message)) : res(msg.result); }
  }
});
function send(method, params = {}, sessionId) {
  const id = nextId++;
  return new Promise((res, rej) => { pending.set(id, { res, rej }); toChrome.write(JSON.stringify({ id, method, params, sessionId }) + '\0'); });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));
let sid;
const ev = async expr => {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sid);
  if (r.exceptionDetails) throw new Error('page error: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
};
async function waitFor(expr, ms = 15000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { try { if (await ev(expr)) return true; } catch (_) {} await sleep(150); }
  return false;
}
async function shot(name, clip) {
  const r = await send('Page.captureScreenshot', { format: 'png', ...(clip ? { clip, captureBeyondViewport: true } : {}) }, sid);
  fs.writeFileSync(path.join(outDir, name), Buffer.from(r.data, 'base64'));
}
const results = []; let failed = 0;
const check = (name, ok, detail) => { results.push({ name, ok: !!ok, detail }); if (!ok) failed++; console.log((ok ? 'PASS ' : 'FAIL ') + name + (detail ? ' — ' + detail : '')); };

async function shotEl(name, sel) {
  const r = await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return null; const b = e.getBoundingClientRect(); return {x: Math.max(0, b.left + scrollX - 6), y: Math.max(0, b.top + scrollY - 6), width: Math.min(innerWidth, b.width + 12), height: b.height + 12, scale: 1}; })()`);
  if (r) await shot(name, r);
}
const nav = async url => { await send('Page.navigate', { url }, sid); await sleep(300); };


const fillDemo = () => ev(`(() => {
  const set = (el, v) => { el.value = v; el.dispatchEvent(new Event('input', {bubbles: true})); };
  const b = [...document.querySelectorAll('#q1 input.blank')]; set(b[0], '5/4'); set(b[2], '-3');
  const r = document.querySelectorAll('#q2 input[type=radio]')[1]; r.checked = true; r.dispatchEvent(new Event('change', {bubbles: true}));
  set(document.querySelectorAll('#q3 input.blank')[1], 'y=1'); return 1; })()`);
// count grade requests / capture their bodies; optionally delay or fail the first one
const hookFetch = (delayMs, failFirst) => ev(`(() => {
  window.__bodies = []; window.__fail = ${failFirst ? 'true' : 'false'};
  if (!window.__origFetch) window.__origFetch = window.fetch;
  const of = window.__origFetch;
  window.fetch = (u, o) => {
    if (!String(u).includes('/grade')) return of(u, o);
    window.__bodies.push(o && o.body);
    if (window.__fail) { window.__fail = false; return Promise.reject(new TypeError('offline')); }
    return new Promise(r => setTimeout(r, ${delayMs})).then(() => of(u, o));
  };
  return 1; })()`);
const gateLogin = async () => {
  await ev(`document.getElementById('gateInput').value=${JSON.stringify(code)}; document.getElementById('gateForm').requestSubmit(); 1`);
};
const bodyText = () => ev(`document.body.innerText`);

async function finish() {
  console.log(`\n${results.length - failed}/${results.length} browser checks passed`);
  chrome.kill('SIGKILL'); await sleep(400); try { fs.rmSync(prof, { recursive: true, force: true }); } catch (_) {}
  process.exit(failed ? 1 : 0);
}

// Submit flow shared by both modes. graded=true: the brain confirms; false: archive-only.
async function submitFlow(base, graded, tag) {
  const cards = `document.querySelectorAll('#qs section.card').length`;
  await nav(base + '/m/practice/?set=demo-1');
  if (await ev(`getComputedStyle(document.getElementById('gate')).display==='flex'`)) await gateLogin();
  check(tag + 'demo set loads for the submit flow', await waitFor(`${cards}===3`));
  await ev(`localStorage.clear(); 1`);
  await send('Page.reload', {}, sid);
  await waitFor(`${cards}===3`);

  // nothing answered: message, no request
  await hookFetch(0, false);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await sleep(300);
  check(tag + 'submitting nothing shows a hint and sends no request', (await ev(`document.getElementById('submitMsg').textContent`)).includes('还没有作答') && (await ev(`window.__bodies.length`)) === 0);

  // slow request: the button is disabled and says 提交中…, a second click does not send a second request
  await fillDemo(); await sleep(400);
  await hookFetch(900, false);
  await ev(`document.getElementById('btnSubmit').click(); document.getElementById('btnSubmit').click(); 1`);
  const busy = await ev(`({dis: document.getElementById('btnSubmit').disabled, text: document.getElementById('btnSubmit').textContent, n: window.__bodies.length, clear: document.getElementById('btnClear').disabled})`);
  check(tag + 'while submitting the button is disabled (提交中…) and a double click sends one request', busy.dis && busy.text === '提交中…' && busy.n === 1 && busy.clear, JSON.stringify(busy));
  check(tag + 'result card appears', await waitFor(`getComputedStyle(document.getElementById('resultCard')).display==='block'`));
  await sleep(300);
  const sent = JSON.parse(await ev(`window.__bodies[0]`));
  // rev comes from the brain's lookup (the mock sends rev 1); also on the archive-only site: rev comes from lookup, not from grading
  const rv = {rev: 1};
  check(tag + 'request carries client_attempt_id (uuid), answered_at (fixed per id), and only qref/answer(/rev) rows; multi-key answers are arrays/strings by type',
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(sent.client_attempt_id) && Object.keys(sent).sort().join() === 'answered_at,answers,client_attempt_id' && !isNaN(Date.parse(sent.answered_at)) &&
    JSON.stringify(sent.answers) === JSON.stringify([
      {qref: 'example.set:math:1', answer: ['5/4', '', '-3'], ...rv}, {qref: 'example.set:math:2', answer: 'B', ...rv}, {qref: 'example.set:math:3', answer: ['', 'y=1'], ...rv}]), JSON.stringify(sent));
  const res = await ev(`(() => ({
    h: document.querySelector('#resultCard h2').textContent, sum: document.querySelector('#resultCard .sum').textContent,
    vd: [1, 2, 3].map(n => { const v = document.querySelector('#q' + n + ' .vd'); return v ? v.textContent + '|' + v.className : null; }),
    locked: [...document.querySelectorAll('#qs input,#qs textarea')].every(e => e.disabled),
    again: !!document.getElementById('btnAgain'), submitHidden: getComputedStyle(document.getElementById('submitCard')).display === 'none',
    page: document.body.innerText }))()`);
  if (graded) {
    check(tag + 'graded mode: 「已提交到大脑」 with the brain-confirmed summary', res.h === '已提交到大脑' && res.sum === '共 3 题 · 答对 1 · 答错 1 · 待核对 1 · 未作答 0', res.h + ' / ' + res.sum);
    check(tag + 'graded mode: per-question badges (wrong, right, pending) and no answer key anywhere',
      res.vd[0].startsWith('✗ 不对|vd wrong') && res.vd[1].startsWith('✓ 答对了|vd ok') && res.vd[2].startsWith('待核对|vd pend') && !/正确答案|解析|MOCK-ANSWER/.test(res.page), JSON.stringify(res.vd));
  } else {
    check(tag + 'archive-only: heading says 已提交（暂未判分）', res.h === '已提交（暂未判分）' && res.sum === '共 3 题 · 已作答 3 · 未作答 0', res.h + ' / ' + res.sum);
    check(tag + 'archive-only: every question is 待核对 — no ✓/✗ verdict at all', res.vd.every(v => v && v.startsWith('待核对（不入大脑）|vd pend')) && !/[✓✗]/.test(res.page), JSON.stringify(res.vd));
    check(tag + 'archive-only: page never says 已记入大脑 / 已提交到大脑; every badge and the card say 不入大脑', !res.page.includes('已记入大脑') && !res.page.includes('已提交到大脑') && res.vd.every(v => v.startsWith('待核对（不入大脑）|')) && res.page.includes('暂不入大脑'), 'text scanned: ' + res.page.length + ' chars ' + JSON.stringify(res.vd));
  }
  check(tag + 'inputs locked after submitting, 再做一次 offered, submit button hidden', res.locked && res.again && res.submitHidden);
  const ls = await ev(`JSON.parse(localStorage.getItem('ruyi-practice-demo-1-v1'))`);
  check(tag + 'draft keeps client_attempt_id and the stored result (no family code, no tokens)', ls.cid === sent.client_attempt_id && ls.res && ls.res.client_attempt_id === ls.cid && !JSON.stringify(ls).includes(code));
  await shot('practice-result-' + (graded ? 'graded' : 'archive') + '.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  await ev(`document.getElementById('resultCard').scrollIntoView(); 1`);
  await sleep(200);
  await shot('practice-result-card-' + (graded ? 'graded' : 'archive') + '.png');

  // reload: the result is restored from the draft, nothing is re-sent
  await send('Page.reload', {}, sid);
  check(tag + 'after reload the result is shown again without a new request', await waitFor(`${cards}===3 && getComputedStyle(document.getElementById('resultCard')).display==='block'`) &&
    (await ev(`performance.getEntriesByType('resource').filter(r => r.name.includes('/grade')).length`)) === 0);

  // 再做一次: back to a blank form with a new attempt
  await ev(`document.getElementById('btnAgain').click(); 1`);
  await sleep(300);
  const again = await ev(`({ls: localStorage.getItem('ruyi-practice-demo-1-v1'), vals: [...document.querySelectorAll('#qs input.blank')].map(e => e.value).join(''),
    locked: [...document.querySelectorAll('#qs input,#qs textarea')].some(e => e.disabled), submit: getComputedStyle(document.getElementById('submitCard')).display, result: getComputedStyle(document.getElementById('resultCard')).display, vd: document.querySelectorAll('.vd').length, btnOk: !document.getElementById('btnSubmit').disabled && document.getElementById('btnSubmit').textContent === '提交'})`);
  check(tag + '「再做一次」 clears answers, result and attempt id; form is editable again', again.ls === null && again.vals === '' && !again.locked && again.submit === 'block' && again.result === 'none' && again.vd === 0 && again.btnOk, JSON.stringify(again));

  // network failure: answers stay, the retry re-sends the SAME client_attempt_id
  await fillDemo(); await sleep(400);
  await hookFetch(0, true);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check(tag + 'offline submit: message, button usable again, answers kept', await waitFor(`document.getElementById('submitMsg').textContent.includes('连不上练习站服务') && !document.getElementById('btnSubmit').disabled`) &&
    (await ev(`document.querySelectorAll('#q1 input.blank')[0].value`)) === '5/4');
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check(tag + 'retry succeeds', await waitFor(`getComputedStyle(document.getElementById('resultCard')).display==='block'`));
  const ids = JSON.parse(await ev(`JSON.stringify(window.__bodies.map(b => JSON.parse(b).client_attempt_id))`));
  check(tag + 'the retry reused the same client_attempt_id (idempotent)', ids.length === 2 && ids[0] === ids[1], JSON.stringify(ids));
  const ats = JSON.parse(await ev(`JSON.stringify(window.__bodies.map(b => JSON.parse(b).answered_at))`));
  check(tag + 'the retry reused the same answered_at (the brain compares it on a replay)', ats.length === 2 && ats[0] === ats[1] && !!ats[0], JSON.stringify(ats));
  // changing the answers after a failed attempt gives a new id
  await ev(`document.getElementById('btnAgain').click(); 1`);
  await sleep(200);
  await fillDemo(); await sleep(300);
  await hookFetch(0, true);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`document.getElementById('submitMsg').textContent.includes('连不上')`);
  await ev(`(() => { const e = document.querySelectorAll('#q1 input.blank')[1]; e.value = '9'; e.dispatchEvent(new Event('input', {bubbles: true})); return 1; })()`);
  await sleep(300);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`getComputedStyle(document.getElementById('resultCard')).display==='block'`);
  const ids2 = JSON.parse(await ev(`JSON.stringify(window.__bodies.map(b => JSON.parse(b).client_attempt_id))`));
  check(tag + 'edited answers after a failed attempt use a fresh client_attempt_id', ids2.length === 2 && ids2[0] !== ids2[1], JSON.stringify(ids2));
  await ev(`document.getElementById('btnAgain').click(); 1`);
}


// fake the FIRST grade request's reply (status + JSON body); later requests go through
const hookFetchReply = (status, body) => ev(`(() => {
  window.__bodies = []; window.__fake = {status: ${status}, body: ${JSON.stringify(body)}};
  if (!window.__origFetch) window.__origFetch = window.fetch;
  const of = window.__origFetch;
  window.fetch = (u, o) => {
    if (!String(u).includes('/grade')) return of(u, o);
    window.__bodies.push(o && o.body);
    if (window.__fake) { const f = window.__fake; window.__fake = null; return Promise.resolve(new Response(JSON.stringify(f.body), {status: f.status, headers: {'Content-Type': 'application/json'}})); }
    return of(u, o);
  };
  return 1; })()`);
const bumpRev = qref => new Promise((res, rej) => {
  const u = new URL(process.env.MOCK_URL + '/_mock/bump-rev?qref=' + encodeURIComponent(qref));
  const rq = require('http').request(u, { method: 'POST' }, r => { r.resume(); r.on('end', res); });
  rq.on('error', rej); rq.end();
});
const resultInfo = () => ev(`(() => ({
  shown: getComputedStyle(document.getElementById('resultCard')).display === 'block',
  h: (document.querySelector('#resultCard h2') || {}).textContent, page: document.body.innerText,
  vd: [...document.querySelectorAll('#qs .vd')].map(v => v.textContent),
  msg: document.getElementById('submitMsg').textContent }))()`);
const loginAndLoad = async (url, n) => {
  await nav(url);
  if (await waitFor(`getComputedStyle(document.getElementById('gate')).display==='flex'`, 3000)) await gateLogin();
  return waitFor(`document.querySelectorAll('#qs section.card').length===${n}`);
};


// S1: the AI first-pass status of queued answers (mock brain with MOCK_QUEUE=1; site with the queue on).
const mockQ = (op, params) => new Promise((res, rej) => {
  const u = new URL(process.env.MOCK_URL + '/_mock/queue/' + op + '?' + new URLSearchParams(params));
  const rq = require('http').request(u, { method: 'POST' }, r => { r.resume(); r.on('end', res); });
  rq.on('error', rej); rq.end();
});
async function queueFlow(base) {
  const resultShown = `getComputedStyle(document.getElementById('resultCard')).display==='block'`;
  const cards = n => `document.querySelectorAll('#qs section.card').length===${n}`;
  const Q3 = 'example.set:math:3';
  const st3 = () => ev(`(() => { const c = document.querySelector('#qs section[data-qref="${Q3}"] .qst'); return c ? c.getAttribute('data-ui') : null; })()`);
  const waitUI = (ui, ms) => waitFor(`(() => { const c = document.querySelector('#qs section[data-qref="${Q3}"] .qst'); return !!c && c.getAttribute('data-ui') === ${JSON.stringify(ui)}; })()`, ms || 15000);
  // the real schedule is 15 s / 60 s: scale every long timer down so the run is quick (the real numbers are asserted below)
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `(() => { const st = window.setTimeout; window.setTimeout = (f, d, ...a) => st(f, d >= 15000 ? d / 50 : d, ...a); })();` }, sid);
  check('[queue] demo set loads', await loginAndLoad(base + '/m/practice/?set=demo-1', 3));
  await ev(`localStorage.clear(); 1`);
  await send('Page.reload', {}, sid); await waitFor(cards(3));
  check('[queue] polling schedule: 15 s for the first 5 minutes, then 60 s', await ev(`(() => { const d = window.__ruyiQ.delay; return d(0) === 15000 && d(299999) === 15000 && d(300000) === 60000 && d(3600000) === 60000; })()`));

  const submitOnce = async () => {
    await fillDemo(); await sleep(400);
    await ev(`window.confirm = () => true; 1`);
    await ev(`document.getElementById('btnSubmit').click(); 1`);
    return waitFor(resultShown);
  };
  // --- 1. submit: the unkeyed answer (q3, pending_key) is queued; graded ones are not
  check('[queue] submit: result card appears', await submitOnce());
  check('[queue] the queued answer (q3) shows a status box right away (待解答 / 排队 / AI 判卷中)', await waitFor(`!!document.querySelector('#qs section[data-qref="${Q3}"] .qst')`));
  check('[queue] graded answers (q1, q2) get NO status box', await ev(`document.querySelectorAll('#qs .qst').length === 1`));
  check('[queue] first status is waiting: 待解答（排队中），预计 24 小时内出结果', await waitUI('waiting') && (await ev(`document.querySelector('#qs .qst .qst-l').textContent`)).includes('预计 24 小时内出结果'), String(await st3()));
  check('[queue] banner under the heading explains the wait', await waitFor(`!!document.getElementById('qBanner') && document.getElementById('qBanner').textContent.includes('还在等 1 题')`));
  check('[queue] no upload control and no reanalyze/re-run button anywhere (S2/S3 are not part of S1)',
    await ev(`document.querySelectorAll('input[type=file]').length === 0 && ![...document.querySelectorAll('button')].some(b => /重新分析|重判|重新判|reanaly/i.test(b.textContent))`));
  check('[queue] the badge of a queued answer does not say 不入大脑', await ev(`!document.querySelector('#qs section[data-qref="${Q3}"] .vd').textContent.includes('不入大脑')`));
  await shotEl('queue-waiting.png', '#q3');
  // --- 2. brain claims it -> AI 判卷中 (the page polls on its own)
  await mockQ('advance', { qref: Q3, to: 'claimed' });
  check('[queue] claimed -> AI 判卷中 (picked up by the poll, no reload)', await waitUI('ai_working'));
  // --- 3. first pass with full text; hostile text is shown as text
  const evil = '12 <img src=x onerror="window.__xss=1"> $x^2$';
  await mockQ('advance', { qref: Q3, to: 'compared', answer: evil, rationale: '第一步 <b>粗体</b>\n第二步 $\\dfrac{1}{2}$', compare: 'mismatch' });
  check('[queue] compared -> AI初评·待家长核', await waitUI('ai_first_pass'));
  const fp = await ev(`(() => { const q = document.querySelector('#qs section[data-qref="${Q3}"] .qst'); return {label: q.querySelector('.qst-l').textContent, box: (q.querySelector('.aibox')||{}).innerText || '', imgs: q.querySelectorAll('img').length, bold: q.querySelectorAll('b').length, xss: window.__xss === 1, banner: document.getElementById('qBanner').textContent, cmp: (q.querySelector('.qst-c')||{}).textContent}; })()`);
  check('[queue] first-pass label and the FULL AI text (answer + rationale) are shown, labelled AI初评·待家长核', fp.label === 'AI 初评 · 待家长核' && fp.box.includes('AI初评·待家长核') && fp.box.includes('AI 的答案') && fp.box.includes('12') && fp.box.includes('第一步') && fp.box.includes('第二步'), JSON.stringify(fp));
  check('[queue] AI text is escaped: no injected <img>, <b>, no script ran', fp.imgs === 0 && fp.bold === 0 && !fp.xss, JSON.stringify([fp.imgs, fp.bold, fp.xss]));
  check('[queue] math in the AI text is typeset (KaTeX)', await waitFor(`document.querySelectorAll('#qs .aibox .katex').length >= 1`));
  check('[queue] the mismatch note and the banner (AI 初评已出 1 题（待家长核）)', (fp.cmp || '').includes('不一致') && fp.banner.includes('AI 初评已出 1 题（待家长核）'), fp.banner);
  await shotEl('queue-firstpass.png', '#q3');
  // --- 4. everything processed: polling stops (no more GET .../queue)
  await sleep(1500);
  await ev(`(() => { window.__qreq = 0; const of = window.fetch; window.fetch = (u, o) => { if (String(u).includes('/queue')) window.__qreq++; return of(u, o); }; return 1; })()`);
  await sleep(2500);
  check('[queue] all processed -> the page stops polling (0 requests in 2.5 s even at the scaled speed)', (await ev(`window.__qreq`)) === 0 && (await ev(`window.__ruyiQ.state().active`)) === false);
  // --- 5. reload keeps everything (state comes from the site, not from the draft)
  await send('Page.reload', {}, sid); await waitFor(cards(3));
  check('[queue] after a reload the first-pass text is back (status is re-read, nothing re-queued)', await waitUI('ai_first_pass') && await ev(`document.querySelector('#qs .aibox').innerText.includes('第二步')`));
  // --- 6. second submission: hidden tab does not poll; timeout copy
  await ev(`document.getElementById('btnAgain').click(); 1`);
  await waitFor(`document.querySelectorAll('#qs .qst').length === 0`);
  check('[queue] 再做一次: status boxes are gone with the old result', await ev(`document.querySelectorAll('#qs .qst').length === 0 && !document.getElementById('qBanner')`));
  check('[queue] second submit succeeds', await submitOnce() && await waitUI('waiting'));
  await ev(`Object.defineProperty(document, 'visibilityState', {configurable: true, get: () => 'hidden'}); 1`);
  await mockQ('advance', { qref: Q3, to: 'expired' });
  await sleep(2500);
  check('[queue] a hidden tab does not poll (status unchanged after the brain moved on)', (await st3()) === 'waiting');
  await ev(`Object.defineProperty(document, 'visibilityState', {configurable: true, get: () => 'visible'}); document.dispatchEvent(new Event('visibilitychange')); 1`);
  check('[queue] back in view: expired -> 「超过 24 小时仍没有结果，已转人工」', await waitUI('expired') && (await ev(`document.querySelector('#qs section[data-qref="${Q3}"] .qst-l').textContent`)) === '超过 24 小时仍没有结果，已转人工');
  // --- 7. third submission: purged = deleted, no text
  await ev(`document.getElementById('btnAgain').click(); 1`);
  await waitFor(`document.querySelectorAll('#qs .qst').length === 0`);
  check('[queue] third submit succeeds', await submitOnce() && await waitUI('waiting'));
  await mockQ('advance', { qref: Q3, to: 'purged' });
  check('[queue] purged -> 「家长已删除这道题的解析」 and no AI text', await waitUI('purged') && await ev(`document.querySelectorAll('#qs .aibox').length === 0 && document.querySelector('#qs .qst-l').textContent === '家长已删除这道题的解析'`));
  check('[queue] no family code / token in storage or page', !(await ev(`JSON.stringify(Object.assign({}, localStorage)) + document.body.innerText`)).includes(code));
  return finish();
}

// Final-contract states in a real browser, against the mock brain (grade token configured, auto sets on).
async function contractFlow(base) {
  const cards = n => `document.querySelectorAll('#qs section.card').length===${n}`;
  const resultShown = `getComputedStyle(document.getElementById('resultCard')).display==='block'`;
  check('[contract] demo set loads', await loginAndLoad(base + '/m/practice/?set=demo-1', 3));
  await ev(`localStorage.clear(); 1`);
  await send('Page.reload', {}, sid); await waitFor(cards(3));

  // 1. rev changed on the brain after the page was served -> 409 rev_mismatch -> "题目已更新", page reloads, answers stay, new rev is sent
  await fillDemo(); await sleep(400);
  await hookFetch(0, false);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(resultShown);
  const first = JSON.parse(await ev(`window.__bodies[0]`));
  check('[contract] rev from the brain lookup is sent with every answer', first.answers.every(a => a.rev === 1), JSON.stringify(first.answers.map(a => a.rev)));
  check('[contract] graded: 已提交到大脑 (all three recorded:true)', (await resultInfo()).h === '已提交到大脑');
  await ev(`document.getElementById('btnAgain').click(); 1`); await sleep(300);
  await fillDemo(); await sleep(400);
  await bumpRev('example.set:math:2');
  await hookFetch(0, false);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check('[contract] stale rev: message 题目已更新，请刷新 and the page re-fetched the questions', await waitFor(`document.getElementById('submitMsg').textContent.includes('题目已更新')`) && await waitFor(cards(3)));
  const kept = await ev(`({q1: [...document.querySelectorAll('#q1 input.blank')].map(e => e.value).join('|'), q2: document.querySelectorAll('#q2 input:checked').length, shown: ${resultShown}})`);
  check('[contract] after the refresh the answers are still there and nothing was marked submitted', kept.q1 === '5/4||-3' && kept.q2 === 1 && !kept.shown, JSON.stringify(kept));
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check('[contract] resubmitting after the refresh succeeds with the new rev and a NEW client_attempt_id', await waitFor(resultShown));
  const bodies = JSON.parse(await ev(`JSON.stringify(window.__bodies.map(b => JSON.parse(b)))`));
  check('[contract] ... q2 now carries rev 2; ids differ between the refused and the accepted request',
    bodies.length === 2 && bodies[1].answers.find(a => a.qref === 'example.set:math:2').rev === 2 && bodies[0].client_attempt_id !== bodies[1].client_attempt_id, JSON.stringify(bodies.map(b => [b.client_attempt_id.slice(0, 8), b.answers.map(a => a.rev)])));
  await ev(`document.getElementById('btnAgain').click(); 1`); await sleep(300);

  // 2. idempotency_conflict (our bug path): the page drops its id and resubmits once with a NEW id
  await fillDemo(); await sleep(400);
  await hookFetchReply(409, {error: 'idempotency_conflict', message: '提交编号冲突', retry_new_id: true});
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check('[contract] idempotency_conflict: automatic single resubmit succeeds', await waitFor(resultShown));
  const ib = JSON.parse(await ev(`JSON.stringify(window.__bodies.map(b => JSON.parse(b)))`));
  check('[contract] ... the second request used a new client_attempt_id (same answers)', ib.length === 2 && ib[0].client_attempt_id !== ib[1].client_attempt_id && JSON.stringify(ib[0].answers) === JSON.stringify(ib[1].answers), JSON.stringify(ib.map(b => b.client_attempt_id.slice(0, 8))));
  await ev(`document.getElementById('btnAgain').click(); 1`); await sleep(300);
  // a conflict that keeps coming back is not retried forever
  await fillDemo(); await sleep(400);
  await ev(`(() => { window.__bodies = []; const of = window.__origFetch; window.fetch = (u, o) => { if (!String(u).includes('/grade')) return of(u, o); window.__bodies.push(o.body); return Promise.resolve(new Response(JSON.stringify({error: 'idempotency_conflict', message: '提交编号冲突', retry_new_id: true}), {status: 409, headers: {'Content-Type': 'application/json'}})); }; return 1; })()`);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`document.getElementById('submitMsg').textContent.includes('冲突') && !document.getElementById('btnSubmit').disabled`);
  const nb = await ev(`window.__bodies.length`);
  check('[contract] a repeating idempotency_conflict stops after one automatic retry and shows a message', nb === 2 && !(await ev(resultShown)), 'requests=' + nb);

  // 3. daily cap (429): message, answers kept, not marked submitted
  await hookFetchReply(429, {error: 'daily_cap', message: '今天这题的次数已用完，已留档，未记入大脑。（每题每天只能提交 3 次，明天再提交才会记入大脑。）'});
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`document.getElementById('submitMsg').textContent.includes('次数已用完')`);
  const cap = await resultInfo();
  check('[contract] 429 daily_cap: 「今天这题的次数已用完，已留档，未记入大脑」, answers stay, no result card', cap.msg.includes('今天这题的次数已用完，已留档，未记入大脑') && !cap.shown && (await ev(`document.querySelectorAll('#q1 input.blank')[0].value`)) === '5/4', cap.msg);
  await ev(`localStorage.clear(); 1`);

  // 4. recorded:false (unsupported): the page must NOT say it was submitted to the brain
  check('[contract] free-form set loads', await loginAndLoad(base + '/m/practice/?set=free-3', 1));
  await ev(`(() => { const t = document.querySelector('#q1 textarea.ans'); t.value = '1/2+1/3=5/6'; t.dispatchEvent(new Event('input', {bubbles: true})); return 1; })()`);
  await sleep(400);
  await hookFetch(0, false);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(resultShown);
  const un = await resultInfo();
  check('[contract] unsupported (recorded:false): heading 已提交（仅留档，未记入大脑）, badge 不自动判分, never 已提交到大脑',
    un.h === '已提交（仅留档，未记入大脑）' && un.vd.join() === '不自动判分（不入大脑）' && !un.page.includes('已提交到大脑') && !un.page.includes('已记入大脑'), un.h + ' / ' + un.vd.join());
  await shot('practice-result-unsupported.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  await ev(`localStorage.clear(); 1`);

  // 5. a brain-picked set: origin tags and 「参考答案待核，不自动判」
  const made = JSON.parse(await ev(`fetch('/api/practice/new?source=math.synthetic.p0-demo&n=3&mode=wrong_only').then(r => r.text())`));
  check('[contract] /api/practice/new?mode=wrong_only creates a set from the brain pick', made.set_id && made.mode === 'wrong_only' && made.count === 3 && made.deferred === 2, JSON.stringify(made));
  check('[contract] picked set loads', await loginAndLoad(base + made.url, 3));
  const pk = await ev(`({tags: [...document.querySelectorAll('#qs .tag')].map(e => e.textContent).join(), pend: [...document.querySelectorAll('#qs .refpend')].map(e => e.closest('section').id + ':' + e.textContent).join(), title: document.getElementById('title').textContent})`);
  check('[contract] 错题 tags and 参考答案待核，不自动判 on the two deferred questions', pk.tags === '错题,错题,错题' && pk.pend === 'q2:参考答案待核，不自动判,q3:参考答案待核，不自动判', JSON.stringify(pk));
  await shot('practice-picked.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  return finish();
}

// Daily limit UX against the mock brain, which enforces the real rule (3 writes per question per Shanghai day).
async function dailyLimitFlow(base) {
  const resultShown = `getComputedStyle(document.getElementById('resultCard')).display==='block'`;
  const cards = n => `document.querySelectorAll('#qs section.card').length===${n}`;
  const lefts = () => ev(`[...document.querySelectorAll('#qs section.card')].map(c => { const q = c.querySelector('.quota'); return q ? q.textContent : null; })`);
  const hookConfirm = ans => ev(`(() => { window.__confirms = []; window.__ans = ${ans}; window.confirm = m => { window.__confirms.push(String(m)); return window.__ans; }; return 1; })()`);
  check('[limit] demo set loads', await loginAndLoad(base + '/m/practice/?set=demo-1', 3));
  await ev(`localStorage.clear(); 1`);
  await send('Page.reload', {}, sid); await waitFor(cards(3));

  // 1. fresh day: every question says 今天约还剩 3 次 (estimate), with the explanation on the submit card
  check('[limit] each question shows 「今天约还剩 3 次」', await waitFor(`[...document.querySelectorAll('#qs .quota')].length===3 && [...document.querySelectorAll('#qs .quota')].every(q => q.textContent === '今天约还剩 3 次')`), JSON.stringify(await lefts()));
  check('[limit] the labels carry the tooltip 「按本站提交记录估算」', await ev(`[...document.querySelectorAll('#qs .quota')].every(q => q.title.includes('按本站提交记录估算'))`));
  check('[limit] the page explains it is an estimate from this site\'s records', (await ev(`document.getElementById('submitCard').innerText`)).includes('按本站提交记录估算'));
  await shotEl('practice-limit-fresh.png', '#q1');

  // 2. cancelling the confirm: nothing is sent, nothing lost
  await fillDemo(); await sleep(400);
  await hookFetch(0, false); await hookConfirm('false');
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await sleep(500);
  const cancelled = await ev(`({n: window.__bodies.length, c: window.__confirms, btn: document.getElementById('btnSubmit').textContent, dis: document.getElementById('btnSubmit').disabled, shown: ${resultShown}, q1: [...document.querySelectorAll('#q1 input.blank')].map(e => e.value).join('|'), q2: document.querySelectorAll('#q2 input:checked').length, ls: JSON.parse(localStorage.getItem('ruyi-practice-demo-1-v1') || 'null')})`);
  check('[limit] the confirm asks once, in the required words (3 questions: smallest remaining = 2)', cancelled.c.length === 1 && cancelled.c[0].includes('每题每天只能提交3次') && cancelled.c[0].includes('本次提交 3 道题') && cancelled.c[0].includes('约还剩2次') && cancelled.c[0].includes('按本站提交记录估算'), JSON.stringify(cancelled.c));
  check('[limit] cancel: no request, button usable, no result, answers and draft kept, no attempt id minted', cancelled.n === 0 && !cancelled.dis && cancelled.btn === '提交' && !cancelled.shown && cancelled.q1 === '5/4||-3' && cancelled.q2 === 1 && cancelled.ls && cancelled.ls.a['example.set:math:2'] && !cancelled.ls.cid, JSON.stringify([cancelled.n, cancelled.q1, cancelled.ls && cancelled.ls.cid]));

  // 3. three successful submissions of the same questions: 2, 1, 0 left; the last confirm says 0
  const wantLeft = ['2', '1', '0'];
  for (let i = 0; i < 3; i++) {
    await hookFetch(0, false); await hookConfirm('true');
    await ev(`document.getElementById('btnSubmit').click(); 1`);
    check(`[limit] submission ${i + 1}: graded, 已提交到大脑`, await waitFor(resultShown) && (await resultInfo()).h === '已提交到大脑');
    const cf = await ev(`window.__confirms`);
    check(`[limit] submission ${i + 1}: confirm says "本次提交后…约还剩${wantLeft[i]}次"`, cf.length === 1 && cf[0].includes('约还剩' + wantLeft[i] + '次') && !cf[0].includes('，还剩'), JSON.stringify(cf));
    const want = wantLeft[i] === '0' ? '今天这题的次数已用完（再提交只会留档，不入大脑）' : '今天约还剩 ' + wantLeft[i] + ' 次';
    check(`[limit] after submission ${i + 1} the labels read: ${want}`, await waitFor(`[...document.querySelectorAll('#qs .quota')].length===3 && [...document.querySelectorAll('#qs .quota')].every(q => q.textContent === ${JSON.stringify(want)})`), JSON.stringify(await lefts()));
    await ev(`document.getElementById('btnAgain').click(); 1`); await sleep(300);
    await fillDemo(); await sleep(400);
  }
  await shotEl('practice-limit-zero.png', '#q1');

  // 4. the 4th: the confirm warns that it will not be recorded; accepting gives 429 daily_cap
  await hookFetch(0, false); await hookConfirm('true');
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check('[limit] 4th: 「已留档，未记入大脑」 message from the 429, no result card, answers kept', await waitFor(`document.getElementById('submitMsg').textContent.includes('今天这题的次数已用完，已留档，未记入大脑')`));
  const c4 = await ev(`({c: window.__confirms, shown: ${resultShown}, q1: [...document.querySelectorAll('#q1 input.blank')].map(e => e.value).join('|'), caps: document.querySelectorAll('#qs .vd.cap').length, dis: document.getElementById('btnSubmit').disabled, ids: window.__bodies.map(b => JSON.parse(b).client_attempt_id), ls: JSON.parse(localStorage.getItem('ruyi-practice-demo-1-v1') || 'null')})`);
  check('[limit] 4th: the confirm warned 「已经用完 … 不会记入大脑」', c4.c.length === 1 && c4.c[0].includes('今天的 3 次已经用完') && c4.c[0].includes('不会记入大脑') && c4.c[0].includes('约还剩0次'), JSON.stringify(c4.c));
  check('[limit] 4th: not marked submitted, answers kept, capped questions marked, button usable', !c4.shown && c4.q1 === '5/4||-3' && c4.caps === 3 && !c4.dis, JSON.stringify([c4.shown, c4.q1, c4.caps]));
  check('[limit] 4th: the page dropped its attempt id (next submit gets a new one)', c4.ls && !c4.ls.cid && !c4.ls.res, JSON.stringify(c4.ls && c4.ls.cid));
  await shot('practice-limit-4th.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  await hookFetch(0, false); await hookConfirm('true');
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`document.getElementById('btnSubmit').textContent==='提交' && window.__bodies.length===1 && document.getElementById('submitMsg').textContent.includes('已留档')`);
  await sleep(300);
  const ids2 = await ev(`window.__bodies.map(b => JSON.parse(b).client_attempt_id)`);
  check('[limit] clicking again sends a NEW client_attempt_id (the refused one is never replayed)', ids2.length === 1 && ids2[0] !== c4.ids[0], JSON.stringify([c4.ids[0].slice(0, 8), ids2[0] && ids2[0].slice(0, 8)]));
  await send('Page.reload', {}, sid); await waitFor(cards(3));
  check('[limit] after a reload the labels still say the count is used up', await waitFor(`[...document.querySelectorAll('#qs .quota.zero')].length===3`), JSON.stringify(await lefts()));

  // 5. another kind of 429 (the brain is busy) reads differently and never says 已留档
  await fillDemo(); await sleep(400);
  await hookFetchReply(429, {error: 'brain_rate_limited', message: '大脑这会儿请求太多，这次没有提交成功。答案还在，过几分钟再点一次「提交」。'});
  await hookConfirm('true');
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`document.getElementById('submitMsg').textContent.includes('大脑这会儿请求太多')`);
  const rl = await ev(`({msg: document.getElementById('submitMsg').textContent, caps: document.querySelectorAll('#qs .vd.cap').length, shown: ${resultShown}})`);
  check('[limit] rate_limited: 「大脑这会儿请求太多」, not 「已留档」, nothing marked, no result card', !rl.msg.includes('已留档') && !rl.msg.includes('次数已用完') && rl.caps === 0 && !rl.shown, rl.msg);
  await ev(`localStorage.clear(); 1`);

  // 6. unsupported (not sent to the brain): the badge and the card say 不入大脑
  check('[limit] free-form set loads', await loginAndLoad(base + '/m/practice/?set=free-3', 1));
  await ev(`(() => { const t = document.querySelector('#q1 textarea.ans'); t.value = '1/2+1/3=5/6'; t.dispatchEvent(new Event('input', {bubbles: true})); return 1; })()`);
  await sleep(400);
  await hookFetch(0, false); await hookConfirm('true');
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(resultShown);
  const un = await resultInfo();
  const cf1 = await ev(`window.__confirms`);
  check('[limit] single question: confirm reads 「每题每天只能提交3次，本次提交后约还剩2次。」', cf1.length === 1 && cf1[0].startsWith('每题每天只能提交3次，本次提交后约还剩2次。（按本站提交记录估算）'), JSON.stringify(cf1));
  check('[limit] unsupported: badge 「不自动判分（不入大脑）」, card says 不入大脑, never 已提交到大脑', un.vd.join() === '不自动判分（不入大脑）' && un.page.includes('（不入大脑）') && !un.page.includes('已提交到大脑'), un.vd.join() + ' / ' + un.h);
  await shot('practice-limit-unsupported.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  return finish();
}

// The brain's grade endpoint is not deployed: archive-only wording, never "recorded".
async function notReadyFlow(base) {
  check('[not-ready] demo set loads', await loginAndLoad(base + '/m/practice/?set=demo-1', 3));
  await ev(`localStorage.clear(); 1`);
  await send('Page.reload', {}, sid); await waitFor(`document.querySelectorAll('#qs section.card').length===3`);
  await fillDemo(); await sleep(400);
  await hookFetch(0, false);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  await waitFor(`getComputedStyle(document.getElementById('resultCard')).display==='block'`);
  const r = await resultInfo();
  check('[not-ready] heading 已提交（暂未判分）, 大脑接口还没准备好, every badge 待核对（不入大脑）, no verdict',
    r.h === '已提交（暂未判分）' && r.page.includes('大脑接口还没准备好') && r.vd.every(v => v === '待核对（不入大脑）') && !/[✓✗]/.test(r.page) && !r.page.includes('已提交到大脑') && !r.page.includes('已记入大脑'), r.h + ' / ' + r.vd.join());
  await shot('practice-result-notready.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  return finish();
}

// The local test set: no brain involved. Answer, 「检查」 (server-judged), submit -> archive-only wording.
async function smokeFlow(base) {
  const SET = 'smoke-test', KEY = 'ruyi-practice-smoke-test-v1';
  check('[smoke] smoke-test set loads after login (1 question)', await loginAndLoad(base + '/m/practice/?set=' + SET, 1));
  await ev(`localStorage.clear(); 1`);
  await send('Page.reload', {}, sid); await waitFor(`document.querySelectorAll('#qs section.card').length===1`);
  const dom = await ev(`(() => ({
    banner: getComputedStyle(document.getElementById('testBanner')).display, check: getComputedStyle(document.getElementById('btnCheck')).display,
    stem: document.querySelector('#q1 .stem').textContent, radios: document.querySelectorAll('#q1 input[type=radio]').length,
    title: document.getElementById('title').textContent, page: document.body.innerText }))()`);
  check('[smoke] test banner and 检查 button are shown; stem says 测试题，随便填; 3 options', dom.banner === 'block' && dom.check !== 'none' && dom.stem.includes('测试题，随便填') && dom.radios === 3, JSON.stringify({b: dom.banner, c: dom.check, r: dom.radios}));
  check('[smoke] no answer key in the page text', !/正确答案|答案是|解析/.test(dom.page));
  await shot('practice-smoke-1-load.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});

  const pick = i => ev(`(() => { const r = document.querySelectorAll('#q1 input[type=radio]')[${i}]; r.checked = true; r.dispatchEvent(new Event('change', {bubbles: true})); return 1; })()`);
  const vd = () => ev(`(() => { const v = document.querySelector('#q1 .vd'); return v ? v.textContent + '|' + v.className : null; })()`);
  // 检查 with nothing chosen: a hint, no request
  await ev(`window.__checks = 0; const of = window.fetch; window.fetch = (u, o) => { if (String(u).includes('/check')) window.__checks++; return of(u, o); }; document.getElementById('btnCheck').click(); 1`);
  await sleep(300);
  check('[smoke] 检查 with no answer: hint, no request', (await ev(`document.getElementById('submitMsg').textContent`)).includes('还没有作答') && (await ev(`window.__checks`)) === 0);
  await pick(0); await sleep(300);                                   // A: wrong
  await ev(`document.getElementById('btnCheck').click(); 1`);
  check('[smoke] 检查 on a wrong choice shows ✗ (judged by the server)', await waitFor(`!!document.querySelector('#q1 .vd.chk.wrong')`) && (await vd()).startsWith('✗'), await vd());
  await pick(1); await sleep(300);                                   // B: right; the old verdict goes away on change
  check('[smoke] changing the answer clears the stale verdict', (await vd()) === null);
  await ev(`document.getElementById('btnCheck').click(); 1`);
  check('[smoke] 检查 on the right choice shows ✓', await waitFor(`!!document.querySelector('#q1 .vd.chk.ok')`) && (await vd()).startsWith('✓ 答对了'), await vd());
  check('[smoke] two check requests were sent, nothing submitted yet', (await ev(`window.__checks`)) === 2 && (await ev(`getComputedStyle(document.getElementById('resultCard')).display`)) === 'none');
  await shot('practice-smoke-2-checked.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});

  await hookFetch(0, false);
  await ev(`document.getElementById('btnSubmit').click(); 1`);
  check('[smoke] result card appears after submit', await waitFor(`getComputedStyle(document.getElementById('resultCard')).display==='block'`));
  await sleep(300);
  const sent = JSON.parse(await ev(`window.__bodies[0]`));
  check('[smoke] one submit request for the smoke:q1 answer B, no rev', Object.keys(sent).sort().join() === 'answered_at,answers,client_attempt_id' && JSON.stringify(sent.answers) === JSON.stringify([{qref: 'smoke:q1', answer: 'B'}]), JSON.stringify(sent));
  const res = await ev(`(() => ({ h: document.querySelector('#resultCard h2').textContent, sum: document.querySelector('#resultCard .sum').textContent,
    vd: document.querySelector('#q1 .vd') ? document.querySelector('#q1 .vd').textContent : null, chk: document.querySelectorAll('#q1 .vd.chk').length,
    locked: [...document.querySelectorAll('#qs input')].every(e => e.disabled), again: !!document.getElementById('btnAgain'),
    submitHidden: getComputedStyle(document.getElementById('submitCard')).display === 'none', page: document.body.innerText }))()`);
  check('[smoke] heading 已提交（仅留档，未记入大脑）, test wording', res.h === '已提交（仅留档，未记入大脑）' && res.sum.includes('测试套') && res.page.includes('测试'), res.h + ' / ' + res.sum);
  check('[smoke] never 已提交到大脑 / 已记入大脑 (a bare 「未记入大脑」 is the only mention)', !res.page.includes('已提交到大脑') && !res.page.replace(/未记入大脑/g, '').includes('已记入大脑'), 'page: ' + res.page.length + ' chars');
  check('[smoke] the badge is the (测试) verdict from the server; the check badge was replaced', res.vd === '✓ 答对了（测试）' && res.chk === 0, String(res.vd));
  check('[smoke] inputs locked, 再做一次 offered, submit card hidden', res.locked && res.again && res.submitHidden);
  const ls = await ev(`JSON.parse(localStorage.getItem(${JSON.stringify(KEY)}))`);
  check('[smoke] draft key ruyi-practice-smoke-test-v1 keeps the attempt id and the stored result (no family code)', ls && ls.set === SET && ls.cid === sent.client_attempt_id && ls.res && ls.res.test === true && ls.res.mode === 'archive_only' && !JSON.stringify(ls).includes(code));
  await shot('practice-smoke-3-result.png', {x: 0, y: 0, width: 390, height: 844, scale: 1});
  await send('Page.reload', {}, sid);
  check('[smoke] after reload the result is restored without a new request', await waitFor(`document.querySelectorAll('#qs section.card').length===1 && getComputedStyle(document.getElementById('resultCard')).display==='block'`) &&
    (await ev(`performance.getEntriesByType('resource').filter(r => r.name.includes('/grade')).length`)) === 0);
  await ev(`document.getElementById('btnAgain').click(); 1`);
  await sleep(300);
  check('[smoke] 再做一次 gives a blank, editable form with the 检查 button again', await ev(`localStorage.getItem(${JSON.stringify(KEY)}) === null && document.querySelectorAll('#q1 input:checked').length === 0 && !document.querySelector('#q1 input').disabled && getComputedStyle(document.getElementById('btnCheck')).display !== 'none'`));
  // the demo set of a real paper has neither banner nor 检查 (checked by the other flows' DOM too)
  return finish();
}

(async () => {
  const { targetInfos } = await send('Target.getTargets');
  const page = targetInfos.find(t => t.type === 'page');
  ({ sessionId: sid } = await send('Target.attachToTarget', { targetId: page.targetId, flatten: true }));
  await send('Page.enable', {}, sid); await send('Runtime.enable', {}, sid);
  await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true }, sid);
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `window.confirm = () => true; window.alert = () => {};` }, sid);
  const cards = `document.querySelectorAll('#qs section.card').length`;
  const status = `document.getElementById('status').textContent`;

  if (flow === 'contract') { await send('Page.navigate', { url: base + '/m/practice/' }, sid); await sleep(300); return contractFlow(base); }
  if (flow === 'dailylimit') { await send('Page.navigate', { url: base + '/m/practice/' }, sid); await sleep(300); return dailyLimitFlow(base); }
  if (flow === 'queue') { await send('Page.navigate', { url: base + '/m/practice/' }, sid); await sleep(300); return queueFlow(base); }
  if (flow === 'notready') { return notReadyFlow(base); }
  if (flow === 'smoke') { return smokeFlow(base); }
  if (flow === 'archive') {
    await nav(base + '/m/practice/?set=demo-1');
    await waitFor(`getComputedStyle(document.getElementById('gate')).display==='flex'`);
    await gateLogin();
    await submitFlow(base, false, '[archive] ');
    return finish();
  }

  // --- friendly messages for a missing / illegal set id (no login needed) ---
  await nav(base + '/m/practice/');
  check('no ?set= : friendly message', await waitFor(`${status}.includes('缺少练习套的编号')`));
  await shot('practice-no-set.png');
  await nav(base + '/m/practice/?set=' + encodeURIComponent('Bad/../Id'));
  check('illegal set id: friendly message, no cards', await waitFor(`${status}.includes('缺少练习套的编号')`) && await ev(cards) === 0);

  // --- gate ---
  await nav(base + '/m/practice/?set=demo-1');
  check('gate shown when not logged in', await waitFor(`getComputedStyle(document.getElementById('gate')).display==='flex'`));
  await ev(`document.getElementById('gateInput').value='definitely-wrong'; document.getElementById('gateForm').requestSubmit(); 1`);
  check('wrong code shows message', await waitFor(`document.getElementById('gateErr').textContent.includes('家庭码不对')`));
  await ev(`document.getElementById('gateInput').value=${JSON.stringify(code)}; document.getElementById('gateForm').requestSubmit(); 1`);
  check('login through the form renders 3 question cards', await waitFor(`${cards}===3`));
  await waitFor(`document.querySelectorAll('.katex').length>0`);
  await sleep(500);

  const dom = await ev(`(() => {
    const q = n => document.getElementById('q' + n);
    const cs = [1, 2, 3].map(q);
    const texts = cs.map(c => c.querySelector('.stem').textContent);
    return {
      title: document.getElementById('title').textContent, docTitle: document.title,
      tex: document.querySelectorAll('#qs .tex').length, katex: document.querySelectorAll('#qs .katex').length,
      display: document.querySelectorAll('#qs .katex-display').length, err: document.querySelectorAll('.katex-error').length,
      raw: document.querySelectorAll('#qs .tex.raw').length, mathml: document.querySelectorAll('#qs .katex-mathml').length,
      blanks: cs.map(c => c.querySelectorAll('input.blank').length),
      radios: cs.map(c => c.querySelectorAll('input[type=radio]').length),
      areas: cs.map(c => c.querySelectorAll('textarea.ans').length),
      script: document.querySelectorAll('#qs script').length, pwned: typeof window.__pwned,
      q3text: texts[2], q3code: [...cs[2].querySelectorAll('code')].map(x => x.textContent), q3strong: [...cs[2].querySelectorAll('strong')].map(x => x.textContent),
      optsHaveKatex: [...cs[1].querySelectorAll('.opt .t')].map(x => !!x.querySelector('.katex')),
      noOptionRawDollar: !cs[1].textContent.includes('$'), stemRawDollar: cs.some(c => c.querySelector('.stem').textContent.includes('$')),
      qno: cs.map(c => c.querySelector('.qno').textContent),
      submit: (() => { const b = document.getElementById('btnSubmit'); return {disabled: b.disabled, text: b.textContent, shown: getComputedStyle(document.getElementById('submitCard')).display !== 'none'}; })(),
      noteOnPage: document.getElementById('submitCard').textContent.includes('提交前答案只保存在本机'),
      resultShown: getComputedStyle(document.getElementById('resultCard')).display !== 'none',
      verdicts: document.querySelectorAll('.vd, .correct, .verdict, .answer-key').length,
      fontOk: document.fonts.check('16px KaTeX_Main'),
      sameOrigin: performance.getEntriesByType('resource').every(r => r.name.startsWith(location.origin + '/')),
      resources: performance.getEntriesByType('resource').map(r => new URL(r.name).pathname).filter(p => !p.startsWith('/api/')).sort()
    };
  })()`);
  console.log('  dom:', JSON.stringify({ ...dom, q3text: undefined }));
  check('title from the set config', dom.title === '演示套' && dom.docTitle.startsWith('演示套'), dom.title);
  check('formulas rendered by local KaTeX (.katex present, none left as raw)', dom.tex === 15 && dom.katex >= 15 && dom.raw === 0, `tex=${dom.tex} katex=${dom.katex} raw=${dom.raw}`);
  check('no .katex-error anywhere', dom.err === 0);
  check('$$ blocks render as display math (2)', dom.display === 2, 'display=' + dom.display);
  check('no raw $ delimiters left in stems or options', !dom.stemRawDollar && dom.noOptionRawDollar);
  check('fill-in boxes: one per underscore run (3, 0, 2)', JSON.stringify(dom.blanks) === '[3,0,2]', JSON.stringify(dom.blanks));
  check('single-choice: 4 radios on question 2 only', JSON.stringify(dom.radios) === '[0,4,0]', JSON.stringify(dom.radios));
  check('options render math too', dom.optsHaveKatex.every(Boolean) && dom.optsHaveKatex.length === 4);
  check('no free-form answer area on questions that have blanks/options', JSON.stringify(dom.areas) === '[0,0,0]');
  check('HTML is escaped, never executed (<script> text shown, no element, no side effect)', dom.script === 0 && dom.pwned === 'undefined' && dom.q3text.includes('<script>window.__pwned=1</script>'));
  check('Markdown: `code` and **bold** rendered; x<y kept literally', dom.q3code.includes('x<y') && dom.q3strong.includes('解方程组'), JSON.stringify([dom.q3code, dom.q3strong]));
  check('question labels are plain ordinals', dom.qno[0].includes('第 1 题') && dom.qno[2].includes('第 3 题'));
  check('submit button is enabled and says 提交; no result UI before submitting', !dom.submit.disabled && dom.submit.text === '提交' && dom.submit.shown && dom.noteOnPage && dom.resultShown === false, JSON.stringify(dom.submit));
  check('no correct/incorrect UI before submitting', dom.verdicts === 0);
  check('KaTeX woff2 font actually loaded from this origin', dom.fontOk);
  check('every request is same-origin (nothing from a CDN)', dom.sameOrigin, JSON.stringify(dom.resources));
  check('only the embedded files were fetched (page assets)', dom.resources.every(p => p === '/m/practice/' || p.startsWith('/vendor/katex/')), JSON.stringify(dom.resources));

  // --- mobile layout ---
  const lay = await ev(`(() => {
    const small = [...document.querySelectorAll('input.blank, .opt, #qs button, .sticky button, textarea.ans')].filter(e => e.getBoundingClientRect().height < 44).length;
    const fs = [...document.querySelectorAll('input.blank, textarea.ans, #gateInput')].map(e => parseFloat(getComputedStyle(e).fontSize));
    const wide = [...document.querySelectorAll('#qs *')].filter(e => { const r = e.getBoundingClientRect(); return r.right > innerWidth + 1 && !e.closest('.katex-display') && !e.closest('.katex'); }).length;
    return {scrollW: document.documentElement.scrollWidth, iw: innerWidth, small, minFont: Math.min(...fs), body: parseFloat(getComputedStyle(document.body).fontSize), wide,
      blankW: Math.min(...[...document.querySelectorAll('input.blank')].map(e => e.getBoundingClientRect().width))};
  })()`);
  console.log('  layout:', JSON.stringify(lay));
  check('no horizontal page overflow at 390px', lay.scrollW <= lay.iw + 1 && lay.wide === 0, JSON.stringify(lay));
  check('touch targets >= 44px (blanks, options, buttons)', lay.small === 0, 'too small: ' + lay.small);
  check('inputs use >= 16px font (no iOS zoom); body is 18px', lay.minFont >= 16 && lay.body === 18, `minFont=${lay.minFont} body=${lay.body}`);
  check('blank boxes are wide (>= 110px)', lay.blankW >= 110, 'min width ' + Math.round(lay.blankW));
  await shot('practice-top.png');
  await shotEl('practice-q1.png', '#q1');
  await shotEl('practice-q3.png', '#q3');

  // --- answer, draft saved, restored after reload ---
  await ev(`(() => {
    const set = (el, v) => { el.value = v; el.dispatchEvent(new Event('input', {bubbles: true})); };
    const b = [...document.querySelectorAll('#q1 input.blank')]; set(b[0], '5/4'); set(b[2], '-3');
    const r = document.querySelectorAll('#q2 input[type=radio]')[1]; r.checked = true; r.dispatchEvent(new Event('change', {bubbles: true}));
    set(document.querySelectorAll('#q3 input.blank')[1], 'y=1'); return 1; })()`);
  await sleep(700);
  const store = await ev(`JSON.stringify({ls: Object.assign({}, localStorage), ss: Object.assign({}, sessionStorage), cookie: document.cookie})`);
  const st = JSON.parse(store); let draft = null; try { draft = JSON.parse(st.ls['ruyi-practice-demo-1-v1']); } catch (_) {}
  console.log('  draft keys:', draft ? JSON.stringify(Object.keys(draft)) : 'none', 'ls keys:', JSON.stringify(Object.keys(st.ls)));
  check('draft saved under ruyi-practice-demo-1-v1 with set, timestamp and qref-keyed answers',
    draft && draft.v === 1 && draft.set === 'demo-1' && /^\d{4}-\d\d-\d\dT/.test(draft.t || '') &&
    JSON.stringify(draft.a['example.set:math:1']) === JSON.stringify(['5/4', '', '-3']) && draft.a['example.set:math:2'] === 'B' &&
    JSON.stringify(draft.a['example.set:math:3']) === JSON.stringify(['', 'y=1']), JSON.stringify(draft && draft.a));
  check('family code not in localStorage / sessionStorage / document.cookie', !store.includes(code));
  check('session cookie is HttpOnly (invisible to JS)', !st.cookie.includes('ruyi_sess'));
  check('progress shows answered count', await ev(`document.getElementById('countPill').textContent.includes('已答 3 / 3')`));

  await send('Page.reload', {}, sid);
  check('after reload (cookie kept) the cards come back without the gate', await waitFor(`${cards}===3`));
  await waitFor(`document.querySelectorAll('.katex').length>=15`);
  const rest = await ev(`(() => ({
    q1: [...document.querySelectorAll('#q1 input.blank')].map(e => e.value),
    q2: [...document.querySelectorAll('#q2 input[type=radio]')].map(e => e.checked),
    q2sel: document.querySelectorAll('#q2 .opt.sel').length,
    q3: [...document.querySelectorAll('#q3 input.blank')].map(e => e.value)}))()`);
  check('draft restored after reload (blanks and the chosen option)', JSON.stringify(rest.q1) === '["5/4","","-3"]' && JSON.stringify(rest.q2) === '[false,true,false,false]' && rest.q2sel === 1 && JSON.stringify(rest.q3) === '["","y=1"]', JSON.stringify(rest));
  await shotEl('practice-q1-filled.png', '#q1');

  // --- clear ---
  await ev(`document.getElementById('btnClear').click(); 1`);
  await sleep(300);
  const cleared = await ev(`({ls: localStorage.getItem('ruyi-practice-demo-1-v1'), vals: [...document.querySelectorAll('#qs input.blank')].map(e => e.value).join(''), checked: document.querySelectorAll('#qs input:checked').length})`);
  check('「清空」 removes the draft and the inputs', cleared.ls === null && cleared.vals === '' && cleared.checked === 0, JSON.stringify(cleared));

  // --- submit flow (the brain grades) ---
  await submitFlow(base, true, '[graded] ');

  // --- a set whose paper is partly missing, and a free-form answer area ---
  await nav(base + '/m/practice/?set=reorder-2');
  check('config order wins (3 then 1) and the missing qref only yields a notice', await waitFor(`${cards}===2`) &&
    await ev(`[...document.querySelectorAll('#qs section.card')].map(c => c.getAttribute('data-qref')).join()`) === 'example.set:math:3,example.set:math:1' &&
    await ev(`${status}.includes('有 1 道题暂时没取到')`));
  await nav(base + '/m/practice/?set=free-3');
  check('free-form question gets a wide answer area (textarea)', await waitFor(`${cards}===1 && document.querySelectorAll('#q1 textarea.ans').length===1`));
  await waitFor(`document.querySelectorAll('.katex').length>=2`);
  await ev(`(() => { const t = document.querySelector('#q1 textarea.ans'); t.value = '1/2+1/3=5/6\\n过程：通分'; t.dispatchEvent(new Event('input', {bubbles: true})); return 1; })()`);
  await sleep(600);
  const fd = await ev(`JSON.parse(localStorage.getItem('ruyi-practice-free-3-v1') || 'null')`);
  check('textarea answer saved as a string under ruyi-practice-free-3-v1', fd && fd.a['example.set:math:4'] === '1/2+1/3=5/6\n过程：通分', JSON.stringify(fd && fd.a));
  const ta = await ev(`(() => { const r = document.querySelector('#q1 textarea.ans').getBoundingClientRect(); return {h: r.height, w: r.width, iw: innerWidth}; })()`);
  check('answer area is tall and full-width on a phone', ta.h >= 140 && ta.w >= ta.iw - 60, JSON.stringify(ta));
  await shotEl('practice-free.png', '#q1');
  await nav(base + '/m/practice/?set=no-such-set');
  check('unknown set: friendly message, no cards', await waitFor(`${status}.includes('没有找到这套练习')`) && await ev(cards) === 0);

  // --- logout returns to the gate ---
  await nav(base + '/m/practice/?set=demo-1');
  await waitFor(`${cards}===3`);
  await ev(`document.getElementById('btnLogout').click(); 1`);
  check('logout shows the gate and removes the questions', await waitFor(`getComputedStyle(document.getElementById('gate')).display==='flex' && ${cards}===0`));

  return finish();
})().catch(e => { console.error('FATAL', e.message); try { chrome.kill('SIGKILL'); } catch (_) {} process.exit(1); });
