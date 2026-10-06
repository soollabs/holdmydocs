// Run with: node --test test/ui/oauth.test.cjs
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = readFileSync(path.join(__dirname, '../../internal/web/web/static/oauth.js'), 'utf8');

function consentFixture() {
  const state = { scope: true, admin: false, all: false, selected: false };
  const approve = { disabled: true };
  const validation = {}, namespaces = {}, warning = {}, checkbox = {}, events = {};
  const list = { querySelectorAll: () => [checkbox] };
  const form = {
    querySelector(selector) {
      if (selector === 'button[value="approve"]') return approve;
      if (selector === 'input[name="scope"]:checked') return state.scope || state.admin ? {} : null;
      if (selector === 'input[name="scope"][value="settings"]:checked') return state.admin ? {} : null;
      if (selector === 'input[name="namespace_mode"]:checked') return { value: state.all ? 'all' : 'selected' };
      if (selector === 'input[name="namespace"]:checked') return state.selected ? {} : null;
      throw Error(selector);
    },
    addEventListener(event, callback) { events[event] = callback; }
  };
  const nodes = {
    'oauth-consent-form': form, 'oauth-consent-validation': validation,
    'oauth-namespace-access': namespaces, 'oauth-admin-warning': warning, 'oauth-namespace-list': list
  };
  vm.runInNewContext(source, { document: { getElementById: id => nodes[id], querySelectorAll: () => [] } });
  function submit(value) {
    let prevented = false;
    events.submit({ submitter: value ? { value } : null, preventDefault() { prevented = true; } });
    return prevented;
  }
  return { state, approve, validation, namespaces, warning, checkbox, list, change: events.change, submit };
}

test('consent needs actions and explicit namespace access; denial is always available', () => {
  const f = consentFixture();
  assert.equal(f.approve.disabled, true);
  assert.equal(f.submit('approve'), true);
  assert.equal(f.submit(null), true);
  assert.equal(f.submit('deny'), false);
  f.state.selected = true;
  f.change();
  assert.equal(f.approve.disabled, false);
  assert.equal(f.submit('approve'), false);
  f.state.scope = false;
  f.change();
  assert.equal(f.approve.disabled, true);
  assert.equal(f.submit('deny'), false);
});

test('all namespaces hides inactive choices and restores them when narrowed', () => {
  const f = consentFixture();
  f.state.all = true;
  f.change();
  assert.equal(f.approve.disabled, false);
  assert.equal(f.list.hidden, true);
  assert.equal(f.checkbox.disabled, true);
  f.state.all = false;
  f.change();
  assert.equal(f.approve.disabled, true);
  assert.equal(f.list.hidden, false);
  assert.equal(f.checkbox.disabled, false);
});

test('removing administrator access never silently grants all namespaces', () => {
  const f = consentFixture();
  f.state.admin = true;
  f.change();
  assert.equal(f.approve.disabled, false);
  assert.equal(f.namespaces.hidden, true);
  assert.equal(f.warning.hidden, false);
  assert.match(f.validation.textContent, /unrestricted/);
  f.state.admin = false;
  f.change();
  assert.equal(f.state.all, false);
  assert.equal(f.namespaces.hidden, false);
  assert.equal(f.warning.hidden, true);
  assert.equal(f.approve.disabled, true);
});

for (const mode of ['available', 'denied', 'absent']) {
  test(`credential copy handles ${mode} clipboard without retaining a second secret`, async () => {
    const events = {}, status = {}, copied = [];
    const input = { value: 'synthetic-credential', focus() { this.focused = true; }, select() { this.selected = true; } };
    const button = { hidden: true, dataset: { oauthCopy: 'credential' }, addEventListener(name, callback) { events[name] = callback; } };
    vm.runInNewContext(source, {
      document: {
        getElementById: id => id === 'credential' ? input : null,
        querySelector: () => status, querySelectorAll: () => [button]
      },
      navigator: {
        clipboard: mode === 'absent' ? undefined : { writeText: async value => {
          if (mode === 'denied') throw Error('denied');
          copied.push(value);
        } }
      }
    });
    assert.equal(button.hidden, false);
    await events.click();
    if (mode === 'available') assert.deepEqual(copied, [input.value]);
    else {
      assert.equal(input.focused, true);
      assert.equal(input.selected, true);
    }
    assert.equal(status.textContent.includes(input.value), false);
  });
}
