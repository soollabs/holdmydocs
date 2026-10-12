const assert = require('node:assert/strict');
const components = require('../../internal/web/web/static/components.js');

class Element {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.attrs = {}; this.listeners = {}; this.children = {}; this.isConnected = true; this.open = false; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k] ?? null; }
  querySelector(selector) { return this.children[selector] || null; }
  addEventListener(k, fn) { this.listeners[k] = fn; }
  removeEventListener(k, fn) { if (this.listeners[k] === fn) delete this.listeners[k]; }
  dispatch(k) { if (this.listeners[k]) this.listeners[k]({ type: k }); }
  focus() { this.focused = true; }
  showModal() { this.open = true; }
  close() { this.open = false; this.dispatch('close'); }
}
const doc = { activeElement: null, createElement: tag => new Element(tag) };
const button = components.createButton(doc, { variant: 'primary', size: 'compact', text: 'Save', disabled: true });
assert.equal(button.tagName, 'BUTTON');
assert.equal(button.type, 'button');
assert.equal(button.className, 'ui-button ui-button--primary ui-button--compact');
assert.equal(button.textContent, 'Save');
assert.equal(button.disabled, true);
assert.throws(() => components.createButton(doc, { size: 'icon' }), /label/);
assert.throws(() => components.createButton(doc, { variant: 'bogus' }), /variant/);
assert.throws(() => components.createButton(doc, { type: 'menu' }), /type/);
assert.equal(components.createButton(doc, { type: 'submit' }).type, 'submit');

const trigger = new Element('button');
const dialog = new Element('dialog');
dialog.ownerDocument = { activeElement: trigger };
const close = components.openDialog(dialog, trigger);
assert.equal(dialog.open, true);
close();
assert.equal(dialog.open, false);
assert.equal(trigger.focused, true);
assert.equal(dialog.listeners.close, undefined);
close();
assert.equal(trigger.focused, true);

const escapedTrigger = new Element('button');
const escapedDialog = new Element('dialog');
escapedDialog.ownerDocument = { activeElement: escapedTrigger };
components.openDialog(escapedDialog);
// Native Escape dispatches cancel, then the browser closes and dispatches close.
escapedDialog.dispatch('cancel');
escapedDialog.open = false;
escapedDialog.dispatch('close');
assert.equal(escapedTrigger.focused, true);
assert.equal(escapedDialog.listeners.close, undefined);

const failedDialog = new Element('dialog');
failedDialog.ownerDocument = { activeElement: trigger };
failedDialog.showModal = () => { throw new Error('not modal'); };
assert.throws(() => components.openDialog(failedDialog), /not modal/);
assert.equal(failedDialog.listeners.close, undefined);

const details = new Element('details');
const summary = new Element('summary');
details.children.summary = summary;
const destroy = components.enhanceDisclosure(details);
assert.equal(summary.getAttribute('aria-expanded'), 'false');
details.open = true;
details.listeners.toggle();
assert.equal(summary.getAttribute('aria-expanded'), 'true');
destroy();
assert.equal(details.listeners.toggle, undefined);
assert.throws(() => components.enhanceDisclosure(new Element('div')), /details/);
console.log('components primitive contracts passed');
