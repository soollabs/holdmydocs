// CodeMirror version: see package.json in build directory
// Mermaid: @11

(function() {
  const $ = (s, p) => (p || document).querySelector(s);
  const $$ = (s, p) => Array.from((p || document).querySelectorAll(s));

  // ---- Offline edit queue: queue form POSTs made while offline, flush on reconnect ----
  const OFFLINE_QUEUE_KEY = 'hmd-offline-queue';

  function loadQueue() {
    try { return JSON.parse(localStorage.getItem(OFFLINE_QUEUE_KEY)) || []; }
    catch (e) { return []; }
  }

  function saveQueue(queue) {
    localStorage.setItem(OFFLINE_QUEUE_KEY, JSON.stringify(queue));
  }

  function queueFormSubmit(form) {
    const queue = loadQueue();
    queue.push({
      url: form.action,
      entries: Array.from(new FormData(form).entries()),
      ts: Date.now()
    });
    saveQueue(queue);
  }

  async function flushOfflineQueue() {
    const queue = loadQueue();
    while (queue.length) {
      const item = queue[0];
      try {
        const res = await fetch(item.url, { method: 'POST', body: new URLSearchParams(item.entries) });
        if (!res.ok) break;
      } catch (e) {
        break; // still offline (or server unreachable) — retry next 'online' event
      }
      queue.shift();
      saveQueue(queue);
    }
  }

  window.addEventListener('online', flushOfflineQueue);
  if (navigator.onLine) flushOfflineQueue();

  // Shared page.js owns reading enhancements, including Mermaid rendering.
  let pageContent = $('#page-content');

  // ---- Pages sidebar: merged pinned + recent (client-side, localStorage) ----
  // Content owns the root, and every app route lives under /_/ — so any
  // path outside /_/ is a real page's own URL, and the slug is just the
  // path with its leading slash stripped.
  const recentList = $('#recent-list');
  const currentSlug = window.location.pathname.startsWith('/_/') ? '' : window.location.pathname.slice(1).replace(/\/$/, '');
  // Namespaces are one level deep (mirrors namespaceFor in namespace.go):
  // "blog/drafts/post" belongs to "blog", a bare slug belongs to root ("").
  const namespaceOf = slug => { const i = slug.indexOf('/'); return i === -1 ? '' : slug.slice(0, i); };
  const currentNs = namespaceOf(document.body.dataset.slug || '');

  if (recentList) {
    try {
      const titleEl = $('.page-title');
      if (currentSlug && titleEl) {
        const title = titleEl.dataset.title || titleEl.textContent.trim() || currentSlug;
        let recent = JSON.parse(localStorage.getItem('hmd-recent') || '[]');
        recent = recent.filter(e => e.slug !== currentSlug);
        recent.unshift({slug: currentSlug, title});
        recent = recent.slice(0, 8);
        localStorage.setItem('hmd-recent', JSON.stringify(recent));
      }
    } catch (e) { /* ignore */ }
    renderSidebarPages();
  }

  function renderSidebarPages() {
    if (!recentList) return;
    let pinned = [];
    try { pinned = JSON.parse(localStorage.getItem('hmd-pinned') || '[]'); } catch (e) {}
    let recent = [];
    try { recent = JSON.parse(localStorage.getItem('hmd-recent') || '[]'); } catch (e) {}
    const recentBySlug = new Map(recent.map(e => [e.slug, e]));
    const pinnedEntries = pinned
      .filter(slug => namespaceOf(slug) === currentNs)
      .map(slug => ({slug, title: (recentBySlug.get(slug) || {}).title || slug, pinned: true}));
    const recentEntries = recent
      .filter(e => !pinned.includes(e.slug) && namespaceOf(e.slug) === currentNs)
      .map(e => ({slug: e.slug, title: e.title, pinned: false}));
    const entries = pinnedEntries.concat(recentEntries);
    recentList.innerHTML = entries.map(e => {
      const hasDraft = localStorage.getItem('hmd-draft-' + e.slug) !== null;
      const dot = hasDraft ? '<span class="draft-dot"></span>' : '';
      const glyph = e.pinned ? '<span class="pin-glyph">★</span>' : '<span class="recent-glyph">·</span>';
      return `<li>${glyph}${dot}<a href="/${e.slug}"${e.slug === currentSlug ? ' class="active"' : ''}>${escapeHtml(e.title)}</a></li>`;
    }).join('');
  }

  // ---- Preview cards for wiki-link hover ----
  const previewCard = document.createElement('div');
  previewCard.className = 'preview-card';
  previewCard.style.display = 'none';
  document.body.appendChild(previewCard);

  if (pageContent) {
    const wikiLinks = pageContent.querySelectorAll('a');
    let previewTimer;

    wikiLinks.forEach(link => {
      link.addEventListener('mouseenter', () => {
        if (link.classList.contains('missing')) return;

        previewTimer = setTimeout(() => {
          const href = link.getAttribute('href');
          if (!link.classList.contains('wiki') || !href) return;

          const slug = href.replace(/^\//, '').replace(/\/$/, '');
          fetch('/_/api/preview/' + encodeURIComponent(slug))
            .then(r => r.json())
            .then(data => {
              const rect = link.getBoundingClientRect();
              const tags = (data.tags || []).map(t => '<span class="card-tag">' + escapeHtml(t) + '</span>').join(' ');
              previewCard.innerHTML = `
                <h3><span class="h"></span>${escapeHtml(data.title)}</h3>
                <div class="snippet">${escapeHtml(data.snippet)}</div>
                <div class="meta">${tags ? tags + ' · ' : ''}edited ${escapeHtml(data.age)}</div>
              `;
              previewCard.style.top = (rect.bottom + 10 + window.scrollY) + 'px';
              previewCard.style.left = (rect.left) + 'px';
              previewCard.style.display = 'block';
            })
            .catch(() => {});
        }, 300);
      });

      link.addEventListener('mouseleave', () => {
        clearTimeout(previewTimer);
        previewCard.style.display = 'none';
      });
    });
  }

  // TOC rail: see toc.js, loaded separately so it also runs for anonymous
  // public-namespace views.

  // ---- Word count for view mode ----
  const wordCountSeg = $('#word-count');
  if (pageContent && wordCountSeg) {
    const text = pageContent.textContent || '';
    const words = text.trim().split(/\s+/).filter(w => w.length > 0).length;
    const readingTime = Math.max(1, Math.ceil(words / 200));
    wordCountSeg.textContent = words + 'w · ' + readingTime + ' min';
  }

  // ---- Sync polling ----
  const syncSeg = $('#status-sync');
  if (syncSeg) {
    const pollMs = window.hmdSyncPollMs || 10000;
    const bidi = window.hmdSyncMode === 'bidirectional';
    let pollTimer;
    let lastRenderedHash = pageContent ? (pageContent.dataset.blobHash || '') : null;
    let syncAgeTimer;
    const syncText = $('#sync-text', syncSeg) || syncSeg;

    function updateSyncAge() {
      const state = syncSeg.dataset.state || 'unknown';
      const last = parseInt(syncSeg.dataset.lastSuccess || '0');
      if (state === 'no remote') {
        syncText.textContent = 'local only';
        return;
      }
      if (state === 'ok' && last > 0) {
        syncText.textContent = 'synced ' + relativeAge(last);
        return;
      }
      const arrow = bidi ? '⇣⇡' : '⇡';
      syncText.textContent = arrow + ' ' + state + (last > 0 ? ' · ' + relativeAge(last) : '');
    }

    function updateSyncSeg(syncData) {
      syncSeg.setAttribute('data-state', syncData.state);
      syncSeg.setAttribute('data-last-success', syncData.last_success_unix || 0);
      updateSyncAge();
    }

    function refreshViewedPage() {
      if (!pageContent) return;
      fetch(window.location.pathname, { headers: { 'X-Requested-With': 'fetch' } })
        .then(r => r.text())
        .then(html => {
          const doc = new DOMParser().parseFromString(html, 'text/html');
          const newContent = doc.querySelector('#page-content');
          if (!newContent) return;
          const newHash = newContent.dataset.blobHash || '';
          if (newHash && newHash === lastRenderedHash) return;
          lastRenderedHash = newHash;
          const parent = pageContent.parentNode;
          if (!parent) return;
          parent.replaceChild(newContent, pageContent);
          pageContent = newContent;
          if (window.HMDEnhancePage) window.HMDEnhancePage(newContent);
        })
        .catch(() => {});
    }

    function showRemoteChangedBanner() {
      const banner = $('#remote-changed-banner');
      if (banner) banner.hidden = false;
    }

    function pollSync() {
      fetch('/_/api/sync').then(r => r.json()).then(s => {
        updateSyncSeg(s);
        if (s.pagesChanged && s.pagesChanged.length > 0 && pageContent) {
          const slug = document.body.dataset.slug || '';
          const routePrefix = document.body.dataset.routePrefix || '';
          const prefix = routePrefix === '/_/hidden' ? '.' : '';
          const pageFile = prefix + slug + '.md';
          if (s.pagesChanged.includes(pageFile)) {
            showRemoteChangedBanner();
          } else {
            refreshViewedPage();
          }
        }
        if (s.state === 'pending' || bidi) {
          pollTimer = setTimeout(pollSync, pollMs);
        }
      }).catch(() => {
        if (bidi) pollTimer = setTimeout(pollSync, pollMs);
      });
    }
    if (syncSeg.dataset.state === 'pending' || bidi) pollSync();

    // Tick sync age every second
    syncAgeTimer = setInterval(updateSyncAge, 30000);
  }

  // ---- New-page shortcut (ctrl-j) ---- targets this page's namespace when it
  // has a `new:` template; disabled otherwise (window.hmdNewEnabled).
  //
  // The server renders the draft straight into this response instead of
  // creating+redirecting, so nothing is saved until the user hits Save
  // (Cancel on the editor leaves no trace). Since there's no redirect to
  // follow for the pretty /<slug> URL, swap the returned page in directly
  // and set the URL via pushState instead of navigating.
  function openNewPage() {
    fetch('/_/new?ns=' + encodeURIComponent(window.hmdNewNamespace), { method: 'POST' }).then(r => {
      if (!r.ok) return;
      r.text().then(html => {
        const slug = new DOMParser().parseFromString(html, 'text/html').querySelector('#cm-host')?.dataset.slug;
        if (slug) history.pushState(null, '', '/' + slug + '?do=edit');
        document.open();
        document.write(html);
        document.close();
      });
    });
  }
  document.addEventListener('keydown', e => {
    if (!window.hmdNewEnabled) return;
    const mod = e.ctrlKey || e.metaKey;
    if (mod && (e.key === 'j' || e.key === 'J')) {
      e.preventDefault();
      openNewPage();
    }
  });

  // ---- Shared app bar ----
  const sidebarToggle = $('#sidebar-toggle');
  const sidebar = $('#sidebar');
  if (sidebarToggle && sidebar) {
    const COLLAPSE_KEY = 'hmd-sidebar-collapsed';
    if (localStorage.getItem(COLLAPSE_KEY) === '1') document.body.classList.add('sidebar-collapsed');
    sidebarToggle.addEventListener('click', () => {
      if (window.matchMedia('(max-width: 899px)').matches) {
        sidebar.classList.toggle('open');
        return;
      }
      const collapsed = document.body.classList.toggle('sidebar-collapsed');
      localStorage.setItem(COLLAPSE_KEY, collapsed ? '1' : '0');
    });
  }
  const topbarSearch = $('#topbar-search');
  if (topbarSearch) {
    topbarSearch.addEventListener('click', e => {
      e.preventDefault();
      if (sidebar) sidebar.classList.remove('open');
      openPalette();
    });
  }

  // ---- Sidebar: collapsible LOG section, remembers open/closed state ----
  const logDetails = $('#log-details');
  if (logDetails) {
    const savedLogOpen = localStorage.getItem('hmd-log-open');
    if (savedLogOpen !== null) logDetails.open = savedLogOpen === '1';
    logDetails.addEventListener('toggle', () => {
      localStorage.setItem('hmd-log-open', logDetails.open ? '1' : '0');
    });
  }

  // ---- Statusline scroll-progress fill ----
  const mainEl = $('main');
  if (mainEl) {
    let scrollRaf = null;

    function onMainScroll() {
      scrollRaf = null;
      const max = mainEl.scrollHeight - mainEl.clientHeight;
      const pct = max > 0 ? (mainEl.scrollTop / max) * 100 : 0;
      document.documentElement.style.setProperty('--scroll-pct', pct + '%');

    }
    mainEl.addEventListener('scroll', () => {
      if (scrollRaf) return;
      scrollRaf = requestAnimationFrame(onMainScroll);
    });
    onMainScroll();
  }

  // ---- editor: toggle preview pane for more writing space ----
  const togglePreviewBtn = $('#toggle-preview-btn');
  const splitEl = $('.split');
  if (togglePreviewBtn && splitEl) {
    const applyPreviewState = hidden => {
      splitEl.classList.toggle('no-preview', hidden);
      togglePreviewBtn.classList.toggle('active', hidden);
    };
    applyPreviewState(localStorage.getItem('hmd-hide-preview') === '1');
    togglePreviewBtn.addEventListener('click', () => {
      const hidden = !splitEl.classList.contains('no-preview');
      applyPreviewState(hidden);
      localStorage.setItem('hmd-hide-preview', hidden ? '1' : '0');
    });
  }

  // ---- ctrl-k palette ----
  const paletteBackdrop = $('#palette-backdrop');
  const paletteInput = $('#palette-input');
  const paletteResults = $('#palette-results');
  const paletteShortcuts = $('#palette-shortcuts');
  const paletteCount = $('#palette-count');

  let paletteOpen = false;
  let paletteRows = [];
  let paletteSelected = 0;
  let paletteCreateMode = false;
  let paletteVerbMode = false;  // input starts with ">" — rows are verbs
  let paletteVerbInput = null;  // a verb ('rename'|'tag') is awaiting its argument
  let searchTimer;
  const paletteMatchLimit = 5;

  function openPalette(createMode) {
    if (!paletteBackdrop) return;
    paletteOpen = true;
    paletteCreateMode = !!createMode;
    paletteVerbMode = false;
    paletteVerbInput = null;
    paletteBackdrop.classList.add('open');
    paletteInput.value = '';
    paletteInput.placeholder = paletteCreateMode ? 'title for new document…' : 'jump to a page…';
    paletteCount.textContent = '';
    paletteSelected = 0;
    if (paletteCreateMode) {
      paletteRows = [];
      renderPaletteRows('');
    } else {
      showRecent();
    }
    setTimeout(() => paletteInput.focus(), 0);
  }

  function closePalette() {
    if (!paletteBackdrop) return;
    paletteOpen = false;
    paletteVerbMode = false;
    paletteVerbInput = null;
    paletteBackdrop.classList.remove('open');
  }

  function showRecent() {
    if (!paletteResults) return;
    let recent = [];
    try { recent = JSON.parse(localStorage.getItem('hmd-recent') || '[]'); } catch (e) {}
    recent = recent.slice(0, paletteMatchLimit);
    paletteRows = recent.map(e => ({
      slug: e.slug, title: e.title, snippet: '', tags: [], create: false
    }));
    paletteSelected = 0;
    renderPaletteRows('');
  }

  // :hidden: and settings are generic navigation shortcuts — out of place
  // when the palette was opened specifically to create a new document.
  function builtinCount() {
    return (paletteCreateMode || paletteVerbMode) ? 0 : 2;
  }

  function renderPaletteRows(query) {
    if (!paletteResults) return;
    const builtinHtml = (paletteCreateMode || paletteVerbMode) ? '' :
      `<a href="/_/hidden" class="palette-row palette-builtin${paletteSelected === paletteRows.length ? ' selected' : ''}"><span class="filetype">hid</span><span class="title">:hidden:</span></a>` +
      `<a href="/_/settings" class="palette-row palette-settings${paletteSelected === paletteRows.length + 1 ? ' selected' : ''}"><span class="filetype">cfg</span><span class="title">settings</span></a>`;
    if (paletteShortcuts) paletteShortcuts.innerHTML = builtinHtml;
    if (paletteRows.length === 0 && !query) {
      const emptyMsg = paletteCreateMode ? 'Type a title for the new document…' : 'No recent pages';
      paletteResults.innerHTML = '<div class="palette-row" style="color:var(--fg-faint)">' + emptyMsg + '</div>';
      paletteCount.textContent = '';
      return;
    }
    const rowsHtml = paletteRows.map((r, i) => {
      if (r.verb) {
        return `<div class="palette-row palette-verb${i === paletteSelected ? ' selected' : ''}" data-action="${r.slug}">
          <span class="title">${escapeHtml(r.title)}</span>
          <span class="verb-desc">${escapeHtml(r.snippet)}</span>
        </div>`;
      }
      const tags = r.tags && r.tags.length ? ' <span class="hit-tags">' + r.tags.map(t => '<span class="hit-tag">' + escapeHtml(t) + '</span>').join(' ') + '</span>' : '';
      const owner = r.owner ? ' <span class="hit-tags">' + escapeHtml(r.owner) + '</span>' : '';
      const href = r.href || '/' + r.slug;
      // r.snippet comes from bleve's "html" highlighter, which already HTML-escapes
      // the surrounding text and only adds trusted <mark> tags around matches.
      return `<a href="${href}" class="palette-row${i === paletteSelected ? ' selected' : ''}">
        <span class="filetype">${r.attachment ? 'att' : 'md'}</span>
        <span class="title">${escapeHtml(r.title)}</span>
        <span class="snippet">${r.snippet || ''}</span>${tags}${owner}
      </a>`;
    }).join('');
    const createRow = (query && !paletteVerbMode && createNamespace())
      ? `<a href="${createHref(query)}" class="palette-row palette-create${paletteSelected === paletteRows.length + builtinCount() ? ' selected' : ''}">+ create page "${escapeHtml(query)}" in ${escapeHtml(createNamespace())}</a>`
      : '';
    paletteResults.innerHTML = rowsHtml + createRow;
    if (!paletteVerbMode) {
      paletteCount.textContent = paletteRows.length > 0 ? paletteRows.length + (query ? ' matches' : ' recent') : '';
    }
  }

  // Every page lives in a namespace, so the palette creates into the one the
  // current page is in, falling back to the quick-create namespace. With
  // neither (settings pages on a wiki with no namespaces) there is nowhere to
  // put a page and the create row stays hidden.
  function createNamespace() {
    return window.hmdNamespace || window.hmdNewNamespace || '';
  }

  function createHref(query) {
    return '/' + createNamespace() + '/' + slugifyQuery(query) + '?do=edit';
  }

  function slugifyQuery(q) {
    return q.toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  }

  function escapeHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  function relativeAge(unixSeconds) {
    const diff = Math.floor(Date.now() / 1000) - unixSeconds;
    if (diff < 60) return diff + 's ago';
    if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
    if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
    return Math.floor(diff / 86400) + 'd ago';
  }

  const verbs = [
    { name: 'rename', desc: 'rename this page', action: 'rename' },
    { name: 'tag', desc: 'edit tags', action: 'tag' },
    { name: 'hist', desc: 'view history', action: 'hist' },
    { name: 'new', desc: 'new page in this namespace', action: 'new' },
    { name: 'health', desc: 'wiki health', action: 'health' },
    { name: 'ns', desc: 'namespaces & templates', action: 'ns' },
    { name: 'sync', desc: 'push now', action: 'sync' },
    { name: 'pin', desc: 'pin this page', action: 'pin' },
    { name: 'delete', desc: 'delete this page', action: 'delete' },
  ].filter(v => {
    if (v.action === 'new') return window.hmdNewEnabled;
    return !window.hmdNamespaceIndex || !['rename', 'tag', 'hist', 'pin', 'delete'].includes(v.action);
  });

  function syncVerbDesc() {
    const seg = $('#status-sync');
    const last = seg ? parseInt(seg.dataset.lastSuccess || '0') : 0;
    if (!last) return 'push now';
    return `push now · last push ${relativeAge(last)} (${seg.dataset.state || 'unknown'})`;
  }

  function searchPalette(q) {
    paletteVerbMode = q.startsWith('>');

    if (paletteVerbMode) {
      const verbQuery = q.slice(1).toLowerCase();
      paletteRows = verbs
        .filter(v => v.name.includes(verbQuery))
        .map(v => ({
          slug: v.action,
          title: '>' + v.name,
          snippet: v.action === 'new'
            ? `new page in the ${window.hmdNewNamespace || 'current'} namespace`
            : v.action === 'sync' ? syncVerbDesc() : v.desc,
          verb: true
        }));
      paletteSelected = 0;
      renderPaletteRows(q);
      paletteCount.textContent = paletteRows.length + ' verb' + (paletteRows.length === 1 ? '' : 's');
      return;
    }

    if (!q) { showRecent(); return; }
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      Promise.all([
        fetch('/_/api/search?q=' + encodeURIComponent(q)).then(r => r.json()),
        fetch('/_/api/search/attachments?q=' + encodeURIComponent(q)).then(r => r.ok ? r.json() : [])
      ])
        .then(([pages, attachments]) => {
          const rows = pages.map(h => ({
            slug: h.slug, title: h.title, snippet: h.snippet, tags: h.tags || [], create: false
          })).concat(attachments.map(h => ({
            href: h.url, title: h.filename, snippet: h.excerpt, owner: h.owner_slug, attachment: true
          })));
          paletteRows = rows.slice(0, paletteMatchLimit);
          if (rows.length > paletteMatchLimit) {
            paletteRows.push({ href: '/_/search?q=' + encodeURIComponent(q), title: 'search all results', snippet: '' });
          }
          paletteSelected = 0;
          renderPaletteRows(q);
        })
        .catch(() => {});
    }, 150);
  }

  function paletteNavigate(dir) {
    const max = paletteRows.length + builtinCount() + (paletteInput.value && !paletteVerbMode ? 1 : 0);
    if (max === 0) return;
    paletteSelected = (paletteSelected + dir + max) % max;
    renderPaletteRows(paletteInput.value);
    const sel = $('.palette-row.selected', paletteResults);
    if (sel) sel.scrollIntoView({block: 'nearest'});
  }

  function executeVerb(action) {
    const slug = document.body.dataset.slug || '';
    switch (action) {
      case 'hist':
        closePalette();
        window.location.href = `/${slug}?do=history`;
        break;
      case 'new':
        closePalette();
        openNewPage();
        break;
      case 'health':
        closePalette();
        window.location.href = '/_/health-report';
        break;
      case 'ns':
        closePalette();
        window.location.href = '/_/namespaces';
        break;
      case 'sync':
        closePalette();
        fetch('/_/api/sync/push-now', { method: 'POST' })
          .then(r => r.json())
          .then(d => {
            const seg = $('#status-sync');
            if (seg) {
              seg.setAttribute('data-state', d.state);
              seg.setAttribute('data-last-success', d.last_success_unix || 0);
            }
          })
          .catch(() => {});
        break;
      case 'pin':
        closePalette();
        togglePin(slug);
        break;
      case 'rename':
      case 'tag':
        enterVerbInput(action);
        break;
      case 'delete':
        closePalette();
        if (!slug || !window.confirm(`Delete "${slug}"? Its history stays in git, but it disappears from the wiki.`)) return;
        fetch(`/${slug}?do=delete`, { method: 'POST' })
          .then(r => { if (r.ok) window.location.href = '/'; })
          .catch(() => {});
        break;
    }
  }

  // rename/tag take an argument: reuse the palette input as an inline prompt.
  function enterVerbInput(action) {
    const slug = document.body.dataset.slug || '';
    if (!slug) { closePalette(); return; }
    paletteVerbInput = action;
    paletteVerbMode = false;
    paletteRows = [];
    paletteResults.innerHTML = '';
    if (action === 'rename') {
      const titleEl = document.querySelector('.page-title');
      paletteInput.value = (titleEl && titleEl.dataset.title) || '';
      paletteInput.placeholder = 'new title…';
    } else {
      paletteInput.value = [...document.querySelectorAll('.page-meta .meta-tag')]
        .map(a => a.textContent.replace(/^#/, '')).join(', ');
      paletteInput.placeholder = 'tags, comma separated…';
    }
    paletteCount.textContent = '>' + action;
    paletteInput.focus();
    paletteInput.select();
  }

  function submitVerbInput() {
    const slug = document.body.dataset.slug || '';
    const value = paletteInput.value.trim();
    const action = paletteVerbInput;
    paletteVerbInput = null;
    if (action === 'rename') {
      if (!value) { closePalette(); return; }
      fetch(`/${slug}?do=rename`, { method: 'POST', body: new URLSearchParams({ title: value }) })
        .then(r => r.ok ? r.json() : Promise.reject())
        .then(d => { window.location.href = '/' + d.slug; })
        .catch(() => closePalette());
    } else {
      fetch(`/${slug}?do=tags`, { method: 'POST', body: new URLSearchParams({ tags: value }) })
        .then(() => window.location.reload())
        .catch(() => closePalette());
    }
  }

  function togglePin(slug) {
    let pinned = [];
    try { pinned = JSON.parse(localStorage.getItem('hmd-pinned') || '[]'); } catch (e) {}
    if (pinned.includes(slug)) {
      pinned = pinned.filter(s => s !== slug);
    } else {
      pinned.push(slug);
    }
    localStorage.setItem('hmd-pinned', JSON.stringify(pinned));
    renderSidebarPages();
  }

  function paletteOpenSelected(editMode) {
    if (paletteVerbMode) {
      const row = paletteRows[paletteSelected];
      if (row) executeVerb(row.slug);
      return;
    }

    const isHidden = !paletteCreateMode && paletteSelected === paletteRows.length;
    const isSettings = !paletteCreateMode && paletteSelected === paletteRows.length + 1;
    const isCreate = paletteSelected === paletteRows.length + builtinCount() && paletteInput.value;
    if (isCreate) {
      if (!createNamespace()) return;
      window.location.href = createHref(paletteInput.value);
      return;
    }
    if (isHidden) {
      window.location.href = '/_/hidden';
      return;
    }
    if (isSettings) {
      window.location.href = '/_/settings';
      return;
    }
    const row = paletteRows[paletteSelected];
    if (row) window.location.href = row.href || '/' + row.slug + (editMode ? '?do=edit' : '');
  }

  if (paletteBackdrop) {
    paletteBackdrop.addEventListener('click', e => {
      if (e.target === paletteBackdrop) closePalette();
    });
  }
  if (paletteInput) {
    paletteInput.addEventListener('input', () => {
      if (paletteVerbInput) return; // inline verb argument, not a search
      searchPalette(paletteInput.value);
    });
    paletteInput.addEventListener('keydown', e => {
      if (e.key === 'Escape') { e.preventDefault(); closePalette(); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); paletteNavigate(1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); paletteNavigate(-1); }
      else if (e.key === 'Enter') {
        e.preventDefault();
        if (paletteVerbInput) { submitVerbInput(); return; }
        paletteOpenSelected(e.ctrlKey || e.metaKey);
      }
    });
  }
  if (paletteResults) {
    paletteResults.addEventListener('click', e => {
      const verbRow = e.target.closest('.palette-verb');
      if (verbRow) executeVerb(verbRow.dataset.action);
    });
  }

  // Global keybindings: ctrl/cmd-k and "/" open palette; ctrl-e → edit current page
  document.addEventListener('keydown', e => {
    const mod = e.ctrlKey || e.metaKey;
    const tag = (e.target.tagName || '').toLowerCase();
    const inField = tag === 'input' || tag === 'textarea' || e.target.isContentEditable;
    const cmFocused = document.activeElement && document.activeElement.closest && document.activeElement.closest('.cm-editor');

    if (mod && (e.key === 'k' || e.key === 'K')) {
      if (e.shiftKey) return; // ctrl-shift-k handled by editor link insert
      e.preventDefault();
      paletteOpen ? closePalette() : openPalette();
      return;
    }
    if (e.key === '/' && !inField && !cmFocused) {
      e.preventDefault();
      openPalette();
      return;
    }
    if (mod && (e.key === 'e' || e.key === 'E') && !inField && !cmFocused) {
      // A namespace listing has no page behind it — its slug is "ns/", which
      // ?do=edit would 404 on. Same gate the palette verbs and the topbar
      // Edit button already use.
      const slug = window.hmdNamespaceIndex ? '' : document.body.dataset.slug;
      const routePrefix = document.body.dataset.routePrefix || '';
      if (slug) { e.preventDefault(); window.location.href = routePrefix + '/' + slug + '?do=edit'; }
      return;
    }
    if (e.key === 'Escape' && paletteOpen) {
      closePalette();
    }
  });

  // ---- Edit page: mount CodeMirror editor ----
  const cmHost = $('#cm-host');
  if (cmHost && window.HMD) {
    const textarea = $('#editor-src');
    const preview = $('#preview');
    const dirtyMark = $('#dirty-mark');
    const statusContext = $('#status-context');
    const zenStatus = $('#zen-save-status');
    let previewTimeout;
    let dirty = false;
    let draftTimeout;

    // Zen mode state
    const zenState = {
      manuscript: false,
      typewriter: false,
      focus: false
    };

    // Load saved zen state
    try {
      const saved = JSON.parse(localStorage.getItem('hmd-zen'));
      if (saved) Object.assign(zenState, saved);
    } catch (e) { /* ignore */ }

    function saveZenState() {
      localStorage.setItem('hmd-zen', JSON.stringify(zenState));
    }

    // Draft restoration — check for saved draft on load
    const draftKey = 'hmd-draft-' + cmHost.dataset.slug;
    const savedDraft = localStorage.getItem(draftKey);
    if (savedDraft && savedDraft !== textarea.value) {
      if (confirm('A saved draft was found for this page. Restore it?')) {
        textarea.value = savedDraft;
      } else {
        localStorage.removeItem(draftKey);
      }
    }

    function saveDraft() {
      localStorage.setItem(draftKey, textarea.value);
    }

    // Word count — updates both the statusline context segment and zen status
    function updateWordCount() {
      const text = textarea.value;
      const words = text.trim() ? text.trim().split(/\s+/).length : 0;
      const chars = text.length;
      if (statusContext) statusContext.textContent = words + ' words · ' + chars + ' chars';
      if (zenStatus) zenStatus.textContent = words + ' words';
    }

    const breadcrumbDirty = $('#breadcrumb-dirty');

    function markDirty() {
      dirty = true;
      if (dirtyMark) dirtyMark.style.display = 'inline';
      if (breadcrumbDirty) breadcrumbDirty.hidden = false;
    }

    // Typewriter scroll — keep cursor vertically centred (instant, not smooth)
    function scrollCursorToCenter() {
      if (!zenState.typewriter) return;
      const cursorCoords = editor.coordsAtPos(editor.state.selection.main.head);
      if (!cursorCoords) return;
      const editorRect = cmHost.getBoundingClientRect();
      const viewportHeight = editorRect.height;
      const cursorRelativeY = cursorCoords.top - editorRect.top;
      const targetY = viewportHeight / 2;
      const scrollDelta = cursorRelativeY - targetY;
      const scroller = cmHost.querySelector('.cm-scroller');
      if (scroller) {
        scroller.scrollBy({ top: scrollDelta, behavior: 'auto' });
      }
    }

    function applyZenState() {
      document.body.classList.toggle('zen-manuscript', zenState.manuscript);
      document.body.classList.toggle('zen-typewriter', zenState.typewriter);
      document.body.classList.toggle('zen-focus', zenState.focus);

      $$('.zen-toolbar button[data-zen]').forEach(btn => {
        btn.classList.toggle('active', zenState[btn.dataset.zen]);
      });

      if (zenState.typewriter) {
        scrollCursorToCenter();
      }
    }

    function toggleZen(mode) {
      zenState[mode] = !zenState[mode];
      saveZenState();
      applyZenState();
      if (mode === 'manuscript') {
        if (zenState.manuscript) startMousemoveListener();
        else stopMousemoveListener();
      }
    }

    // Word wrap — on by default, toggle persisted
    let wrapEnabled = localStorage.getItem('hmd-wrap') !== '0';

    // Create editor — zenState defined before listener references it
    function mountEditor() {
      return new HMD.EditorView({
        doc: textarea.value,
        extensions: [
          HMD.basicSetup,
          HMD.markdown(),
          HMD.EditorView.cspNonce.of(window.hmdCSPNonce),
          ...(wrapEnabled ? [HMD.EditorView.lineWrapping] : []),
          HMD.EditorView.updateListener.of(update => {
            if (update.docChanged) {
              textarea.value = update.state.doc.toString();
              dirty = true;
              markDirty();
              schedulePreview();
              updateWordCount();
              scheduleDraft();
            }
            if (update.selectionSet) {
              if (zenState.typewriter) scrollCursorToCenter();
            }
          })
        ],
        parent: cmHost
      });
    }
    let editor = mountEditor();

    const toggleWrapBtn = $('#toggle-wrap-btn');
    if (toggleWrapBtn) {
      toggleWrapBtn.classList.toggle('active', wrapEnabled);
      toggleWrapBtn.addEventListener('click', () => {
        wrapEnabled = !wrapEnabled;
        localStorage.setItem('hmd-wrap', wrapEnabled ? '1' : '0');
        toggleWrapBtn.classList.toggle('active', wrapEnabled);
        editor.destroy();
        editor = mountEditor();
        editor.focus();
      });
    }

    // Preview scheduling with error handling and loading indicator
    function schedulePreview() {
      clearTimeout(previewTimeout);
      previewTimeout = setTimeout(() => {
        preview.classList.add('preview-loading');
        preview.classList.remove('preview-error');
        fetch('/_/api/preview?slug=' + encodeURIComponent(document.body.dataset.slug || ''), {
          method: 'POST',
          body: textarea.value
        })
        .then(r => {
          if (!r.ok) throw new Error('Preview request failed');
          return r.text();
        })
        .then(html => {
          preview.innerHTML = html;
          preview.classList.remove('preview-loading');
          if (window.mermaid) {
            mermaid.run({querySelector: '#preview pre.mermaid'}).catch(() => {});
          }
        })
        .catch(err => {
          preview.classList.remove('preview-loading');
          preview.classList.add('preview-error');
          preview.innerHTML = '<p>Preview unavailable</p>';
        });
      }, 300);
    }

    // Draft auto-save (debounced)
    let draftSavedAt = null;

    function scheduleDraft() {
      clearTimeout(draftTimeout);
      draftTimeout = setTimeout(() => {
        saveDraft();
        draftSavedAt = Date.now();
        updateDraftStatus();
      }, 3000);
    }

    function updateDraftStatus() {
      if (!statusContext) return;
      if (!draftSavedAt) return;
      const secs = Math.floor((Date.now() - draftSavedAt) / 1000);
      if (dirty) {
        statusContext.textContent = 'draft saved ' + secs + 's ago';
        setTimeout(updateDraftStatus, 5000);
      } else {
        updateWordCount();
      }
    }

    // Zen mode button handlers
    $$('.zen-toolbar button[data-zen]').forEach(btn => {
      btn.addEventListener('click', () => toggleZen(btn.dataset.zen));
    });

    // Exit manuscript mode on save (so form submits correctly)
    const editForm = $('#edit-form');
    if (editForm) {
      editForm.addEventListener('submit', e => {
        if (zenState.manuscript) toggleZen('manuscript');
        if (!navigator.onLine) {
          e.preventDefault();
          queueFormSubmit(editForm);
          dirty = false;
          localStorage.removeItem(draftKey);
          if (statusContext) statusContext.textContent = 'offline — save queued, will sync when back online';
          return;
        }
        dirty = false;
        localStorage.removeItem(draftKey);
      });
    }

    // ctrl-s → submit the edit form
    document.addEventListener('keydown', e => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 's' || e.key === 'S')) {
        e.preventDefault();
        if (editForm) editForm.requestSubmit();
      }
    });

    // Re-centre cursor on resize when typewriter mode is active
    let resizeTimer;
    window.addEventListener('resize', () => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        if (zenState.typewriter) scrollCursorToCenter();
      }, 100);
    });

    // Unsaved changes warning
    window.addEventListener('beforeunload', e => {
      if (dirty) {
        e.preventDefault();
        e.returnValue = '';
      }
      saveDraft();
    });

    // Restore saved zen state on load
    applyZenState();

    // Scroll sync — editor scroll maps proportionally to preview
    let syncScrollSource = null;
    const scroller = cmHost.querySelector('.cm-scroller');
    if (scroller && preview) {
      scroller.addEventListener('scroll', () => {
        if (syncScrollSource === 'preview') return;
        syncScrollSource = 'editor';
        const maxScroll = scroller.scrollHeight - scroller.clientHeight;
        if (maxScroll > 0) {
          const ratio = scroller.scrollTop / maxScroll;
          const previewMax = preview.scrollHeight - preview.clientHeight;
          preview.scrollTop = ratio * previewMax;
        }
        requestAnimationFrame(() => { syncScrollSource = null; });
      });
      preview.addEventListener('scroll', () => {
        if (syncScrollSource === 'editor') return;
        syncScrollSource = 'preview';
        const maxScroll = preview.scrollHeight - preview.clientHeight;
        if (maxScroll > 0) {
          const ratio = preview.scrollTop / maxScroll;
          const editorMax = scroller.scrollHeight - scroller.clientHeight;
          scroller.scrollTop = ratio * editorMax;
        }
        requestAnimationFrame(() => { syncScrollSource = null; });
      });
    }

    // Toolbar actions — wrap selection in markers, or insert with cursor
    function wrapSelection(before, after) {
      const { from, to } = editor.state.selection.main;
      const selected = editor.state.sliceDoc(from, to);
      if (selected) {
        editor.dispatch({
          changes: { from, to, insert: before + selected + after },
          selection: { anchor: from + before.length, head: from + before.length + selected.length }
        });
      } else {
        editor.dispatch({
          changes: { from, insert: before + after },
          selection: { anchor: from + before.length }
        });
      }
      editor.focus();
    }

    function insertHeaderLevel(level) {
      const pos = editor.state.selection.main.head;
      const line = editor.state.doc.lineAt(pos);
      const prefix = '#'.repeat(level) + ' ';
      // Strip existing header prefix before inserting new one
      const existing = line.text.match(/^(#{1,6}\s*)/);
      const stripFrom = existing ? existing[1].length : 0;
      editor.dispatch({
        changes: { from: line.from, to: line.from + stripFrom, insert: prefix },
        selection: { anchor: pos - stripFrom + prefix.length }
      });
      editor.focus();
    }

    function insertLink() {
      const { from, to } = editor.state.selection.main;
      const selected = editor.state.sliceDoc(from, to);
      const insert = `[${selected}](url)`;
      const urlStart = from + 1 + selected.length + 2;
      editor.dispatch({
        changes: { from, to, insert },
        selection: { anchor: urlStart, head: urlStart + 3 }
      });
      editor.focus();
    }

    function insertTable() {
      const pos = editor.state.selection.main.head;
      const snippet = '\n| Header | Header |\n| --- | --- |\n|  |  |\n';
      // Place cursor in the first empty data cell
      const cursorPos = pos + snippet.length - 5;
      editor.dispatch({
        changes: { from: pos, insert: snippet },
        selection: { anchor: cursorPos, head: cursorPos }
      });
      editor.focus();
    }

    function insertImagePlaceholder() {
      const pos = editor.state.selection.main.head;
      const insert = '![alt](url)';
      editor.dispatch({
        changes: { from: pos, insert },
        selection: { anchor: pos + 2, head: pos + 5 }
      });
      editor.focus();
    }

    function insertTOC() {
      const pos = editor.state.selection.main.head;
      const insert = '\n<!-- hmd:toc -->\n';
      editor.dispatch({
        changes: { from: pos, insert },
        selection: { anchor: pos + insert.length }
      });
      editor.focus();
    }

    const attachmentInput = $('#attachment-input');
    const attachmentButton = $('.toolbar button[data-action="attachment"]');
    let attachmentUploads = 0;

    function updateAttachmentUploadStatus() {
      attachmentButton.disabled = attachmentUploads > 0;
      attachmentButton.textContent = attachmentUploads ? `uploading (${attachmentUploads})` : 'attach';
    }
    const toolbarActions = {
      bold: () => wrapSelection('**', '**'),
      italic: () => wrapSelection('_', '_'),
      header: () => insertHeaderLevel(1),
      link: insertLink,
      code: () => wrapSelection('`', '`'),
      table: insertTable,
      image: insertImagePlaceholder,
      attachment: () => attachmentInput.click(),
      toc: insertTOC
    };

    $$('.toolbar button[data-action]').forEach(btn => {
      btn.addEventListener('click', () => {
        const action = toolbarActions[btn.dataset.action];
        if (action) action();
      });
    });

    // Run preview and word count once on load
    schedulePreview();
    updateWordCount();

    // Handle attachment uploads, with paste and drop remaining image-only.
    function escapeMarkdownText(text) {
      return text.replace(/[\[\]()]/g, '\\$&');
    }

    function handleAttachmentFiles(files, imagesOnly) {
      for (let file of files) {
        if (imagesOnly && !file.type.startsWith('image/')) continue;

        attachmentUploads++;
        updateAttachmentUploadStatus();

        const formData = new FormData();
        formData.append('file', file);

        fetch(`/_/api/attachments/${cmHost.dataset.slug}`, {
          method: 'POST',
          body: formData
        })
        .then(r => {
          if (!r.ok) throw new Error('Upload failed');
          return r.json();
        })
        .then(data => {
          const name = escapeMarkdownText(file.name);
          const markdown = file.type.startsWith('image/') ? `![${name}](${data.url})\n` : `[${name}](${data.url})\n`;
          editor.dispatch({
            changes: {
              from: editor.state.selection.main.head,
              insert: markdown
            }
          });
        })
        .catch(err => {
          alert('Failed to upload attachment: ' + file.name);
        })
        .finally(() => {
          attachmentUploads--;
          updateAttachmentUploadStatus();
        });
      }
    }

    attachmentInput.addEventListener('change', () => {
      handleAttachmentFiles(attachmentInput.files, false);
      attachmentInput.value = '';
    });

    cmHost.addEventListener('paste', e => {
      if (e.clipboardData.files && e.clipboardData.files.length) {
        const hasImages = Array.from(e.clipboardData.files).some(f => f.type.startsWith('image/'));
        if (hasImages) {
          e.preventDefault();
          handleAttachmentFiles(e.clipboardData.files, true);
        }
      }
    });

    cmHost.addEventListener('drop', e => {
      if (e.dataTransfer.files && e.dataTransfer.files.length) {
        const hasImages = Array.from(e.dataTransfer.files).some(f => f.type.startsWith('image/'));
        if (hasImages) {
          e.preventDefault();
          handleAttachmentFiles(e.dataTransfer.files, true);
        }
      }
    });

    cmHost.addEventListener('dragover', e => {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
    });

    // Keyboard shortcuts. Uses Alt for headers and Ctrl+Shift for zen modes
    // to avoid conflicts with browser shortcuts.
    cmHost.addEventListener('keydown', e => {
      const mod = e.ctrlKey || e.metaKey;

      // Zen mode shortcuts (Ctrl+Shift+F/T/D)
      if (mod && e.shiftKey && (e.key === 'F' || e.key === 'f')) {
        e.preventDefault();
        toggleZen('manuscript');
        return;
      }

      if (mod && e.shiftKey && (e.key === 'T' || e.key === 't')) {
        e.preventDefault();
        toggleZen('typewriter');
        return;
      }

      if (mod && e.shiftKey && (e.key === 'D' || e.key === 'd')) {
        e.preventDefault();
        toggleZen('focus');
        return;
      }

      // Header shortcuts (Alt+1/2/3)
      if (e.altKey && !mod && (e.key === '1' || e.key === '2' || e.key === '3')) {
        e.preventDefault();
        insertHeaderLevel(parseInt(e.key));
        return;
      }

      if (!mod) return;

      if (!e.shiftKey && (e.key === 'b' || e.key === 'B')) {
        e.preventDefault();
        wrapSelection('**', '**');
      } else if (!e.shiftKey && (e.key === 'i' || e.key === 'I')) {
        e.preventDefault();
        wrapSelection('_', '_');
      } else if (e.shiftKey && (e.key === 'k' || e.key === 'K')) {
        // ctrl-shift-k: insert link (palette owns plain ctrl-k)
        e.preventDefault();
        insertLink();
      }
    });

    // Document-level Escape handler (works even when editor isn't focused)
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape' && zenState.manuscript) {
        e.preventDefault();
        toggleZen('manuscript');
      }
    });

    // Floating status bar for manuscript mode — only active when manuscript is on
    const zenStatusBar = $('#zen-status');
    const zenExitBtn = $('#zen-exit');
    let statusTimeout;
    let mousemoveActive = false;

    // Exit button in the floating status bar
    if (zenExitBtn) {
      zenExitBtn.addEventListener('click', () => toggleZen('manuscript'));
    }

    function startMousemoveListener() {
      if (mousemoveActive) return;
      mousemoveActive = true;
      document.addEventListener('mousemove', handleMousemove);
    }

    function stopMousemoveListener() {
      if (!mousemoveActive) return;
      mousemoveActive = false;
      document.removeEventListener('mousemove', handleMousemove);
    }

    function handleMousemove() {
      if (!zenState.manuscript) {
        stopMousemoveListener();
        return;
      }
      zenStatusBar.classList.add('visible');
      clearTimeout(statusTimeout);
      statusTimeout = setTimeout(() => {
        zenStatusBar.classList.remove('visible');
      }, 3000);
    }

    // If manuscript mode is active on load, start listener
    if (zenState.manuscript) {
      startMousemoveListener();
    }
  }

  // ---- History page: checkbox and diff viewer ----
  const revCheckboxes = $$('.rev-checkbox');
  const diffPanel = $('#diff-panel');
  const diffStatus = $('#diff-status');
  const diffHeader = $('#diff-header');
  const diffBody = $('#diff-body');
  if (revCheckboxes.length > 0 && diffPanel) {
    let checked = [];

    revCheckboxes.forEach(cb => {
      cb.addEventListener('change', e => {
        if (e.target.checked) {
          if (checked.length >= 2) {
            const oldest = checked.shift();
            oldest.checked = false;
          }
          checked.push(e.target);
        } else {
          checked = checked.filter(c => c !== e.target);
        }
        updateDiffDisplay();
      });
    });

    function updateDiffDisplay() {
      if (checked.length === 0) {
        diffPanel.style.display = 'none';
        diffStatus.textContent = '';
        return;
      }

      if (checked.length === 1) {
        diffPanel.style.display = 'none';
        diffStatus.textContent = checked[0].value.slice(0, 7) + ' selected';
        return;
      }

      // Two checked: show diff
      const hashA = checked[0].value;
      const hashB = checked[1].value;
      diffStatus.textContent = hashA.slice(0, 7) + ' ↔ ' + hashB.slice(0, 7) + ' selected';

      const slug = document.body.dataset.slug;
      fetch(`/${slug}?do=diff&a=${hashA}&b=${hashB}`)
        .then(r => r.text())
        .then(diff => {
          diffHeader.textContent = `diff ${hashA.slice(0, 7)}..${hashB.slice(0, 7)}`;
          renderDiff(diff);
          diffPanel.style.display = 'block';
        })
        .catch(() => {});
    }

    function renderDiff(unifiedDiff) {
      const lines = unifiedDiff.split('\n');
      const html = lines.map(line => {
        let cls = 'diff-context';
        let prefix = ' ';
        if (line.startsWith('+++') || line.startsWith('---')) return '';
        if (line.startsWith('@@')) return `<div class="diff-line">${escapeHtml(line)}</div>`;
        if (line.startsWith('+')) {
          cls = 'diff-add';
        } else if (line.startsWith('-')) {
          cls = 'diff-delete';
        }
        return `<div class="diff-line ${cls}">${escapeHtml(line)}</div>`;
      }).join('');
      diffBody.innerHTML = `<pre>${html}</pre>`;
    }
  }
})();
