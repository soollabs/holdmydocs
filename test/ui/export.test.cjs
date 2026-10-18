// Run with: node --test test/ui/export.test.cjs
// Exercise standalone export dialog wiring with production native helpers.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const { hmdT } = require('./strings.cjs');
const source = readFileSync(path.join(__dirname, '../../internal/web/web/static/export.js'), 'utf8');
const components = require('../../internal/web/web/static/components.js');

function fixture() {
  function node(tag) {
    return {
      tagName: tag.toUpperCase(), handlers: {}, open: false, isConnected: true,
      addEventListener(name, callback) { this.handlers[name] = callback; },
      removeEventListener(name, callback) { if (this.handlers[name] === callback) delete this.handlers[name]; },
      showModal() { this.open = true; },
      close() { this.open = false; if (this.handlers.close) this.handlers.close(); },
      focus() { this.focused = true; },
      getBoundingClientRect() { return { left: 0, right: 100, top: 0, bottom: 100 }; }
    };
  }
  const dialog = node('dialog'), toggle = node('button'), input = node('input'), close = node('button');
  dialog.ownerDocument = { activeElement: toggle };
  const results = { replaceChildren() {}, appendChild() {} }, status = { textContent: '' };
  const script = { src: 'https://docs.example/index.html' };
  const nodes = { 'export-search': dialog, 'export-search-toggle': toggle, 'export-search-input': input,
    'export-search-close': close, 'export-search-results': results, 'export-search-status': status,
    'export-search-index': script };
  const document = {
    activeElement: toggle,
    querySelector(selector) { return selector === '.app-topbar-public' ? null : null; },
    getElementById(id) { return nodes[id]; },
    addEventListener(name, callback) { this.handlers[name] = callback; },
    handlers: {}, createElement: node,
  };
  const context = { document, HMDComponents: components, HMDSearchIndex: [], URL,
    ResizeObserver: undefined, console, setTimeout, window: null };
  context.window = context;
  context.hmdT = hmdT;
  vm.runInNewContext(source, context);
  return { dialog, toggle, input, close, context };
}

test('export search uses shared native dialog lifecycle and restores trigger after close', () => {
  const f = fixture();
  f.toggle.handlers.click();
  assert.equal(f.dialog.open, true);
  assert.equal(f.input.focused, true);
  f.close.handlers.click();
  assert.equal(f.dialog.open, false);
  assert.equal(f.toggle.focused, true);
  f.context.document.handlers.keydown({ ctrlKey: true, metaKey: false, key: 'k', preventDefault() {} });
  assert.equal(f.dialog.open, true);
  f.dialog.close(); // Browser Escape follows the native close event path.
  assert.equal(f.toggle.focused, true);
});
