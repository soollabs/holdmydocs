// Run with: node --test test/ui/page.test.cjs
// Exercise the shared script without a browser or third-party dependencies.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const { hmdT } = require('./strings.cjs');
const source = readFileSync(path.join(__dirname, '../../internal/web/web/static/page.js'), 'utf8');
const components = require('../../internal/web/web/static/components.js');

function fixture(clipboard) {
  const copied = [], selected = [], timers = [], rendered = [];
  function page(text, className = '') {
    const buttons = [], labels = [], classes = [];
    const code = { textContent: text, className };
    code.parentElement = {
      classList: { add(name) { classes.push(name); } },
      querySelector() { return buttons[0] || null; },
      appendChild(button) { buttons.push(button); },
      insertBefore(label, before) { assert.equal(before, code); labels.push(label); }
    };
    const diagram = { processed: false };
    return {
      code, buttons, labels, classes, diagram,
      querySelectorAll(selector) {
        return selector === 'pre > code' ? [code] : diagram.processed ? [] : [diagram];
      }
    };
  }
  const initial = page('printf "<hello> & goodbye"\n');
  const context = {
    console,
    HMDComponents: components,
    navigator: { clipboard: clipboard === 'denied' ? { writeText: async () => { throw Error('denied'); } } :
      clipboard === 'absent' ? undefined : { writeText: async text => { copied.push(text); } } },
    document: {
      getElementById() { return initial; },
      querySelector() { return null; },
      createElement() {
        return {
          attributes: {}, handlers: {}, classList: { add() {} },
          setAttribute(name, value) { this.attributes[name] = value; },
          addEventListener(name, callback) { this.handlers[name] = callback; }
        };
      },
      createRange() { return { selectNodeContents(node) { selected.push(node); } }; }
    },
    setTimeout(callback) { timers.push(callback); },
    mermaid: { run: async ({nodes}) => { nodes.forEach(node => { node.processed = true; rendered.push(node); }); } },
    getSelection() { return { removeAllRanges() {}, addRange() {} }; }
  };
  context.window = context;
  context.HMDRunMermaid = nodes => context.mermaid.run({ nodes });
  context.hmdT = hmdT;
  vm.runInNewContext(source, context);
  return { context, initial, page, copied, selected, timers, rendered };
}

test('copies original code, announces success, and resets the button', async () => {
  const f = fixture();
  const button = f.initial.buttons[0];
  await button.handlers.click();
  assert.deepEqual(f.copied, [f.initial.code.textContent]);
  assert.equal(button.textContent, 'Copied');
  assert.equal(button.attributes['aria-label'], 'Copied');
  f.timers[0]();
  assert.equal(button.textContent, 'Copy');
  assert.equal(button.attributes['aria-label'], 'Copy code');
});

for (const mode of ['denied', 'absent']) {
  test(`selects code for manual copying when clipboard is ${mode}`, async () => {
    const f = fixture(mode);
    await f.initial.buttons[0].handlers.click();
    assert.deepEqual(f.selected, [f.initial.code]);
    assert.equal(f.initial.buttons[0].textContent, 'Press Ctrl/Cmd+C');
  });
}

test('enhancements are idempotent and work on replacement page content', () => {
  const f = fixture();
  f.context.HMDEnhancePage(f.initial);
  assert.equal(f.initial.buttons.length, 1);
  assert.equal(f.rendered.length, 1);
  const updated = f.page('updated source');
  f.context.HMDEnhancePage(updated);
  assert.equal(updated.buttons.length, 1);
  assert.equal(f.rendered.length, 2);
  f.context.HMDEnhancePage(null);
});

test('names a fenced language in the code block header', () => {
  const f = fixture();
  assert.deepEqual(f.initial.labels, []);
  assert.ok(!f.initial.classes.includes('code-has-lang'));
  const fenced = f.page('ls\n', 'language-sh');
  f.context.HMDEnhancePage(fenced);
  assert.equal(fenced.labels.length, 1);
  assert.equal(fenced.labels[0].className, 'code-lang');
  assert.equal(fenced.labels[0].textContent, 'sh');
  assert.ok(fenced.classes.includes('code-has-lang'));
  assert.equal(fenced.buttons.length, 1);
});
