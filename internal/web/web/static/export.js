// Enhancements for an entirely self-contained export. No live HMD requests.
(function() {
  'use strict';

  var dialog = document.getElementById('export-search');
  var toggle = document.getElementById('export-search-toggle');
  var input = document.getElementById('export-search-input');
  var results = document.getElementById('export-search-results');
  var status = document.getElementById('export-search-status');
  var script = document.getElementById('export-search-index');
  if (!dialog || !toggle || !Array.isArray(window.HMDSearchIndex)) return;

  var entries = window.HMDSearchIndex.map(function(entry) {
    return Object.assign({}, entry, {
      titleLower: entry.title.toLocaleLowerCase(),
      textLower: entry.text.toLocaleLowerCase()
    });
  });
  toggle.hidden = false;
  toggle.addEventListener('click', function() {
    dialog.showModal();
    input.focus();
  });
  document.getElementById('export-search-close').addEventListener('click', function() { dialog.close(); });
  dialog.addEventListener('click', function(event) {
    var rect = dialog.getBoundingClientRect();
    if (event.target === dialog && (event.clientX < rect.left || event.clientX > rect.right ||
        event.clientY < rect.top || event.clientY > rect.bottom)) dialog.close();
  });
  document.addEventListener('keydown', function(event) {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      if (!dialog.open) dialog.showModal();
      input.focus();
    }
  });

  input.addEventListener('input', function() {
    results.replaceChildren();
    var query = input.value.trim().toLocaleLowerCase();
    if (!query) {
      status.textContent = 'Search titles and page text. No data leaves your browser.';
      return;
    }
    var terms = query.split(/\s+/);
    var matches = entries.filter(function(entry) {
      return terms.every(function(term) { return entry.titleLower.includes(term) || entry.textLower.includes(term); });
    }).sort(function(a, b) {
      function score(entry) {
        return (entry.titleLower === query ? 100 : 0) +
          (entry.titleLower.includes(query) ? 20 : 0) +
          terms.filter(function(term) { return entry.titleLower.includes(term); }).length;
      }
      return score(b) - score(a) || a.title.localeCompare(b.title);
    });
    status.textContent = matches.length ? matches.length + ' matching page' + (matches.length === 1 ? '' : 's') +
      (matches.length > 30 ? ' (showing the first 30).' : '.') : 'No matching pages. Try fewer words or a different term.';
    matches.slice(0, 30).forEach(function(entry) {
      var item = document.createElement('li');
      var link = document.createElement('a');
      // Resolve against the index script, not the current nested page.
      link.href = new URL(entry.href, script.src).href;
      link.textContent = entry.title;
      var snippet = document.createElement('p');
      var matchAt = entry.textLower.indexOf(terms[0]);
      var start = Math.max(0, matchAt - 60);
      snippet.textContent = (start ? '…' : '') + entry.text.slice(start, start + 180) +
        (entry.text.length > start + 180 ? '…' : '');
      item.append(link, snippet);
      results.appendChild(item);
    });
  });
})();
