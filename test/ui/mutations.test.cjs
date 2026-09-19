// Run with: node --test test/ui/*.test.cjs
// Execute the complete production script with a small DOM/CodeMirror fixture.
// Only browser facilities are mocked: payload building, fetch/error handling,
// event wiring, draft preservation and navigation are the real app.js code.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = readFileSync(path.join(__dirname, '../../internal/web/web/static/app.js'), 'utf8');

class Element {
  constructor(value = '') {
    Object.assign(this, { value, dataset: {}, style: {}, hidden: true, checked: false,
      disabled: false, type: 'text', textContent: '', handlers: {}, children: [], selectors: new Map() });
    this.classList = { add() {}, remove() {}, toggle() {}, contains() { return false; } };
  }
  addEventListener(name, callback) { (this.handlers[name] ||= []).push(callback); }
  removeEventListener() {}
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  querySelectorAll(selector) { return this.selectors.get(selector) || []; }
  appendChild(child) { this.children.push(child); }
  insertBefore(child) { this.children.unshift(child); this.selectors.set('[data-form-error]', [child]); }
  setAttribute(name, value) { this[name] = value; }
  emit(name, extra = {}) {
    const event = { defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...extra };
    for (const handler of this.handlers[name] || []) handler(event);
    return event;
  }
}

function fixture(kind, responses) {
  const document = new Element();
  document.body = new Element();
  document.body.dataset.slug = 'notes/page';
  document.createElement = () => new Element();
  const elements = {};
  const add = (selector, value = '') => {
    const el = new Element(value);
    document.selectors.set(selector, [el]);
    elements[selector] = el;
    return el;
  };
  const form = add(kind === 'page' ? '#edit-form' : '.namespace-form');
  form.action = kind === 'page' ? '/_/api/pages/notes%2Fpage' : '/_/api/namespaces/save';
  const values = kind === 'page'
    ? { title: 'Draft title', tags: 'one, two', basehash: 'old-hash', hidden: '' }
    : { name: 'notes', title: 'Draft namespace', description: 'Unsaved description',
        basehash: 'old-hash', tree: 'home, folder', widgets: 'toc, backlinks', public: '' };
  const fields = {};
  for (const [name, value] of Object.entries(values)) {
    const input = new Element(value);
    fields[name] = input;
    form.selectors.set(`[name="${name}"]`, [input]);
    form.selectors.set(`input[name="${name}"]`, [input]);
    document.selectors.set('#' + name, [input]);
  }
  fields.hidden && (fields.hidden.type = 'checkbox');
  if (fields.public) { fields.public.type = 'checkbox'; fields.public.checked = true; }

  let editorOptions;
  class EditorView {
    static cspNonce = { of: value => value };
    static updateListener = { of: callback => callback };
    static lineWrapping = {};
    constructor(options) { editorOptions = options; }
  }
  if (kind === 'page') {
    add('#cm-host').dataset.slug = 'notes/page';
    add('#editor-src', 'Unsaved Markdown');
    add('#new-slug', 'notes/page');
    add('#attachment-input');
    add('#save-error');
    add('#conflict-banner');
    add('#conflict-text');
    add('#conflict-overwrite');
  }
  const stored = new Map();
  const calls = [];
  const location = { pathname: '/notes/page', href: '', reload() { this.reloaded = true; } };
  const browser = new Element();
  const context = {
    console, document, location, navigator: { onLine: true },
    HMD: { EditorView, basicSetup: {}, markdown: () => ({}) },
    localStorage: {
      getItem: key => stored.get(key) ?? null,
      setItem: (key, value) => stored.set(key, String(value)),
      removeItem: key => stored.delete(key)
    },
    // Timers are intentionally not advanced: preview/debounce timers do not
    // participate in a save, and no fixed sleep is needed to await fetch.
    setTimeout: () => 1, clearTimeout() {}, setInterval: () => 1,
    confirm: () => true, alert() {},
    addEventListener: browser.addEventListener.bind(browser),
    fetch: async (url, options) => {
      calls.push({ url, ...options, payload: JSON.parse(options.body) });
      assert.ok(responses.length, 'unexpected fetch');
      const response = responses.shift();
      if (response instanceof Error) throw response;
      return { ok: response.status < 400, status: response.status, json: async () => response.body };
    }
  };
  context.window = context;
  vm.runInNewContext(source, context, { filename: 'app.js' });
  if (kind === 'page') {
    const listener = editorOptions.extensions.find(value => typeof value === 'function');
    listener({ docChanged: true, state: { doc: { toString: () => 'Unsaved Markdown' } } });
    stored.set('hmd-draft-notes/page', 'Unsaved Markdown');
  }
  return { elements, fields, form, calls, stored, location, browser };
}

