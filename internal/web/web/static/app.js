// CodeMirror version: see package.json in build directory
// Mermaid: @11

(function() {
  const $ = (s, p) => (p || document).querySelector(s);
  const $$ = (s, p) => Array.from((p || document).querySelectorAll(s));

  // components.js is a separate deferred file; skip optional enhancements if it failed to load.
  const ui = window.HMDComponents;
  if (ui) $$('.settings-advanced > details, .settings-selector > details').forEach(details => ui.enhanceDisclosure(details));

  // The mobile settings menu scrolls on its own; when it opens, centre the
  // current page in it so a nested namespace section is visible straight away.
  $$('.settings-selector > details').forEach(details => {
    details.addEventListener('toggle', () => {
      const nav = details.querySelector('.settings-mobile-nav');
      const current = nav && nav.querySelector('[aria-current="page"]');
      if (!details.open || !current) return;
      const offset = current.getBoundingClientRect().top - nav.getBoundingClientRect().top;
      nav.scrollTop += offset - (nav.clientHeight - current.offsetHeight) / 2;
    });
  });

  // ---- Offline edit queue: queue form POSTs made while offline, flush on reconnect ----
  const OFFLINE_QUEUE_KEY = 'hmd-offline-queue';

  function loadQueue() {
    try { return JSON.parse(localStorage.getItem(OFFLINE_QUEUE_KEY)) || []; }
    catch (e) { return []; }
  }

  function saveQueue(queue) {
    localStorage.setItem(OFFLINE_QUEUE_KEY, JSON.stringify(queue));
  }

  function queueMutation(url, body) {
    const queue = loadQueue();
    queue.push({ url, body, ts: Date.now() });
    saveQueue(queue);
  }

  async function flushOfflineQueue() {
    const queue = loadQueue();
    while (queue.length) {
      const item = queue[0];
      try {
        const res = await fetch(item.url, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(item.body)
        });
        if (!res.ok) break;
      } catch (e) {
        break; // still offline (or server unreachable); retry on the next 'online' event
      }
      queue.shift();
      saveQueue(queue);
    }
  }

  window.addEventListener('online', flushOfflineQueue);
  if (navigator.onLine) flushOfflineQueue();

  // ---- Shared JSON mutation helpers ----
  // Every page mutation posts JSON to the httpapi surface; server-rendered
  // navigation still owns GET routes. A failed response carries a safe message
  // and, on 409, the current revision so the caller can keep the draft.
  async function mutateJSON(url, body) {
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });
    if (!res.ok) {
      let detail = {};
      try { detail = await res.json(); } catch (e) { /* non-JSON error body */ }
      const err = new Error(detail.error || window.hmdT('js.request_failed', { Status: res.status }));
      err.status = res.status;
      err.detail = detail;
      throw err;
    }
    return res.json();
  }

  function currentRoutePrefix() {
    return document.body.dataset.routePrefix || '';
  }

  function isHiddenPage() {
    return currentRoutePrefix() === '/_/hidden';
  }

  // Page URLs are built here and nowhere else, mirroring PageURL and
  // HiddenPageURL in internal/wiki/url.go.
  function viewHref(slug, hidden) {
    return (hidden ? '/_/hidden/' : '/') + encodeSlugPath(slug);
  }

  // slugFromPath is the inverse of viewHref for ordinary pages: the slug a
  // URL path names, or '' for an app route under /_/.
  function slugFromPath(pathname) {
    if (pathname.startsWith('/_/')) return '';
    return pathname.slice(1).replace(/\/$/, '').split('/').map(segment => {
      try {
        return decodeURIComponent(segment);
      } catch (e) {
        return segment;
      }
    }).join('/');
  }

  // Parse a comma-separated tag field into the JSON string array the API takes.
  function parseTagsInput(value) {
    return value.split(',').map(t => t.trim()).filter(t => t.length > 0);
  }

  // Shared page.js owns reading enhancements, including Mermaid rendering.
  let pageContent = $('#page-content');

  // ---- Pages sidebar: merged pinned + recent (client-side, localStorage) ----
  // Content owns the root, and every app route lives under /_/, so any
  // path outside /_/ is a real page's own URL, and the slug is just the
  // path with its leading slash stripped.
  const recentList = $('#recent-list');
  const currentSlug = slugFromPath(window.location.pathname);
  // Namespaces are one level deep (mirrors namespaceFor in namespace.go):
  // "blog/drafts/post" belongs to "blog", a bare slug belongs to root ("").
  const namespaceOf = slug => { const i = slug.indexOf('/'); return i === -1 ? '' : slug.slice(0, i); };
  const currentNs = namespaceOf(document.body.dataset.slug || '');

  if (recentList) {
    try {
      // Only a page's own view carries data-title; history and other screens
      // under a page's URL must not rename it.
      const titleEl = $('.page-title[data-title]');
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
      // Markers follow the title so every sidebar row's text starts at the same edge.
      const glyph = e.pinned ? '<span class="pin-glyph">★</span>' : '';
      return `<li><a href="${escapeHtml(viewHref(e.slug))}"${e.slug === currentSlug ? ' class="active"' : ''}>${escapeHtml(e.title)}</a>${dot}${glyph}</li>`;
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

          const slug = slugFromPath(new URL(href, window.location.href).pathname);
          fetch('/_/api/preview/' + encodeURIComponent(slug))
            .then(r => r.json())
            .then(data => {
              const rect = link.getBoundingClientRect();
              const tags = (data.tags || []).map(t => '<span class="card-tag">' + escapeHtml(t) + '</span>').join(' ');
              previewCard.innerHTML = `
                <h3><span class="h"></span>${escapeHtml(data.title)}</h3>
                <div class="snippet">${escapeHtml(data.snippet)}</div>
                <div class="meta">${tags ? tags + ' · ' : ''}${escapeHtml(window.hmdT('js.preview_card.edited', { Age: data.age }))}</div>
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
    wordCountSeg.textContent = window.hmdT('js.reading_summary', { Words: words, Minutes: readingTime });
  }

  // ---- Sync polling ----
  const syncSeg = $('#status-sync');
  if (syncSeg) {
    const pollMs = window.hmdSyncPollMs || 10000;
    const bidi = window.hmdSyncMode === 'bidirectional';
    let pollTimer;
    let lastRenderedHash = pageContent ? (pageContent.dataset.blobHash || '') : null;
    const syncText = $('#sync-text', syncSeg) || syncSeg;

    function updateSyncAge() {
      const state = syncSeg.dataset.state || 'unknown';
      const last = parseInt(syncSeg.dataset.lastSuccess || '0');
      if (state === 'no remote') {
        syncText.textContent = window.hmdT('js.sync.local_only');
        return;
      }
      if (state === 'ok' && last > 0) {
        syncText.textContent = window.hmdT('js.sync.synced', { Age: relativeAge(last) });
        return;
      }
      const arrow = window.hmdReadOnly ? '⇣' : (bidi ? '⇣⇡' : '⇡');
      syncText.textContent = arrow + ' ' + window.hmdT('js.sync.state.' + state) + (last > 0 ? ' · ' + relativeAge(last) : '');
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

    // Tick sync age every 30 seconds
    setInterval(updateSyncAge, 30000);
  }

  // ---- New-page shortcut (ctrl-j) ---- targets this page's namespace when it
  // has a `new:` template; disabled otherwise (window.hmdNewEnabled).
  //
  // The server renders the draft straight into the /_/new response instead of
  // creating+redirecting, so nothing is saved until the user hits Save
  // (Cancel on the editor leaves no trace). Navigate to that response and let
  // the edit page rewrite the address bar to the pretty /<slug> URL, rather
  // than injecting the whole document client-side with document.write.
  function openNewPage() {
    window.location.href = '/_/new?ns=' + encodeURIComponent(window.hmdNewNamespace);
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
  // The toggle only exists on mobile, where it opens the sidebar drawer.
  if (sidebarToggle && sidebar) {
    sidebarToggle.addEventListener('click', () => sidebar.classList.toggle('open'));
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
    if (ui) ui.enhanceDisclosure(logDetails);
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

  // ---- ctrl-k palette ----
  const paletteBackdrop = $('#palette-backdrop');
  const paletteInput = $('#palette-input');
  const paletteResults = $('#palette-results');
  const paletteShortcuts = $('#palette-shortcuts');
  const paletteCount = $('#palette-count');

  let paletteOpen = false;
  let paletteReturnFocus = null;
  let paletteRows = [];
  let paletteSelected = 0;
  let paletteCreateMode = false;
  let paletteVerbMode = false;  // input starts with ">", so rows are verbs
  let paletteVerbInput = null;  // a verb ('rename'|'tag') is awaiting its argument
  let searchTimer;
  const paletteMatchLimit = 5;

  function openPalette(createMode) {
    if (!paletteBackdrop) return;
    if (!paletteOpen) paletteReturnFocus = document.activeElement;
    paletteOpen = true;
    paletteCreateMode = !!createMode;
    paletteVerbMode = false;
    paletteVerbInput = null;
    paletteBackdrop.classList.add('open');
    paletteInput.value = '';
    paletteInput.placeholder = window.hmdT(paletteCreateMode ? 'js.palette.title_placeholder' : 'js.palette.jump_placeholder');
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
    const prior = paletteReturnFocus;
    paletteReturnFocus = null;
    if (prior && typeof prior.focus === 'function' && prior.isConnected !== false) prior.focus();
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

  // :hidden: and settings are generic navigation shortcuts, out of place
  // when the palette was opened specifically to create a new document.
  function builtinCount() {
    return (paletteCreateMode || paletteVerbMode) ? 0 : 2;
  }

  function renderPaletteRows(query) {
    if (!paletteResults) return;
    const builtinHtml = (paletteCreateMode || paletteVerbMode) ? '' :
      `<a href="/_/hidden" class="palette-row palette-builtin${paletteSelected === paletteRows.length ? ' selected' : ''}"><span class="filetype">hid</span><span class="title">:hidden:</span></a>` +
      `<a href="/_/settings" class="palette-row palette-settings${paletteSelected === paletteRows.length + 1 ? ' selected' : ''}"><span class="filetype">cfg</span><span class="title">${escapeHtml(window.hmdT('js.palette.settings'))}</span></a>`;
    if (paletteShortcuts) paletteShortcuts.innerHTML = builtinHtml;
    if (paletteRows.length === 0 && !query) {
      const emptyMsg = window.hmdT(paletteCreateMode ? 'js.palette.empty_create' : 'js.palette.empty_recent');
      paletteResults.innerHTML = '<div class="palette-row" style="color:var(--fg-faint)">' + emptyMsg + '</div>';
      paletteCount.textContent = '';
      return;
    }
    const rowsHtml = paletteRows.map((r, i) => {
      if (r.verb) {
        return `<div class="palette-row palette-verb${i === paletteSelected ? ' selected' : ''}" data-action="${escapeHtml(r.slug)}">
          <span class="title">${escapeHtml(r.title)}</span>
          <span class="verb-desc">${escapeHtml(r.snippet)}</span>
        </div>`;
      }
      const tags = r.tags && r.tags.length ? ' <span class="hit-tags">' + r.tags.map(t => '<span class="hit-tag">' + escapeHtml(t) + '</span>').join(' ') + '</span>' : '';
      const owner = r.owner ? ' <span class="hit-tags">' + escapeHtml(r.owner) + '</span>' : '';
      const href = r.href || viewHref(r.slug);
      // r.snippet comes from bleve's "html" highlighter, which already HTML-escapes
      // the surrounding text and only adds trusted <mark> tags around matches.
      return `<a href="${escapeHtml(href)}" class="palette-row${i === paletteSelected ? ' selected' : ''}">
        <span class="filetype">${r.attachment ? 'att' : 'md'}</span>
        <span class="title">${escapeHtml(r.title)}</span>
        <span class="snippet">${r.snippet || ''}</span>${tags}${owner}
      </a>`;
    }).join('');
    const createRow = (query && !paletteVerbMode && createNamespace())
      ? `<a href="${escapeHtml(createHref(query))}" class="palette-row palette-create${paletteSelected === paletteRows.length + builtinCount() ? ' selected' : ''}">${escapeHtml(window.hmdT('js.palette.create', { Title: query, Namespace: createNamespace() }))}</a>`
      : '';
    paletteResults.innerHTML = rowsHtml + createRow;
    if (!paletteVerbMode) {
      paletteCount.textContent = paletteRows.length > 0 ? (query ? window.hmdT('js.palette.matches', {}, paletteRows.length) : window.hmdT('js.palette.recent', { Count: paletteRows.length })) : '';
    }
  }

  // Every page lives in a namespace, so the palette creates into the one the
  // current page is in, falling back to the quick-create namespace. With
  // neither (settings pages on a wiki with no namespaces) there is nowhere to
  // put a page and the create row stays hidden.
  function createNamespace() {
    if (window.hmdReadOnly) return '';
    return window.hmdNamespace || window.hmdNewNamespace || '';
  }

  function createHref(query) {
    return viewHref(createNamespace() + '/' + slugifyQuery(query)) + '?do=edit';
  }

  function slugifyQuery(q) {
    return q.toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  }

  // encodeSlugPath percent-encodes each segment of a page slug for use in a
  // URL. Recent pages stored by older versions kept the still-encoded
  // location.pathname, so decode before re-encoding to avoid double-encoding;
  // malformed input falls back to encoding the raw segment.
  function encodeSlugPath(slug) {
    return String(slug || '').split('/').map(segment => {
      try {
        return encodeURIComponent(decodeURIComponent(segment));
      } catch (e) {
        return encodeURIComponent(segment);
      }
    }).join('/');
  }

  function escapeHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  function relativeAge(unixSeconds) {
    const diff = Math.floor(Date.now() / 1000) - unixSeconds;
    if (diff < 60) return window.hmdT('js.age.seconds', { Count: diff });
    if (diff < 3600) return window.hmdT('js.age.minutes', { Count: Math.floor(diff / 60) });
    if (diff < 86400) return window.hmdT('js.age.hours', { Count: Math.floor(diff / 3600) });
    return window.hmdT('js.age.days', { Count: Math.floor(diff / 86400) });
  }

  const verbs = [
    { name: 'rename', descKey: 'js.verb.rename', action: 'rename' },
    { name: 'tag', descKey: 'js.verb.tag', action: 'tag' },
    { name: 'hist', descKey: 'js.verb.hist', action: 'hist' },
    { name: 'new', descKey: 'js.verb.new', action: 'new' },
    { name: 'health', descKey: 'js.verb.health', action: 'health' },
    { name: 'ns', descKey: 'js.verb.ns', action: 'ns' },
    { name: 'sync', descKey: 'js.verb.sync', action: 'sync' },
    { name: 'pin', descKey: 'js.verb.pin', action: 'pin' },
    { name: 'delete', descKey: 'js.verb.delete', action: 'delete' },
  ].filter(v => {
    if (window.hmdReadOnly && ['rename', 'tag', 'new', 'sync', 'pin', 'delete'].includes(v.action)) return false;
    if (v.action === 'new') return window.hmdNewEnabled;
    return !window.hmdNamespaceIndex || !['rename', 'tag', 'hist', 'pin', 'delete'].includes(v.action);
  });

  function syncVerbDesc() {
    const seg = $('#status-sync');
    const last = seg ? parseInt(seg.dataset.lastSuccess || '0') : 0;
    if (!last) return window.hmdT('js.verb.sync');
    return window.hmdT('js.verb.sync_last', { Age: relativeAge(last), State: seg.dataset.state || 'unknown' });
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
            ? window.hmdT('js.verb.new_in', { Namespace: window.hmdNewNamespace || window.hmdT('js.verb.current_namespace') })
            : v.action === 'sync' ? syncVerbDesc() : window.hmdT(v.descKey),
          verb: true
        }));
      paletteSelected = 0;
      renderPaletteRows(q);
      paletteCount.textContent = window.hmdT('js.palette.verbs', {}, paletteRows.length);
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
            paletteRows.push({ href: '/_/search?q=' + encodeURIComponent(q), title: window.hmdT('js.palette.search_all'), snippet: '' });
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
        window.location.href = viewHref(slug) + '?do=history';
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
        window.location.href = '/_/settings/namespaces';
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
        if (!slug || !window.confirm(window.hmdT('js.delete_confirm', { Slug: slug }))) return;
        mutateJSON('/_/api/pages/delete/' + encodeURIComponent(slug), { hidden: isHiddenPage() })
          .then(() => { window.location.href = isHiddenPage() ? '/_/hidden' : '/'; })
          .catch(err => alert(err.message || window.hmdT('js.error.delete_failed')));
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
      paletteInput.placeholder = window.hmdT('js.palette.rename_placeholder');
    } else {
      paletteInput.value = [...document.querySelectorAll('.page-meta .meta-tag')]
        .map(a => a.textContent.replace(/^#/, '')).join(', ');
      paletteInput.placeholder = window.hmdT('js.palette.tags_placeholder');
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
      mutateJSON('/_/api/pages/rename/' + encodeURIComponent(slug), { title: value })
        .then(d => { window.location.href = viewHref(d.slug, isHiddenPage()); })
        .catch(() => closePalette());
    } else {
      mutateJSON('/_/api/pages/tags/' + encodeURIComponent(slug), { tags: parseTagsInput(value) })
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
    if (row) {
      // Only follow same-origin paths. row.href comes from the search API and
      // row.slug from local storage, so neither can be trusted to be a path:
      // a leading "//" or "/\\" is a protocol-relative URL, and any scheme
      // (javascript:, data:) would execute in the page origin. Percent-encode
      // the slug before it reaches the URL.
      const target = row.href || viewHref(row.slug) + (editMode && !window.hmdReadOnly ? '?do=edit' : '');
      if (target.startsWith('/') && !target.startsWith('//') && !target.startsWith('/\\')) {
        window.location.href = target;
      }
    }
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
    // The editor owns ctrl-e for its preview.
    if (mod && (e.key === 'e' || e.key === 'E') && !inField && !cmFocused && !window.hmdReadOnly && !$('#edit-form')) {
      // A namespace listing has no page behind it. Its slug is "ns/", which
      // ?do=edit would 404 on. Same gate the palette verbs and the topbar
      // Edit button already use.
      const slug = window.hmdNamespaceIndex ? '' : document.body.dataset.slug;
      if (slug) { e.preventDefault(); window.location.href = viewHref(slug, isHiddenPage()) + '?do=edit'; }
      return;
    }
    if (e.key === 'Escape' && paletteOpen) {
      closePalette();
    }
  });

  // ---- Revert forms: native POST replaced by a JSON mutation ----
  $$('form.js-revert').forEach(form => {
    form.addEventListener('submit', e => {
      e.preventDefault();
      const slug = form.dataset.slug || currentSlug;
      const hashInput = form.querySelector('input[name="hash"]');
      mutateJSON('/_/api/pages/revert/' + encodeURIComponent(slug), { hash: hashInput ? hashInput.value : '' })
        .then(d => { window.location.href = viewHref(d.slug, false) + (d.index_warning ? '?index=pending' : ''); })
        .catch(err => alert(err.message || window.hmdT('js.error.revert_failed')));
    });
  });

  // ---- Edit page: mount CodeMirror editor ----
  const cmHost = $('#cm-host');
  // A draft served from /_/new belongs at its pretty edit URL. Rewrite the
  // address bar without a reload; reloading that URL loads the page itself
  // (still empty until Save), which matches the previous behaviour.
  if (cmHost && window.location.pathname === '/_/new') {
    const draftSlug = cmHost.dataset.slug;
    if (draftSlug) history.replaceState(null, '', viewHref(draftSlug) + '?do=edit');
  }
  if (cmHost && window.HMD) {
    const textarea = $('#editor-src');
    const preview = $('#preview');
    const dirtyMark = $('#dirty-mark');
    const statusContext = $('#status-context');
    const zenStatus = $('#zen-save-status');
    let previewTimeout;
    let dirty = false;
    let draftTimeout;

    // Zen mode state: focus hides everything but the writing and dims all
    // but the current line.
    const zenState = {
      focus: false
    };

    // Load saved zen state. Focus took over the old full-screen mode, whose
    // setting it inherits; the old dim-only focus has no setting of its own.
    try {
      const saved = JSON.parse(localStorage.getItem('hmd-zen'));
      if (saved) {
        zenState.focus = Boolean(saved.manuscript ?? saved.focus);
      }
    } catch (e) { /* ignore */ }

    function saveZenState() {
      localStorage.setItem('hmd-zen', JSON.stringify(zenState));
    }

    // Draft restoration: check for a saved draft on load
    const draftKey = 'hmd-draft-' + cmHost.dataset.slug;
    const savedDraft = localStorage.getItem(draftKey);
    if (savedDraft && savedDraft !== textarea.value) {
      if (confirm(window.hmdT('js.editor.restore_draft'))) {
        textarea.value = savedDraft;
      } else {
        localStorage.removeItem(draftKey);
      }
    }

    function saveDraft() {
      localStorage.setItem(draftKey, textarea.value);
    }

    // Word count: updates both the statusline context segment and zen status
    function updateWordCount() {
      const text = textarea.value;
      const words = text.trim() ? text.trim().split(/\s+/).length : 0;
      const chars = text.length;
      if (statusContext) statusContext.textContent = window.hmdT('js.editor.words', {}, words) + ' · ' + window.hmdT('js.editor.chars', {}, chars);
      if (zenStatus) zenStatus.textContent = window.hmdT('js.editor.words', {}, words);
    }

    const breadcrumbDirty = $('#breadcrumb-dirty');

    function markDirty() {
      dirty = true;
      if (dirtyMark) dirtyMark.style.display = 'inline';
      if (breadcrumbDirty) breadcrumbDirty.hidden = false;
    }

    // The editor grows with its document like a reading page, so the page's
    // own scroller (main) moves through it rather than a box inside it.
    const pageScroller = $('main') || document.scrollingElement;

    function applyZenState() {
      document.body.classList.toggle('zen-focus', zenState.focus);

      $$('.toolbar button[data-zen]').forEach(btn => {
        btn.setAttribute('aria-pressed', String(Boolean(zenState[btn.dataset.zen])));
      });
    }

    function toggleZen(mode) {
      zenState[mode] = !zenState[mode];
      // Focus is for writing; it has no room for the preview.
      if (mode === 'focus' && zenState.focus) setPreviewShown(false);
      saveZenState();
      applyZenState();
      if (mode === 'focus') {
        if (zenState.focus) startMousemoveListener();
        else stopMousemoveListener();
      }
    }

    const toolbar = $('.toolbar');

    // CodeMirror's own text (search panel, folding, screen reader notices) is
    // looked up by its English phrase. The bundle does not export EditorState,
    // so a detached editor lends its state's class for the phrases facet.
    const editorPhrases = (() => {
      const probe = new HMD.EditorView({ extensions: [HMD.EditorView.cspNonce.of(window.hmdCSPNonce)] });
      const EditorState = probe.state.constructor;
      probe.destroy();
      const keys = {
        'Find': 'find', 'Replace': 'replace_field', 'next': 'next', 'previous': 'previous', 'all': 'all',
        'match case': 'match_case', 'regexp': 'regexp', 'by word': 'by_word', 'replace': 'replace',
        'replace all': 'replace_all', 'close': 'close', 'current match': 'current_match',
        'replaced $ matches': 'replaced_matches', 'replaced match on line $': 'replaced_match_on_line',
        'on line': 'on_line', 'Go to line': 'go_to_line', 'go': 'go',
        'Folded lines': 'folded_lines', 'Unfolded lines': 'unfolded_lines', 'to': 'to',
        'Fold line': 'fold_line', 'Unfold line': 'unfold_line', 'folded code': 'folded_code', 'unfold': 'unfold',
        'Control character': 'control_character', 'Selection deleted': 'selection_deleted', 'Completions': 'completions'
      };
      const phrases = {};
      for (const [phrase, key] of Object.entries(keys)) phrases[phrase] = window.hmdT('js.editor.' + key);
      return EditorState.phrases.of(phrases);
    })();

    // Create the editor. zenState is defined before the listener references it. Lines
    // always wrap: the editor grows with the page, so a sideways scrollbar
    // would sit below the end of the document.
    const editor = new HMD.EditorView({
      doc: textarea.value,
      extensions: [
        HMD.basicSetup,
        HMD.markdown(),
        HMD.EditorView.cspNonce.of(window.hmdCSPNonce),
        editorPhrases,
        // Keep the cursor clear of the sticky toolbar when scrolling to it.
        HMD.EditorView.scrollMargins.of(() => ({ top: toolbar ? toolbar.offsetHeight : 0 })),
        HMD.EditorView.lineWrapping,
        HMD.EditorView.updateListener.of(update => {
          if (update.docChanged) {
            textarea.value = update.state.doc.toString();
            dirty = true;
            markDirty();
            schedulePreview();
            updateWordCount();
            scheduleDraft();
          }
        })
      ],
      parent: cmHost
    });

    // Focus mode text size, in tenths of the normal size, persisted.
    const zenSmallerBtn = $('#zen-smaller');
    const zenLargerBtn = $('#zen-larger');
    const zenScaleMin = 10, zenScaleMax = 16;
    let zenScale = Number(localStorage.getItem('hmd-zen-scale')) || 12;
    function applyZenScale() {
      zenScale = Math.min(zenScaleMax, Math.max(zenScaleMin, zenScale));
      document.body.style.setProperty('--zen-scale', String(zenScale / 10));
      if (zenSmallerBtn) zenSmallerBtn.disabled = zenScale <= zenScaleMin;
      if (zenLargerBtn) zenLargerBtn.disabled = zenScale >= zenScaleMax;
    }
    [[zenSmallerBtn, -1], [zenLargerBtn, 1]].forEach(([btn, step]) => {
      if (!btn) return;
      btn.addEventListener('click', () => {
        zenScale += step;
        localStorage.setItem('hmd-zen-scale', String(zenScale));
        applyZenScale();
        editor.focus();
      });
    });
    applyZenScale();

    // Preview replaces the source in the same column, so turning it on moves
    // nothing else on the page. It is only rendered while it is showing.
    const togglePreviewBtn = $('#toggle-preview-btn');
    const previewShown = () => Boolean(preview) && !preview.hidden;

    // Fraction of el scrolled past the top of the page scroller, so a swap
    // can land on the same part of the document.
    function scrollProgress(el) {
      const top = pageScroller.getBoundingClientRect().top;
      const rect = el.getBoundingClientRect();
      const range = rect.height - pageScroller.clientHeight;
      return range > 0 ? Math.min(1, Math.max(0, (top - rect.top) / range)) : 0;
    }

    function setPreviewShown(show) {
      if (show === previewShown() || (show && zenState.focus)) return;
      const from = show ? cmHost : preview;
      const to = show ? preview : cmHost;
      const progress = scrollProgress(from);
      from.hidden = true;
      to.hidden = false;
      if (togglePreviewBtn) togglePreviewBtn.setAttribute('aria-pressed', String(show));
      // Formatting edits the source, so it waits while the preview shows.
      $$('.toolbar button[data-action]').forEach(btn => { btn.disabled = show; });
      const restore = () => {
        if (progress === 0) return;
        const rect = to.getBoundingClientRect();
        const range = rect.height - pageScroller.clientHeight;
        if (range > 0) pageScroller.scrollTop += rect.top - pageScroller.getBoundingClientRect().top + progress * range;
      };
      // Focus follows the swap, so it never sits on the hidden pane.
      if (show) renderPreview().then(() => { restore(); preview.focus({ preventScroll: true }); });
      else { restore(); editor.focus(); }
    }

    if (togglePreviewBtn) {
      togglePreviewBtn.addEventListener('click', () => setPreviewShown(!previewShown()));
    }

    // Ctrl+E (Cmd+E on macOS) switches between writing and preview. On macOS
    // Ctrl+E stays the editor's move-to-line-end.
    const isMac = /Mac|iP(hone|ad|od)/.test(navigator.platform || '');
    document.addEventListener('keydown', e => {
      if ((isMac ? e.metaKey : e.ctrlKey) && !e.shiftKey && !e.altKey && (e.key === 'e' || e.key === 'E')) {
        e.preventDefault();
        setPreviewShown(!previewShown());
      }
    });

    function schedulePreview() {
      if (!previewShown()) return;
      clearTimeout(previewTimeout);
      previewTimeout = setTimeout(renderPreview, 300);
    }

    // Render the preview with error handling and a loading indicator.
    function renderPreview() {
      clearTimeout(previewTimeout);
      preview.classList.add('preview-loading');
      preview.classList.remove('preview-error');
      return fetch('/_/api/preview?slug=' + encodeURIComponent(document.body.dataset.slug || ''), {
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
        preview.innerHTML = '<p>' + escapeHtml(window.hmdT('js.preview.unavailable')) + '</p>';
      });
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
        statusContext.textContent = window.hmdT('js.editor.draft_saved', { Seconds: secs });
        setTimeout(updateDraftStatus, 5000);
      } else {
        updateWordCount();
      }
    }

    // Zen mode button handlers
    $$('.toolbar button[data-zen]').forEach(btn => {
      btn.addEventListener('click', () => toggleZen(btn.dataset.zen));
    });

    // Leave focus mode on save, and convert the native form submit into a
    // JSON mutation so a conflict or validation error renders inline and the
    // user's draft survives.
    const editForm = $('#edit-form');

    function editPayload() {
      const hiddenInput = editForm.querySelector('input[name="hidden"]');
      return {
        title: $('#title').value,
        body: textarea.value,
        tags: parseTagsInput($('#tags').value),
        new_slug: $('#new-slug').value,
        base_hash: editForm.querySelector('input[name="basehash"]').value,
        hidden: !!(hiddenInput && hiddenInput.checked),
        from_hidden: isHiddenPage()
      };
    }

    function showSaveError(message) {
      const el = $('#save-error');
      if (el) { el.textContent = message; el.hidden = false; }
    }

    function clearSaveError() {
      const el = $('#save-error');
      if (el) el.hidden = true;
      const banner = $('#conflict-banner');
      if (banner) banner.hidden = true;
    }

    function showConflict(conflict) {
      const baseInput = editForm.querySelector('input[name="basehash"]');
      if (baseInput && conflict.current_hash) baseInput.value = conflict.current_hash;
      const banner = $('#conflict-banner');
      if (!banner) return;
      const text = $('#conflict-text');
      if (text) text.textContent = window.hmdT('js.editor.conflict', { Hash: (conflict.current_hash || '').slice(0, 8) });
      banner.hidden = false;
    }

    function submitEditForm() {
      if (!editForm) return;
      clearSaveError();
      const slug = cmHost.dataset.slug || document.body.dataset.slug || '';
      const url = '/_/api/pages/' + encodeURIComponent(slug);
      const payload = editPayload();
      if (!navigator.onLine) {
        queueMutation(url, payload);
        dirty = false;
        localStorage.removeItem(draftKey);
        if (statusContext) statusContext.textContent = window.hmdT('js.editor.offline');
        return;
      }
      mutateJSON(url, payload)
        .then(d => {
          dirty = false;
          localStorage.removeItem(draftKey);
          window.location.href = viewHref(d.slug, payload.hidden) + (d.index_warning ? '?index=pending' : '');
        })
        .catch(err => {
          if (err.status === 409 && err.detail && err.detail.conflict) {
            showConflict(err.detail.conflict);
            return;
          }
          showSaveError(err.message || window.hmdT('js.error.save_failed'));
        });
    }

    if (editForm) {
      editForm.addEventListener('submit', e => {
        e.preventDefault();
        if (zenState.focus) toggleZen('focus');
        submitEditForm();
      });
      const overwriteBtn = $('#conflict-overwrite');
      if (overwriteBtn) overwriteBtn.addEventListener('click', submitEditForm);
    }

    const deleteForm = $('#delete-form');
    if (deleteForm) {
      deleteForm.addEventListener('submit', e => {
        // base.html's data-confirm handler runs first; respect its cancellation.
        if (e.defaultPrevented) return;
        e.preventDefault();
        const slug = cmHost.dataset.slug || document.body.dataset.slug || '';
        mutateJSON('/_/api/pages/delete/' + encodeURIComponent(slug), { hidden: isHiddenPage() })
          .then(() => { window.location.href = isHiddenPage() ? '/_/hidden' : '/'; })
          .catch(err => showSaveError(err.message || window.hmdT('js.error.delete_failed')));
      });
    }

    // ctrl-s → submit the edit form
    document.addEventListener('keydown', e => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 's' || e.key === 'S')) {
        e.preventDefault();
        // Call through the prototype: a control named "requestSubmit" would
        // otherwise shadow the method on the form object.
        if (editForm) HTMLFormElement.prototype.requestSubmit.call(editForm);
      }
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

    // Toolbar actions: wrap selection in markers, or insert with cursor
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
      attachmentButton.textContent = attachmentUploads ? window.hmdT('js.editor.uploading', { Count: attachmentUploads }) : window.hmdT('js.editor.attach');
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

    // Word count once on load; the preview renders when it is first shown.
    updateWordCount();

    // Handle attachment uploads, with paste and drop remaining image-only.
    function escapeMarkdownText(text) {
      return text.replace(/[\\\[\]()]/g, '\\$&');
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
          alert(window.hmdT('js.editor.upload_failed', { Name: file.name }));
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

      // Focus mode shortcut (Ctrl+Shift+F)
      if (mod && e.shiftKey && (e.key === 'F' || e.key === 'f')) {
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
      if (e.key === 'Escape' && zenState.focus) {
        e.preventDefault();
        toggleZen('focus');
      }
    });

    // Floating status bar for focus mode, only active when focus is on
    const zenStatusBar = $('#zen-status');
    const zenExitBtn = $('#zen-exit');
    let statusTimeout;
    let mousemoveActive = false;

    // Exit button in the floating status bar
    if (zenExitBtn) {
      zenExitBtn.addEventListener('click', () => toggleZen('focus'));
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
      if (!zenState.focus) {
        stopMousemoveListener();
        return;
      }
      zenStatusBar.classList.add('visible');
      clearTimeout(statusTimeout);
      statusTimeout = setTimeout(() => {
        zenStatusBar.classList.remove('visible');
      }, 3000);
    }

    // If focus mode is active on load, start listener
    if (zenState.focus) {
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
        diffPanel.hidden = true;
        diffStatus.textContent = '';
        return;
      }

      if (checked.length === 1) {
        diffPanel.hidden = true;
        diffStatus.textContent = window.hmdT('js.diff.selected_one', { Hash: checked[0].value.slice(0, 7) });
        return;
      }

      // Two checked: show diff
      const hashA = checked[0].value;
      const hashB = checked[1].value;
      diffStatus.textContent = window.hmdT('js.diff.selected_two', { A: hashA.slice(0, 7), B: hashB.slice(0, 7) });

      const slug = document.body.dataset.slug;
      fetch(viewHref(slug) + `?do=diff&a=${hashA}&b=${hashB}`)
        .then(r => r.text())
        .then(diff => {
          diffHeader.textContent = window.hmdT('js.diff.header', { A: hashA.slice(0, 7), B: hashB.slice(0, 7) });
          renderDiff(diff);
          diffPanel.hidden = false;
        })
        .catch(() => {});
    }

    function renderDiff(unifiedDiff) {
      const lines = unifiedDiff.split('\n');
      const html = lines.map(line => {
        let cls = 'diff-context';
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
  // ---- Settings, admin, namespace and setup mutation forms ----
  // These server-rendered forms post JSON to the httpapi surface; the browser
  // still owns GET navigation. Each form's action names its JSON endpoint, so
  // success redirects or updates in place and failures render inline. A 409
  // namespace conflict keeps the submitted settings and adopts the fresh hash.
  function field(form, name) { return $$('[name="' + name + '"]', form); }

  function fieldValue(form, name) {
    const els = field(form, name);
    if (!els.length) return '';
    const el = els[0];
    if (el.type === 'radio') { const chosen = els.find(e => e.checked); return chosen ? chosen.value : ''; }
    if (el.type === 'checkbox') return els.some(e => e.checked);
    return el.disabled ? '' : el.value;
  }

  function fieldOn(form, name) {
    return field(form, name).some(e => e.type === 'hidden' ? true : e.checked);
  }

  function fieldCheckedList(form, name) {
    return field(form, name).filter(e => e.checked).map(e => e.value);
  }

  function csvList(value) { return value.split(/[,\n]/).map(s => s.trim()).filter(Boolean); }

  function formError(form) {
    let el = form.querySelector('[data-form-error]');
    if (!el) {
      el = document.createElement('p');
      el.className = 'settings-error';
      el.setAttribute('role', 'alert');
      el.setAttribute('data-form-error', '');
      form.insertBefore(el, form.firstChild);
    }
    return el;
  }
  function showFormError(form, message) { const el = formError(form); el.textContent = message; el.hidden = false; }
  function clearFormError(form) { const el = form.querySelector('[data-form-error]'); if (el) el.hidden = true; }

  function jsonForm(form, build, onSuccess) {
    if (!form) return;
    form.addEventListener('submit', e => {
      if (e.defaultPrevented) return; // a data-confirm handler already cancelled it
      e.preventDefault();
      if (e.submitter && e.submitter.dataset.confirm && !window.confirm(e.submitter.dataset.confirm)) return;
      // formAction resolves to the current document URL when the button has no
      // formaction attribute, and a control named "action" shadows
      // form.action (a RadioNodeList with two submit buttons), so read the
      // submitter attribute and the form's action attribute directly.
      const url = (e.submitter && e.submitter.hasAttribute('formaction'))
        ? e.submitter.formAction
        : (form.getAttribute('action') || window.location.href);
      clearFormError(form);
      mutateJSON(url, build(form, e.submitter))
        .then(data => { if (onSuccess) onSuccess(data, form, url); else window.location.reload(); })
        .catch(err => {
          if (err.status === 409 && err.detail && err.detail.conflict) {
            const base = form.querySelector('[name="basehash"]');
            if (base && err.detail.conflict.current_hash) base.value = err.detail.conflict.current_hash;
            showFormError(form, window.hmdT('js.namespace.conflict'));
            return;
          }
          showFormError(form, err.message || window.hmdT('js.error.save_failed'));
        });
    });
  }

  // Server settings: every control carries its config.yaml key and kind, so the
  // payload is built from the form rather than listed field by field. Only
  // controls the user changed are sent, so displayed defaults are never frozen
  // into config.yaml; disabled controls (set by an environment variable) never are.
  function settingChanged(el) {
    if (el.type === 'checkbox') return el.checked !== el.defaultChecked;
    if (el.tagName === 'SELECT') return Array.from(el.options).some(o => o.selected !== o.defaultSelected);
    return el.value !== el.defaultValue;
  }

  function serverSettingsPayload(form) {
    const settings = {};
    form.querySelectorAll('[data-setting]').forEach(el => {
      if (el.disabled || el.readOnly || !settingChanged(el)) return;
      const key = el.dataset.setting;
      switch (el.dataset.kind) {
        case 'checkbox': settings[key] = el.checked; break;
        case 'number': settings[key] = el.value === '' ? null : Number(el.value); break;
        case 'list': settings[key] = csvList(el.value); break;
        default: settings[key] = el.value;
      }
    });
    return { settings };
  }

  // Settings screen: appearance, git author, tokens.
  // Each section has its own path, so return to it with the notice.
  // Dismissible notices hide until the browser session ends. A notice inside
  // a [data-dismiss] wrapper remembers its key in sessionStorage; the head
  // script hides remembered keys on later pages.
  document.addEventListener('click', event => {
    const dismiss = event.target.closest('.ui-notice__dismiss');
    if (!dismiss) return;
    const wrapper = dismiss.closest('[data-dismiss]');
    const target = wrapper || dismiss.closest('.ui-notice');
    if (wrapper) {
      try {
        const dismissed = JSON.parse(sessionStorage.getItem('hmd-dismissed') || '[]');
        if (!dismissed.includes(wrapper.dataset.dismiss)) dismissed.push(wrapper.dataset.dismiss);
        sessionStorage.setItem('hmd-dismissed', JSON.stringify(dismissed));
      } catch (e) { /* storage unavailable: hide for this page only */ }
    }
    target.hidden = true;
  });

  // Shutdown lives in the top-bar menu and the restart banner, so it is wired
  // outside the settings pages.
  (function () {
    const dialog = $('#shutdown-dialog');
    if (!dialog) return;
    const confirmBtn = $('#shutdown-confirm');
    const cancelBtn = $('#shutdown-cancel');
    const message = $('#shutdown-message');
    const error = $('#shutdown-error');
    $$('#shutdown-open, #restart-open').forEach(open => open.addEventListener('click', () => {
      const menu = open.closest('details');
      if (menu) menu.open = false;
      error.hidden = true;
      window.HMDComponents.openDialog(dialog, open);
    }));
    cancelBtn.addEventListener('click', () => dialog.close());
    confirmBtn.addEventListener('click', () => {
      confirmBtn.disabled = true;
      mutateJSON('/_/api/settings/shutdown', {})
        .then(() => {
          $('#shutdown-dialog-title').textContent = window.hmdT('js.shutdown.title');
          message.textContent = window.hmdT('js.shutdown.message');
          confirmBtn.hidden = true;
          cancelBtn.textContent = window.hmdT('js.shutdown.close');
        })
        .catch(err => {
          confirmBtn.disabled = false;
          error.textContent = err.message || window.hmdT('js.error.shutdown_failed');
          error.hidden = false;
        });
    });
  })();

  function settingsReturn(notice) {
    window.location.href = window.location.pathname + (notice ? '?' + notice : '');
  }
  jsonForm($('#appearance-form'), form => ({
    palette: fieldValue(form, 'palette'),
    font_ui: fieldValue(form, 'font_ui'),
    font_mono: fieldValue(form, 'font_mono'),
    skin: fieldValue(form, 'skin'),
    palette_explicit: fieldValue(form, 'palette_explicit') === '1'
  }), () => { settingsReturn('saved=1'); });

  jsonForm($('form[action="/_/api/settings/author"]'), form => ({ git_author: fieldValue(form, 'git_author') }),
    () => { settingsReturn('saved=1'); });

  jsonForm($('form[action="/_/api/settings/tokens"]'), form => ({
    label: fieldValue(form, 'label'),
    expiry: fieldValue(form, 'expiry'),
    scopes: fieldCheckedList(form, 'scopes'),
    namespaces: fieldCheckedList(form, 'namespaces')
  }), data => {
    const slot = $('#new-token-slot');
    const value = $('#new-token-value');
    const hint = $('#new-token-hint');
    if (slot && value) { value.value = data.token || ''; slot.hidden = false; }
    if (hint) hint.hidden = false;
    const label = $('#token_label');
    if (label) label.value = '';
  });

  $$('form.js-token-revoke').forEach(form => {
    jsonForm(form, f => ({ label: fieldValue(f, 'label') }), () => { window.location.reload(); });
  });

  // Admin screen: system configuration, users, export, wiki settings, setup.
  jsonForm($('form[action="/_/api/settings/server"]'), serverSettingsPayload,
    () => { settingsReturn('saved=1'); });
  jsonForm($('form[action="/_/api/settings/users"]'), form => ({
    name: fieldValue(form, 'name'),
    password: fieldValue(form, 'password'),
    scopes: fieldCheckedList(form, 'scopes')
  }), () => { settingsReturn('saved=1'); });
  $$('form[action="/_/api/settings/users/scopes"]').forEach(form => {
    jsonForm(form, f => ({ name: fieldValue(f, 'name'), scopes: fieldCheckedList(f, 'scopes') }),
      () => { settingsReturn('saved=1'); });
  });
  jsonForm($('#export-config-form'), () => ({}), () => { settingsReturn('exported=1'); });
  jsonForm($('form[action="/_/api/settings/wiki"]'), form => ({
    site_name: fieldValue(form, 'site_name'),
    landing: fieldValue(form, 'landing')
  }), () => { settingsReturn('wiki-saved=1'); });
  jsonForm($('form[action="/_/api/settings/setup"]'), () => ({}), () => { settingsReturn(); });
  jsonForm($('form[action="/_/api/settings/attachments/migrate"]'), () => ({}), () => { settingsReturn('attachments-moved=1'); });
  $$('form[action="/_/api/settings/attachments/delete"]').forEach(form => {
    jsonForm(form, f => ({ namespace: fieldValue(f, 'namespace') }), () => { settingsReturn('attachments-deleted=1'); });
  });
  jsonForm($('form[action="/_/api/settings/help/reset"]'), () => ({}), () => { settingsReturn(); });

  // Namespace editor: save, reset and delete share one form; the
  // submitter's formaction names the endpoint.
  jsonForm($('.namespace-form'), (form, submitter) => {
    const action = (submitter && submitter.hasAttribute('formaction')) ? submitter.formAction : '';
    if (/\/namespaces\/(reset|delete)$/.test(action)) {
      return { name: fieldValue(form, 'name') };
    }
    return {
      name: fieldValue(form, 'name'),
      template: fieldValue(form, 'template'),
      base_hash: fieldValue(form, 'basehash'),
      title: fieldValue(form, 'title'),
      description: fieldValue(form, 'description'),
      index: fieldValue(form, 'index'),
      tree: csvList(fieldValue(form, 'tree')),
      public: fieldOn(form, 'public'),
      skin: fieldValue(form, 'skin'),
      palette: fieldValue(form, 'palette'),
      new_enabled: fieldOn(form, 'new_enabled'),
      slug_preset: fieldValue(form, 'slug_preset'),
      slug_custom: fieldValue(form, 'slug_custom'),
      widgets: csvList(fieldValue(form, 'widgets')),
      export: {
        base_url: fieldValue(form, 'export_base_url'),
        sitemap: fieldOn(form, 'export_sitemap'),
        image: fieldValue(form, 'export_image'),
        robots_allow: Array.from(form.querySelectorAll('.export-bot:checked')).map(box => box.value).concat(csvList(fieldValue(form, 'export_bots'))),
        links: Array.from(form.querySelectorAll('.export-link-row')).map(row => ({
          label: row.querySelector('.export-link-label').value,
          url: row.querySelector('.export-link-url').value,
          icon: row.querySelector('.export-link-icon').value,
          location: row.querySelector('.export-link-location').value,
          icon_only: row.querySelector('.export-link-icon-only').checked
        }))
      }
    };
  }, (data, form, url) => {
    // Deletion, and resetting an empty namespace, can remove the editor.
    if (/\/namespaces\/(reset|delete)$/.test(url)) {
      window.location.href = '/_/settings/namespaces?saved=1';
      return;
    }
    // Stay on the editor section that was saved; a new namespace opens its first section.
    const name = fieldValue(form, 'name');
    window.location.href = /\/edit(\/|$)/.test(window.location.pathname)
      ? window.location.pathname + '?saved=1'
      : '/_/settings/namespaces/' + encodeURIComponent(name) + '/edit?saved=1';
  });

  // First-run setup wizard: submit reloads the current page.
  jsonForm($('.setup-modal form'), (form, submitter) => ({
    action: submitter ? submitter.value : 'add',
    setup_wiki: fieldOn(form, 'setup_wiki'),
    default_namespace: fieldValue(form, 'default_namespace'),
    new_namespace: fieldValue(form, 'new_namespace'),
    site_name: fieldValue(form, 'site_name'),
    add_namespace: fieldOn(form, 'add_namespace'),
    namespace: fieldValue(form, 'namespace'),
    add_help: fieldOn(form, 'add_help')
  }), () => { window.location.reload(); });
})();
