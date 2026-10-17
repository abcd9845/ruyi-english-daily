#!/usr/bin/env node
// Headless-Chrome check of the dynamic page, driven over --remote-debugging-pipe
// (Node built-ins only). Usage:
//   BROWSER_CODE=<family code> node test/browser.js <base-url> <out-dir> [chrome-binary]
// Logs in through the real login form, checks the DOM, stubs the ntfy POST (nothing
// is ever sent to ntfy.sh), and writes screenshots to <out-dir>.
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const base = process.argv[2], outDir = process.argv[3], chromeBin = process.argv[4] || 'google-chrome';
const brainDown = process.env.BRAIN_DOWN === '1'; // mode: the site's brain is unreachable
const allMissing = process.env.ALL_MISSING === '1'; // mode: no figure ingested yet (real brain today)
const code = process.env.BROWSER_CODE || '';
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

(async () => {
  const { targetInfos } = await send('Target.getTargets');
  const page = targetInfos.find(t => t.type === 'page');
  ({ sessionId: sid } = await send('Target.attachToTarget', { targetId: page.targetId, flatten: true }));
  await send('Page.enable', {}, sid); await send('Runtime.enable', {}, sid);
  await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true }, sid);
  // never talk to the real ntfy: record the POST instead
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__ntfy = []; window.confirm = () => true; window.alert = () => {};
    const _f = window.fetch.bind(window);
    window.fetch = (u, o) => { if (String(u).startsWith('https://ntfy.sh/')) { window.__ntfy.push({url: String(u), headers: o && o.headers, body: o && o.body}); return Promise.resolve(new Response('{}', {status: 200})); } return _f(u, o); };` }, sid);

  const pageUrl = base + '/m/numberline-4y/';
  await send('Page.navigate', { url: pageUrl }, sid);
  check('gate shown when not logged in', await waitFor(`getComputedStyle(document.getElementById('gate')).display==='flex'`));
  await shot('gate.png');

  // wrong code first
  await ev(`document.getElementById('gateInput').value='definitely-wrong'; document.getElementById('gateForm').requestSubmit(); 1`);
  check('wrong code shows message', await waitFor(`document.getElementById('gateErr').textContent.includes('家庭码不对')`));

  // right code via the real form
  await ev(`document.getElementById('gateInput').value=${JSON.stringify(code)}; document.getElementById('gateForm').requestSubmit(); 1`);
  if (brainDown) {
    check('brain down: readable message shown', await waitFor(`document.getElementById('status').textContent.includes('题库暂时连不上')`));
    check('brain down: no question cards, no sticky submit bar', await ev(`document.querySelectorAll('#qs section.card').length===0 && getComputedStyle(document.getElementById('actions')).display==='none'`));
    check('brain down: retry button offered', await ev(`!!document.querySelector('#status button')`));
    await shot('numberline-brain-down.png');
    console.log(`\n${results.length - failed}/${results.length} browser checks passed`);
    chrome.kill('SIGKILL'); await sleep(400); try { fs.rmSync(prof, { recursive: true, force: true }); } catch (_) {}
    process.exit(failed ? 1 : 0);
  }
  const rendered = await waitFor(`document.querySelectorAll('#qs section.card').length>0`);
  check('login through the form renders questions', rendered);
  if (allMissing) {
    await waitFor(`document.querySelectorAll('#qs .noimg').length>0`);
    const r = await ev(`({cards:document.querySelectorAll('#qs section.card').length, ph:[...document.querySelectorAll('#qs .noimg')].filter(n=>n.textContent.includes('图未入库')).length, imgs:document.images.length})`);
    check('no figures ingested: all 60 questions render with 「图未入库」 placeholders, no images', r.cards === 60 && r.ph === 60 && r.imgs === 0, JSON.stringify(r));
    await shot('numberline-all-missing.png');
    console.log(`\n${results.length - failed}/${results.length} browser checks passed`);
    chrome.kill('SIGKILL'); await sleep(400); try { fs.rmSync(prof, { recursive: true, force: true }); } catch (_) {}
    process.exit(failed ? 1 : 0);
  }
  const total = await ev(`document.querySelectorAll('#qs section.card').length`);
  check('60 question cards', total === 60, 'cards=' + total);
  await ev(`document.querySelectorAll('#qs img').forEach(i=>{i.loading='eager'}); 1`);
  await waitFor(`[...document.images].every(i=>i.complete)`, 20000);
  const st = await ev(`(() => {
    const cards=[...document.querySelectorAll('#qs section.card')];
    const withImg=cards.filter(c=>c.querySelector('.fig img'));
    const loaded=[...document.images].filter(i=>i.naturalWidth>0).length;
    const placeholders=cards.filter(c=>[...c.querySelectorAll('.noimg')].some(n=>n.textContent.includes('图未入库'))).map(c=>c.id);
    let below=0, bad=[];
    withImg.forEach(c=>{ const s=c.querySelector('.stem').getBoundingClientRect(); const i=c.querySelector('.fig img').getBoundingClientRect(); if(i.top>=s.bottom-1) below++; else bad.push(c.id); });
    const broken=[...document.images].filter(i=>i.complete && i.naturalWidth===0).length;
    const q25=document.getElementById('q25'), q29=document.getElementById('q29');
    return {cards:cards.length, withImg:withImg.length, images:document.images.length, loaded, broken, placeholders, below, bad,
      q25_img:!!q25.querySelector('img'), q25_noimg:!!q25.querySelector('.noimg'), q29_img:!!q29.querySelector('.fig img'), q29_noimg:!!q29.querySelector('.noimg'),
      labels:[1,36,38,60].map(n=>document.querySelector('#q'+n+' .qno').textContent)};
  })()`);
  console.log('  dom:', JSON.stringify(st));
  check('every image loaded (no broken images)', st.broken === 0 && st.loaded === st.images && st.images === 56, `images=${st.images} loaded=${st.loaded}`);
  check('images are below the stem', st.below === st.withImg && st.withImg === 55, `below=${st.below}/${st.withImg}`);
  check('placeholder 「图未入库」 only for missing / unknown / absent-key / ok-but-empty (ordinals 17,26,27,28)', JSON.stringify(st.placeholders) === JSON.stringify(['q17','q26','q27','q28']), JSON.stringify(st.placeholders));
  check('figure_status=none, no media: no placeholder and no image (ordinal 25)', !st.q25_img && !st.q25_noimg);
  check('figure_status=none but media present: image still shown, no placeholder (ordinal 29)', st.q29_img && !st.q29_noimg);
  check('question numbers shown verbatim from the library, in sorted order', st.labels[0].includes('第 1(1) 题') && st.labels[1].includes('11(2)①') && st.labels[2].includes('11(2)③') && st.labels[3].includes('18(3)'), JSON.stringify(st.labels));

  // answer something; draft must not hold the family code
  await ev(`(() => { const t=document.querySelector('#q31 input[type=text]'); t.value='3'; t.dispatchEvent(new Event('input',{bubbles:true}));
                     const r=document.querySelector('#q1 input[type=radio]'); r.checked=true; r.dispatchEvent(new Event('change',{bubbles:true})); return 1; })()`);
  await sleep(600);
  const store = await ev(`JSON.stringify({ls:Object.assign({},localStorage), ss:Object.assign({},sessionStorage), cookie:document.cookie})`);
  const ls = JSON.parse(store).ls; let draft = null; try { draft = JSON.parse(ls['ruyi-mi-numberline-4y-v1']); } catch (_) {}
  check('draft saved under ruyi-mi-numberline-4y-v1 (answers keyed by qref)', draft && draft.a && Object.keys(draft.a).length === 2, draft ? 'answers=' + Object.keys(draft.a).length : 'missing');
  check('family code not in localStorage/sessionStorage/document.cookie', !store.includes(code));
  check('session cookie is HttpOnly (invisible to JS)', !(await ev('document.cookie')).includes('ruyi_sess'));

  // screenshots: top, a question with image, #17 placeholder
  await ev(`window.scrollTo(0,0)`); await sleep(300); await shot('numberline-top.png');
  await ev(`document.getElementById('q17').scrollIntoView()`); await sleep(500); await shot('numberline-q17-placeholder.png');
  await ev(`document.getElementById('q5').scrollIntoView()`); await sleep(500); await shot('numberline-q5-option-image.png');

  // submit: stubbed ntfy
  const btn = await ev(`document.getElementById('btnSubmit').textContent`);
  const enabled = await ev(`!document.getElementById('btnSubmit').disabled`);
  if (enabled) {
    await ev(`document.getElementById('btnSubmit').click(); 1`);
    check('submit posts one payload', await waitFor(`window.__ntfy.length===1`));
    const p = JSON.parse(await ev(`window.__ntfy[0].body`));
    const url = await ev(`window.__ntfy[0].url`);
    check('payload shape (type/learner/set/answers/qref, no family code)',
      p.type === 'ruyi-mi-numberline-4y-submit' && p.learner === '如意' && p.set.source === 'math-intake.zz-yuekao-numberline-4y' &&
      p.answers.length === 60 && p.answers.every(a => a.qref && 'typed' in a) && !JSON.stringify(p).includes(code) && p.answers.find(a => a.ordinal === 31).typed === '3' && p.answers.find(a => a.ordinal === 38).n === '11(2)③',
      'answered=' + p.answered);
    check('ntfy url came from /api/config (https://ntfy.sh/…)', /^https:\/\/ntfy\.sh\/[A-Za-z0-9_-]+$/.test(url));
  } else {
    check('submit disabled with notice when no ntfy topic', btn.includes('提交通道未配置'), btn);
  }

  // logout returns to gate
  await ev(`document.getElementById('btnLogout').click(); 1`);
  check('logout shows gate again', await waitFor(`getComputedStyle(document.getElementById('gate')).display==='flex'`));

  console.log(`\n${results.length - failed}/${results.length} browser checks passed`);
  fs.writeFileSync(path.join(outDir, 'browser-results.json'), JSON.stringify(results, null, 1));
  chrome.kill('SIGKILL'); await sleep(400); try { fs.rmSync(prof, { recursive: true, force: true }); } catch (_) {}
  process.exit(failed ? 1 : 0);
})().catch(e => { console.error('browser test error:', e.message); chrome.kill('SIGKILL'); process.exit(1); });
