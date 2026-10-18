// The browser scripts read their text through window.hmdT, which base.html
// defines in its head script. Load the real definition and the en-GB js.*
// messages so the fixtures see the same strings as the page.
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const catalogue = JSON.parse(readFileSync(path.join(__dirname, '../../internal/i18n/locales/en-GB.json'), 'utf8'));
const strings = {};
for (const [key, entry] of Object.entries(catalogue)) {
  if (!key.startsWith('js.')) continue;
  const { description, ...forms } = entry;
  strings[key] = Object.keys(forms).length === 1 && forms.other ? forms.other : forms;
}

const base = readFileSync(path.join(__dirname, '../../internal/web/web/templates/base.html'), 'utf8');
const definition = base.match(/window\.hmdT = function[\s\S]*?\n\t\t\};/);
if (!definition) throw Error('window.hmdT not found in base.html');

const context = { window: { hmdStrings: strings }, document: { documentElement: { lang: 'en-GB' } }, Intl };
vm.runInNewContext(definition[0], context);

module.exports = { hmdT: context.window.hmdT };