// Drain promise work and callbacks without racing a wall-clock timeout.
async function settle() { await new Promise(resolve => setImmediate(resolve)); }

for (const warning of [false, true]) {
  test(`page save navigates after success${warning ? ' with an index warning' : ''}`, async () => {
    const f = fixture('page', [{ status: 200, body: { slug: 'notes/page', index_warning: warning ? 'pending' : '' } }]);
    assert.equal(f.form.emit('submit').defaultPrevented, true);
    await settle();
    assert.equal(f.calls[0].url, '/_/api/pages/notes%2Fpage');
    assert.equal(f.calls[0].method, 'POST');
    assert.equal(f.calls[0].headers['Content-Type'], 'application/json');
    assert.deepEqual(f.calls[0].payload, {
      title: 'Draft title', body: 'Unsaved Markdown', tags: ['one', 'two'],
      new_slug: 'notes/page', base_hash: 'old-hash', hidden: false, from_hidden: false
    });
    assert.equal(f.location.href, '/notes/page' + (warning ? '?index=pending' : ''));
    assert.equal(f.stored.has('hmd-draft-notes/page'), false);
    assert.equal(f.browser.emit('beforeunload').defaultPrevented, false);
  });
}

for (const response of [
  { status: 400, body: { error: 'invalid title' } },
  new Error('network unavailable')
]) {
  test(`failed page save preserves draft: ${response instanceof Error ? 'network' : 'validation'}`, async () => {
    const f = fixture('page', [response]);
    f.form.emit('submit');
    await settle();
    assert.equal(f.location.href, '');
    assert.equal(f.fields.title.value, 'Draft title');
    assert.equal(f.fields.basehash.value, 'old-hash');
    assert.equal(f.elements['#editor-src'].value, 'Unsaved Markdown');
    assert.equal(f.stored.get('hmd-draft-notes/page'), 'Unsaved Markdown');
    assert.equal(f.elements['#save-error'].hidden, false);
    assert.match(f.elements['#save-error'].textContent, /invalid title|network unavailable/);
    assert.equal(f.browser.emit('beforeunload').defaultPrevented, true);
  });
}

test('page conflict keeps every draft field and retries against the fresh revision', async () => {
  const f = fixture('page', [
    { status: 409, body: { conflict: { current_hash: 'fresh-hash', title: 'Theirs', body: 'Their text' } } },
    { status: 200, body: { slug: 'notes/page' } }
  ]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.elements['#conflict-banner'].hidden, false);
  assert.match(f.elements['#conflict-text'].textContent, /draft is preserved/);
  assert.equal(f.fields.basehash.value, 'fresh-hash');
  assert.equal(f.fields.title.value, 'Draft title');
  assert.equal(f.fields.tags.value, 'one, two');
  assert.equal(f.elements['#editor-src'].value, 'Unsaved Markdown');
  assert.equal(f.stored.get('hmd-draft-notes/page'), 'Unsaved Markdown');
  assert.equal(f.location.href, '');
  assert.equal(f.browser.emit('beforeunload').defaultPrevented, true);
  f.elements['#conflict-overwrite'].emit('click');
  await settle();
  assert.equal(f.calls[1].payload.base_hash, 'fresh-hash');
  assert.equal(f.calls[1].payload.body, 'Unsaved Markdown');
  assert.equal(f.location.href, '/notes/page');
});

test('namespace conflict preserves submitted settings and retries with the current hash', async () => {
  const f = fixture('namespace', [
    { status: 409, body: { conflict: { current_hash: 'fresh-hash', config: { title: 'Theirs' } } } },
    { status: 200, body: { ok: true } }
  ]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.location.href, '');
  assert.equal(f.fields.basehash.value, 'fresh-hash');
  assert.equal(f.fields.title.value, 'Draft namespace');
  assert.equal(f.fields.description.value, 'Unsaved description');
  assert.equal(f.fields.public.checked, true);
  assert.match(f.form.querySelector('[data-form-error]').textContent, /nothing has been overwritten/);
  f.form.emit('submit');
  await settle();
  assert.equal(f.calls[1].payload.base_hash, 'fresh-hash');
  assert.equal(f.calls[1].payload.title, 'Draft namespace');
  assert.deepEqual(f.calls[1].payload.tree, ['home', 'folder']);
  assert.equal(f.location.href, '/_/namespaces?saved=1');
});

test('namespace validation failure stays inline without replacing fields', async () => {
  const f = fixture('namespace', [{ status: 400, body: { error: 'invalid index' } }]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.location.href, '');
  assert.equal(f.fields.basehash.value, 'old-hash');
  assert.equal(f.fields.description.value, 'Unsaved description');
  assert.equal(f.form.querySelector('[data-form-error]').textContent, 'invalid index');
});
