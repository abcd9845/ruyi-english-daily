#!/usr/bin/env node
// Headless-Chrome check of the generic practice page (/m/practice/?set=<id>),
// driven over --remote-debugging-pipe (Node built-ins only). Usage:
//   BROWSER_CODE=<family code> node test/browser-practice.js <base-url> <out-dir> [chrome-binary]
// Against the mock brain's synthetic questions. Logs in through the real form, fills
// answers, reloads, clears; writes mobile-viewport screenshots to <out-dir>.
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const base = process.argv[2], outDir = process.argv[3], chromeBin = process.argv[4] || 'google-chrome';
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

async function shotEl(name, sel) {
  const r = await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return null; const b = e.getBoundingClientRect(); return {x: Math.max(0, b.left + scrollX - 6), y: Math.max(0, b.top + scrollY - 6), width: Math.min(innerWidth, b.width + 12), height: b.height + 12, scale: 1}; })()`);
  if (r) await shot(name, r);
}
const nav = async url => { await send('Page.navigate', { url }, sid); await sleep(300); };

(async () => {
  const { targetInfos } = await send('Target.getTargets');
  const page = targetInfos.find(t => t.type === 'page');
  ({ sessionId: sid } = await send('Target.attachToTarget', { targetId: page.targetId, flatten: true }));
  await send('Page.enable', {}, sid); await send('Runtime.enable', {}, sid);
  await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true }, sid);
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `window.confirm = () => true; window.alert = () => {};` }, sid);
  const cards = `document.querySelectorAll('#qs section.card').length`;
  const status = `document.getElementById('status').textContent`;

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
      noteOnPage: document.getElementById('submitCard').textContent.includes('本期仅作答和保存草稿'),
      verdicts: document.querySelectorAll('.correct, .wrong, .verdict, .answer-key').length,
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
  check('submit button is disabled and says 即将开放; page explains draft-only', dom.submit.disabled && dom.submit.text.includes('即将开放') && dom.submit.shown && dom.noteOnPage, JSON.stringify(dom.submit));
  check('no correct/incorrect UI exists', dom.verdicts === 0);
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

  console.log(`\n${results.length - failed}/${results.length} browser checks passed`);
  chrome.kill('SIGKILL'); await sleep(400); try { fs.rmSync(prof, { recursive: true, force: true }); } catch (_) {}
  process.exit(failed ? 1 : 0);
})().catch(e => { console.error('FATAL', e.message); try { chrome.kill('SIGKILL'); } catch (_) {} process.exit(1); });
