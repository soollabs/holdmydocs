/* Browser-only OAuth enhancements. The server independently enforces consent. */
(() => {
  const ui = window.HMDComponents;
  if (ui) document.querySelectorAll('.oauth-page details').forEach(details => ui.enhanceDisclosure(details));
  const form = document.getElementById('oauth-consent-form');
  if (form) {
    const approve = form.querySelector('button[value="approve"]');
    const validation = document.getElementById('oauth-consent-validation');
    const namespaces = document.getElementById('oauth-namespace-access');
    const warning = document.getElementById('oauth-admin-warning');
    const list = document.getElementById('oauth-namespace-list');
    function validate() {
      const hasScope = !!form.querySelector('input[name="scope"]:checked');
      const admin = !!form.querySelector('input[name="scope"][value="admin"]:checked');
      const mode = form.querySelector('input[name="namespace_mode"]:checked');
      // Keep the selected namespace mode in the submitted form. Settings is
      // unrestricted on the server, but removing it must restore the user's
      // restricted choice rather than silently grant all namespaces.
      namespaces.hidden = admin;
      if (warning) warning.hidden = !admin;
      const all = mode && mode.value === 'all';
      list.hidden = all;
      list.querySelectorAll('input').forEach(input => { input.disabled = admin || all; });
      const selected = !!form.querySelector('input[name="namespace"]:checked');
      const message = !hasScope ? window.hmdT('js.oauth.choose_action')
        : !admin && !all && !selected ? window.hmdT('js.oauth.choose_namespace') : '';
      approve.disabled = !!message;
      validation.textContent = message || window.hmdT(admin ? 'js.oauth.admin_warning' : 'js.oauth.ready');
      return !message;
    }
    form.addEventListener('change', validate);
    form.addEventListener('submit', event => {
      if (event.submitter && event.submitter.value === 'deny') return;
      if (!validate()) event.preventDefault();
    });
    validate();
  }

  document.querySelectorAll('[data-oauth-copy]').forEach(button => {
    const input = document.getElementById(button.dataset.oauthCopy);
    const status = document.querySelector('[data-oauth-copy-status]');
    if (!input || !status) return;
    button.hidden = false;
    button.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(input.value);
        status.textContent = window.hmdT('js.oauth.copied');
      } catch (_) {
        input.focus();
        input.select();
        status.textContent = window.hmdT('js.oauth.selected');
      }
    });
  });
})();
