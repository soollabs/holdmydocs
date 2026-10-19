// Shared reading enhancements for authenticated, public, and exported pages.
// No requests to live HMD endpoints; safe to use in a standalone export.
(function() {
  'use strict';

  window.HMDEnhancePage = function(root) {
    if (!root) return;
    root.querySelectorAll('pre > code').forEach(function(code) {
      var block = code.parentElement;
      if (block.querySelector('.code-copy')) return;
      block.classList.add('code-copy-block');
      // An overlay control with its own .code-copy treatment, not a form action.
      var button = document.createElement('button');
      button.type = 'button';
      button.className = 'code-copy';
      button.textContent = window.hmdT('js.code.copy');
      button.setAttribute('aria-label', window.hmdT('js.code.copy_aria'));
      button.setAttribute('aria-live', 'polite');
      button.addEventListener('click', async function() {
        var message;
        try {
          await navigator.clipboard.writeText(code.textContent);
          message = window.hmdT('js.code.copied');
        } catch (_) {
          // Clipboard can be unavailable on file://, HTTP, or restricted hosts.
          var selection = window.getSelection();
          var range = document.createRange();
          range.selectNodeContents(code);
          selection.removeAllRanges();
          selection.addRange(range);
          message = window.hmdT('js.code.press_copy');
        }
        button.textContent = message;
        button.setAttribute('aria-label', message);
        setTimeout(function() {
          button.textContent = window.hmdT('js.code.copy');
          button.setAttribute('aria-label', window.hmdT('js.code.copy_aria'));
        }, 2500);
      });
      block.appendChild(button);
    });

    var diagrams = root.querySelectorAll('pre.mermaid:not([data-processed])');
    if (window.mermaid && diagrams.length) {
      mermaid.run({nodes: diagrams}).catch(function(error) {
        console.warn('Could not render a diagram', error);
      });
    }
  };

  window.HMDEnhancePage(document.getElementById('page-content'));

  // phosphor prints the page title a character at a time. The untyped rest
  // stays in the layout, hidden, so the heading never reflows as it grows.
  (function typeTitle() {
    var title = document.querySelector('h1.page-title[data-title]');
    if (!title || document.documentElement.getAttribute('data-skin') !== 'phosphor') return;
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    var text = title.lastChild;
    if (!text || text.nodeType !== Node.TEXT_NODE || !text.data.trim()) return;
    var full = text.data, shown = 0;
    var rest = document.createElement('span');
    rest.className = 'title-rest';
    rest.setAttribute('aria-hidden', 'true');
    rest.textContent = full;
    text.data = '';
    title.appendChild(rest);
    title.setAttribute('aria-label', full);
    title.classList.add('title-typing');
    var delay = Math.max(15, Math.min(45, 900 / full.length));
    setTimeout(function tick() {
      shown++;
      text.data = full.slice(0, shown);
      rest.textContent = full.slice(shown);
      if (shown < full.length) {
        setTimeout(tick, delay);
        return;
      }
      rest.remove();
      title.removeAttribute('aria-label');
      title.classList.remove('title-typing');
    }, delay);
  })();
})();
