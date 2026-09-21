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
      disabled: false, type: 'text', textContent: '', handlers: {}, children: [], selectors: new Map(), attributes: {} });
    this.classList = { add() {}, remove() {}, toggle() {}, contains() { return false; } };
  }
  addEventListener(name, callback) { (this.handlers[name] ||= []).push(callback); }
  removeEventListener() {}
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  querySelectorAll(selector) { return this.selectors.get(selector) || []; }
  appendChild(child) { this.children.push(child); }
  insertBefore(child) { this.children.unshift(child); this.selectors.set('[data-form-error]', [child]); }
  setAttribute(name, value) { this[name] = value; this.attributes[name] = String(value); }
  getAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attributes, name) ? this.attributes[name] : null; }
  hasAttribute(name) { return Object.prototype.hasOwnProperty.call(this.attributes, name); }
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
  const configs = {
    page: {
      selector: '#edit-form', action: '/_/api/pages/notes%2Fpage',
      values: { title: 'Draft title', tags: 'one, two', basehash: 'old-hash', hidden: '' }
    },
    namespace: {
      selector: '.namespace-form', action: '/_/api/namespaces',
      values: { name: 'notes', title: 'Draft namespace', description: 'Unsaved description',
        basehash: 'old-hash', tree: 'home, folder', widgets: 'toc, backlinks', public: '' }
    },
    setup: {
      selector: '.setup-modal form', action: '/_/api/setup',
      values: { setup_wiki: 'on', default_namespace: '_new', new_namespace: 'notes',
        site_name: 'Wiki', namespace: '', add_namespace: '', add_help: '' }
    },
    appearance: {
      selector: '#appearance-form', action: '/_/api/settings/appearance',
      values: { palette: 'dark', font_ui: 'system', font_mono: 'mono', skin: 'default', palette_explicit: '1' }
    },
    token: {
      selector: 'form[action="/_/api/settings/tokens"]', action: '/_/api/settings/tokens',
      values: { label: 'laptop', expiry: '30d', scopes: 'read', namespaces: 'notes' }
    },
    revert: {
      selector: 'form.js-revert', action: '/_/api/pages/revert/notes%2Fpage',
      values: { hash: 'abc123' }
    }
  };
  const form = add(configs[kind].selector);
  form.setAttribute('action', configs[kind].action);
  const fields = {};
  for (const [name, value] of Object.entries(configs[kind].values)) {
    const input = new Element(value);
    fields[name] = input;
    form.selectors.set(`[name="${name}"]`, [input]);
    form.selectors.set(`input[name="${name}"]`, [input]);
    document.selectors.set('#' + name, [input]);
  }
  fields.hidden && (fields.hidden.type = 'checkbox');
  if (fields.public) { fields.public.type = 'checkbox'; fields.public.checked = true; }
  if (fields.setup_wiki) fields.setup_wiki.type = 'hidden';
  if (fields.add_namespace) { fields.add_namespace.type = 'checkbox'; fields.add_namespace.checked = true; }
  if (fields.add_help) { fields.add_help.type = 'checkbox'; fields.add_help.checked = true; }
  if (fields.scopes) { fields.scopes.type = 'checkbox'; fields.scopes.checked = true; }
  if (fields.namespaces) { fields.namespaces.type = 'checkbox'; fields.namespaces.checked = true; }
  if (kind === 'revert') form.dataset.slug = 'notes/page';

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

// Browsers resolve a submit button's formAction to the current document URL when
// the button carries no formaction attribute, so a plain submit must fall back
// to the form's own action rather than posting back to the page it was on.
test('a submit button without formaction posts to the form action, not the page URL', async () => {
  const f = fixture('namespace', [{ status: 200, body: { ok: true } }]);
  const submitter = new Element();
  submitter.formAction = 'http://localhost:8080/_/namespaces/notes/edit';
  f.form.emit('submit', { submitter });
  await settle();
  assert.equal(f.calls[0].url, '/_/api/namespaces');
});

test('a submit button with formaction posts to that endpoint', async () => {
  const f = fixture('namespace', [{ status: 200, body: { ok: true } }]);
  const submitter = new Element();
  submitter.setAttribute('formaction', '/_/api/namespaces/reset');
  submitter.formAction = 'http://localhost:8080/_/api/namespaces/reset';
  f.form.emit('submit', { submitter });
  await settle();
  assert.equal(f.calls[0].url, 'http://localhost:8080/_/api/namespaces/reset');
  assert.deepEqual(f.calls[0].payload, { name: 'notes' });
});

// Controls named "action" are exposed over HTMLFormElement#action, so a form
// with submit buttons called "action" returns a RadioNodeList there. The
// handler must read the action attribute to keep a real endpoint URL.
test('a control named action does not shadow the form action URL', async () => {
  const f = fixture('namespace', [{ status: 200, body: { ok: true } }]);
  f.form.action = { toString: () => '[object RadioNodeList]' };
  f.form.emit('submit', { submitter: new Element() });
  await settle();
  assert.equal(f.calls[0].url, '/_/api/namespaces');
});

// The setup wizard is the only production form whose submit buttons are named
// "action", which is exactly what makes form.action a RadioNodeList and what
// first exposed the URL-resolution bug.
test('setup modal posts the clicked action button to the setup endpoint', async () => {
  const f = fixture('setup', [{ status: 200, body: { ok: true } }]);
  const skip = new Element('skip');
  skip.type = 'submit';
  f.form.emit('submit', { submitter: skip });
  await settle();
  assert.equal(f.calls[0].url, '/_/api/setup');
  assert.deepEqual(f.calls[0].payload, {
    action: 'skip', setup_wiki: true, default_namespace: '_new', new_namespace: 'notes',
    site_name: 'Wiki', add_namespace: true, namespace: '', add_help: true
  });
});

test('setup modal defaults to the add action without a clicked button', async () => {
  const f = fixture('setup', [{ status: 200, body: { ok: true } }]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.calls[0].url, '/_/api/setup');
  assert.equal(f.calls[0].payload.action, 'add');
});

test('setup modal keeps the form endpoint despite action-named controls', async () => {
  const f = fixture('setup', [{ status: 200, body: { ok: true } }]);
  f.form.action = { toString: () => '[object RadioNodeList]' };
  const add = new Element('add');
  add.type = 'submit';
  f.form.emit('submit', { submitter: add });
  await settle();
  assert.equal(f.calls[0].url, '/_/api/setup');
  assert.equal(f.calls[0].payload.action, 'add');
});

test('appearance sends palette_explicit as a boolean', async () => {
  const f = fixture('appearance', [{ status: 200, body: { ok: true } }]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.calls[0].url, '/_/api/settings/appearance');
  assert.deepEqual(f.calls[0].payload, {
    palette: 'dark', font_ui: 'system', font_mono: 'mono', skin: 'default', palette_explicit: true
  });
});

test('token create sends scopes and namespaces as string arrays', async () => {
  const f = fixture('token', [{ status: 200, body: { token: 'abc' } }]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.calls[0].url, '/_/api/settings/tokens');
  assert.deepEqual(f.calls[0].payload, { label: 'laptop', expiry: '30d', scopes: ['read'], namespaces: ['notes'] });
});

test('revert posts the revision hash to the revert endpoint', async () => {
  const f = fixture('revert', [{ status: 200, body: { slug: 'notes/page' } }]);
  f.form.emit('submit');
  await settle();
  assert.equal(f.calls[0].url, '/_/api/pages/revert/notes%2Fpage');
  assert.deepEqual(f.calls[0].payload, { hash: 'abc123' });
});
