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
      var button = document.createElement('button');
      button.type = 'button';
      button.className = 'code-copy';
      button.textContent = 'Copy';
      button.setAttribute('aria-label', 'Copy code');
      button.setAttribute('aria-live', 'polite');
      button.addEventListener('click', async function() {
        var message;
        try {
          await navigator.clipboard.writeText(code.textContent);
          message = 'Copied';
        } catch (_) {
          // Clipboard can be unavailable on file://, HTTP, or restricted hosts.
          var selection = window.getSelection();
          var range = document.createRange();
          range.selectNodeContents(code);
          selection.removeAllRanges();
          selection.addRange(range);
          message = 'Press Ctrl/Cmd+C';
        }
        button.textContent = message;
        button.setAttribute('aria-label', message);
        setTimeout(function() {
          button.textContent = 'Copy';
          button.setAttribute('aria-label', 'Copy code');
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
})();
