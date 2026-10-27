// Drives the Logs card of a run page in jsdom against a running dashboard.
// Usage: node runpage.js <page url> <transcript path>
const fs = require('fs');
const { JSDOM, VirtualConsole } = require('jsdom');

const [pageURL, transcript] = process.argv.slice(2);
const failures = [];
const check = (name, ok, detail) => {
  console.log((ok ? 'ok ' : 'FAIL ') + name + (ok || detail === undefined ? '' : ': ' + detail));
  if (!ok) { failures.push(name); }
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(fn, ms = 4000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (fn()) { return true; }
    await sleep(25);
  }
  return fn();
}

async function main() {
  const html = (await (await fetch(pageURL)).text()).replace(/data-poll="\d+"/g, 'data-poll="150"');
  const vc = new VirtualConsole();
  (vc.forwardTo || vc.sendTo).call(vc, console);
  const dom = new JSDOM(html, {
    virtualConsole: vc, url: pageURL, runScripts: 'dangerously', pretendToBeVisual: true,
    beforeParse(w) { w.fetch = (u, o) => fetch(new URL(u, pageURL), o); },
  });
  const w = dom.window, d = w.document;
  const pane = d.getElementById('pane-agent');
  const logs = () => Array.from(pane.querySelectorAll('li.ev'));
  const find = (title) => logs().find((li) => li.querySelector('b').textContent === title);

  const md = logs().find((li) => li.querySelector('.md h1, .md [role=heading]'));
  check('markdown event is rendered', !!md);
  if (md) {
    check('heading', md.querySelector('[role=heading]').textContent === 'Title');
    check('unordered list', md.querySelectorAll('ul > li').length === 2);
    check('ordered list', md.querySelectorAll('ol > li').length === 2);
    check('bold', md.querySelector('strong').textContent === 'bold');
    check('inline code', md.querySelector('p code').textContent === 'code');
    check('quote', !!md.querySelector('blockquote'));
    check('rule', !!md.querySelector('hr'));
    check('table', md.querySelectorAll('table th').length === 2 && md.querySelectorAll('table td').length === 2);
    const fences = md.querySelectorAll('.mdcode');
    check('fenced block with language', fences.length === 2 && fences[0].querySelector('.lang').textContent === 'go' && fences[0].querySelector('code').textContent === 'fmt.Println(1)');
    check('unclosed fence is kept as code', fences.length === 2 && fences[1].querySelector('code').textContent === 'still open');
    const links = Array.from(md.querySelectorAll('a'));
    const ok = links.find((a) => a.textContent === 'ok');
    check('https link', !!ok && ok.getAttribute('href') === 'https://example.com/x' && ok.rel === 'noopener noreferrer' && ok.target === '_blank');
    check('javascript link is inert', !links.some((a) => /javascript/i.test(a.getAttribute('href'))) && md.textContent.includes('[bad](javascript:alert(1))'));
    check('script tag is text', md.querySelectorAll('script').length === 0 && md.textContent.includes('<script>window.pwned=1</script>') && w.pwned === undefined);
    check('no raw html nodes', md.querySelectorAll('img, iframe, style').length === 0);
  }
  check('tool output stays plain', !!find('Result: Read') && !!find('Result: Read').querySelector('pre.t'));
  check('no script in the log', pane.querySelectorAll('script').length === 0);

  const big = logs().find((li) => li.querySelector('.xp'));
  check('truncated event has an expand control', !!big && /\d+ more characters not shown/.test(big.querySelector('.cut .n').textContent));
  const before = big.querySelector('.t').textContent.length;
  big.querySelector('.xp').click();
  await until(() => big.querySelector('.t').textContent.length > before);
  check('expand loads the full text', big.querySelector('.t').textContent.length === 6000, big.querySelector('.t').textContent.length);
  check('expanded control reads Show less', big.querySelector('.xp').textContent === 'Show less');
  const open = pane.querySelector('details');
  open.open = true;

  const added = fs.readFileSync(transcript, 'utf8');
  fs.appendFileSync(transcript, '{"type":"assistant","timestamp":"2026-01-01T10:00:09.000Z","message":{"role":"assistant","content":[{"type":"text","text":"a **later** note"}]}}\n');
  check('transcript grew', fs.readFileSync(transcript, 'utf8').length > added.length);
  const gotNew = await until(() => !!logs().find((li) => li.textContent.includes('later')), 3000);
  check('poll appends the new event', gotNew);
  check('new event is rendered as markdown', !!find('Assistant') && Array.from(pane.querySelectorAll('strong')).some((s) => s.textContent === 'later'));
  check('poll keeps the expanded event', big.isConnected && big.querySelector('.t').textContent.length === 6000 && big.querySelector('.xp').textContent === 'Show less');
  check('poll keeps the open details', open.isConnected && open.open);
  check('poll keeps the rendered markdown nodes', !!md && md.isConnected);
  big.querySelector('.xp').click();
  check('collapse restores the short text', big.querySelector('.t').textContent.length < 6000 && big.querySelector('.xp').textContent === 'Show full');

  const engineTab = d.querySelector('[data-tab=engine]');
  check('engine log is not loaded before it is opened', !d.getElementById('logbox').querySelector('pre'));
  check('engine toolbar is one row', !!d.querySelector('.logbar label.f.h #logq') && d.getElementById('logload').textContent === 'Reload');
  engineTab.click();
  const loaded = await until(() => !!d.querySelector('#logbox pre.log'));
  check('engine tab loads the log on click', loaded && d.querySelector('#logbox pre.log').textContent.includes('issue dispatched'));
  check('engine pane is shown', !d.getElementById('pane-engine').hidden && pane.hidden);
  check('engine count is shown', /\d+ of \d+ lines/.test(d.getElementById('logcount').textContent));
  const q = d.getElementById('logq');
  q.value = 'turn started';
  q.dispatchEvent(new w.Event('input'));
  check('find filters the lines', d.querySelector('#logbox pre.log').textContent.includes('turn started') && !d.querySelector('#logbox pre.log').textContent.includes('issue dispatched'));
  w.close();
}

main().then(() => { process.exit(failures.length ? 1 : 0); }, (e) => { console.log('FAIL script error: ' + (e && e.stack || e)); process.exit(1); });
