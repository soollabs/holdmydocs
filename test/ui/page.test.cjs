// Run with: node --test test/ui/page.test.cjs
// Exercise the shared script without a browser or third-party dependencies.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = readFileSync(path.join(__dirname, '../../internal/web/web/static/page.js'), 'utf8');

function fixture(clipboard) {
  const copied = [], selected = [], timers = [], rendered = [];
  function page(text) {
    const buttons = [];
    const code = { textContent: text };
    code.parentElement = {
      classList: { add() {} },
      querySelector() { return buttons[0] || null; },
      appendChild(button) { buttons.push(button); }
    };
    const diagram = { processed: false };
    return {
      code, buttons, diagram,
      querySelectorAll(selector) {
        return selector === 'pre > code' ? [code] : diagram.processed ? [] : [diagram];
      }
    };
  }
  const initial = page('printf "<hello> & goodbye"\n');
  const context = {
    console,
    navigator: { clipboard: clipboard === 'denied' ? { writeText: async () => { throw Error('denied'); } } :
      clipboard === 'absent' ? undefined : { writeText: async text => { copied.push(text); } } },
    document: {
      getElementById() { return initial; },
      createElement() {
        return {
          attributes: {}, handlers: {},
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
