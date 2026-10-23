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
      // A fenced language gets a header naming it; the copy button sits there too.
      var lang = (code.className.match(/(?:^|\s)language-(\S+)/) || [])[1];
      if (lang) {
        var label = document.createElement('span');
        label.className = 'code-lang';
        label.textContent = lang;
        block.insertBefore(label, code);
        block.classList.add('code-has-lang');
      }
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
      window.HMDRunMermaid(diagrams).catch(function(error) {
        console.warn('Could not render a diagram', error);
      });
    }
  };

  window.HMDEnhancePage(document.getElementById('page-content'));
})();
