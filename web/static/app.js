// CodeMirror version: see package.json in build directory
// Mermaid: @11

(function() {
  const $ = (s, p) => (p || document).querySelector(s);
  const $$ = (s, p) => Array.from((p || document).querySelectorAll(s));

  // ---- Page view: render mermaid diagrams if present ----
  const pageContent = $('#page-content');
  if (pageContent && window.mermaid) {
    mermaid.run({querySelector: '#page-content pre.mermaid'});
  }

  // ---- Recent pages sidebar (client-side, localStorage) ----
  // Hidden pages (/hidden/<slug>) are excluded — they're not real /page/
  // routes, and the sidebar list always links to /page/.
  const recentList = $('#recent-list');
  if (recentList) {
    try {
      const slug = document.body.dataset.slug || '';
      const isHidden = document.body.dataset.routePrefix === '/hidden';
      const titleEl = $('.page-title');
      if (slug && titleEl && !isHidden) {
        const title = titleEl.dataset.title || titleEl.textContent.trim() || slug;
        let recent = JSON.parse(localStorage.getItem('hmd-recent') || '[]');
        recent = recent.filter(e => e.slug !== slug);
        recent.unshift({slug, title});
        recent = recent.slice(0, 8);
        localStorage.setItem('hmd-recent', JSON.stringify(recent));
      }
      const recent = JSON.parse(localStorage.getItem('hmd-recent') || '[]');
      recentList.innerHTML = recent.map(e =>
        `<li><a href="/page/${e.slug}"${e.slug === slug ? ' class="active"' : ''}>${escapeHtml(e.title)}</a></li>`
      ).join('');
    } catch (e) { /* ignore */ }
  }

  // ---- TOC rail (page view, ≥ 1200px via CSS) ----
  const tocList = $('#toc-list');
  const tocRail = $('#toc-rail');
  if (tocList && tocRail && pageContent) {
    const headings = $$('h2, h3', pageContent);
    if (headings.length > 1) {
      tocRail.classList.add('has-headings');
      tocList.innerHTML = headings.map((h, i) => {
        if (!h.id) {
          h.id = 'h-' + i + '-' + (h.textContent.toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-|-$/g,'') || 's');
        }
        const cls = h.tagName === 'H3' ? 'h3' : '';
        return `<a href="#${h.id}" class="${cls}">${h.textContent}</a>`;
      }).join('');
      const links = $$('a', tocList);
      const observer = new IntersectionObserver(entries => {
        entries.forEach(en => {
          if (en.isIntersecting) {
            links.forEach(a => a.classList.remove('active'));
            const active = links.find(a => a.getAttribute('href') === '#' + en.target.id);
            if (active) active.classList.add('active');
          }
        });
      }, {rootMargin: '0px 0px -70% 0px'});
      headings.forEach(h => observer.observe(h));

      // Click handler: scrollIntoView on the heading, not default anchor
      // jump — main is the scroll container (overflow-y: auto in the grid),
      // so default #hash scrolling targets the document, not main.
      links.forEach(a => {
        a.addEventListener('click', e => {
          const id = a.getAttribute('href').slice(1);
          const target = document.getElementById(id);
          if (target) {
            e.preventDefault();
            target.scrollIntoView({ behavior: 'smooth', block: 'start' });
          }
        });
      });
    }
  }

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
    let lastSuccessUnix = parseInt(syncSeg.dataset.lastSuccess || '0');
    let syncAgeTimer;

    function relativeAge(unixSeconds) {
      const now = Math.floor(Date.now() / 1000);
      const diff = now - unixSeconds;
      if (diff < 60) return 'now';
      if (diff < 3600) return Math.floor(diff / 60) + 's ago';
      if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
      return Math.floor(diff / 86400) + 'd ago';
    }

    function updateSyncAge() {
      if (lastSuccessUnix > 0) {
        const arrow = bidi ? '⇣⇡' : '⇡';
        const state = syncSeg.dataset.state || 'unknown';
        if (state === 'no remote') {
          syncSeg.textContent = 'local only';
        } else {
          const age = relativeAge(lastSuccessUnix);
          syncSeg.textContent = arrow + ' ' + state + ' · ' + age;
        }
      }
    }

    function updateSyncSeg(syncData) {
      lastSuccessUnix = syncData.last_success_unix || 0;
      syncSeg.setAttribute('data-last-success', lastSuccessUnix);
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
          if (parent) parent.replaceChild(newContent, pageContent);
          if (window.mermaid) {
            const mermaids = newContent.querySelectorAll('pre code.language-mermaid');
            if (mermaids.length > 0) window.mermaid.run({ nodes: mermaids });
          }
        })
        .catch(() => {});
    }

    function showRemoteChangedBanner() {
      const banner = $('#remote-changed-banner');
      if (banner) banner.style.display = 'block';
    }

    function pollSync() {
      fetch('/api/sync').then(r => r.json()).then(s => {
        updateSyncSeg(s);
        if (s.pagesChanged && s.pagesChanged.length > 0 && pageContent) {
          const slug = document.body.dataset.slug || '';
          const routePrefix = document.body.dataset.routePrefix || '/page';
          const prefix = routePrefix === '/hidden' ? '.' : '';
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
    syncAgeTimer = setInterval(updateSyncAge, 1000);
  }

  // ---- "+ new" button in statusline ----
  const newBtn = $('#new-btn');
  if (newBtn) {
    newBtn.addEventListener('click', () => openPalette(true));
  }

  // ---- Mobile sidebar drawer ----
  const sidebarToggle = $('#sidebar-toggle');
  const sidebar = $('#sidebar');
  if (sidebarToggle && sidebar) {
    sidebarToggle.addEventListener('click', () => sidebar.classList.toggle('open'));
  }
  const mobileSearch = $('#mobile-search');
  if (mobileSearch) {
    mobileSearch.addEventListener('click', () => {
      if (sidebar) sidebar.classList.remove('open');
      openPalette();
    });
  }
  const newDocBtn = $('#new-doc-btn');
  if (newDocBtn) {
    newDocBtn.addEventListener('click', () => openPalette(true));
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
  const paletteCount = $('#palette-count');

  let paletteOpen = false;
  let paletteRows = [];
  let paletteSelected = 0;
  let paletteCreateMode = false;
  let searchTimer;

  function openPalette(createMode) {
    if (!paletteBackdrop) return;
    paletteOpen = true;
    paletteCreateMode = !!createMode;
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
    paletteBackdrop.classList.remove('open');
  }

  function showRecent() {
    if (!paletteResults) return;
    let recent = [];
    try { recent = JSON.parse(localStorage.getItem('hmd-recent') || '[]'); } catch (e) {}
    recent = recent.slice(0, 8);
    paletteRows = recent.map(e => ({
      slug: e.slug, title: e.title, snippet: '', tags: [], create: false
    }));
    paletteSelected = 0;
    renderPaletteRows('');
  }

  // :hidden: and settings are generic navigation shortcuts — out of place
  // when the palette was opened specifically to create a new document.
  function builtinCount() {
    return paletteCreateMode ? 0 : 2;
  }

  function renderPaletteRows(query) {
    if (!paletteResults) return;
    if (paletteRows.length === 0 && !query) {
      const emptyMsg = paletteCreateMode ? 'Type a title for the new document…' : 'No recent pages';
      paletteResults.innerHTML = '<div class="palette-row" style="color:var(--fg-faint)">' + emptyMsg + '</div>';
      paletteCount.textContent = '';
      return;
    }
    const rowsHtml = paletteRows.map((r, i) => {
      const tags = r.tags && r.tags.length ? ' <span class="hit-tags">#' + r.tags.map(escapeHtml).join(' #') + '</span>' : '';
      // r.snippet comes from bleve's "html" highlighter, which already HTML-escapes
      // the surrounding text and only adds trusted <mark> tags around matches.
      return `<a href="/page/${r.slug}" class="palette-row${i === paletteSelected ? ' selected' : ''}">
        <span class="filetype">md</span>
        <span class="title">${escapeHtml(r.title)}</span>
        <span class="snippet">${r.snippet || ''}</span>${tags}
      </a>`;
    }).join('');
    const builtinHtml = paletteCreateMode ? '' :
      `<a href="/hidden" class="palette-row palette-builtin${paletteSelected === paletteRows.length ? ' selected' : ''}"><span class="filetype">hid</span><span class="title">:hidden:</span></a>` +
      `<a href="/settings" class="palette-row palette-settings${paletteSelected === paletteRows.length + 1 ? ' selected' : ''}"><span class="filetype">cfg</span><span class="title">settings</span></a>`;
    const createRow = query
      ? `<a href="/page/${slugifyQuery(query)}/edit" class="palette-row palette-create${paletteSelected === paletteRows.length + builtinCount() ? ' selected' : ''}">+ create page "${escapeHtml(query)}"</a>`
      : '';
    paletteResults.innerHTML = rowsHtml + builtinHtml + createRow;
    paletteCount.textContent = paletteRows.length > 0 ? paletteRows.length + (query ? ' matches' : ' recent') : '';
  }

  function slugifyQuery(q) {
    return q.toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  }

  function escapeHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  function searchPalette(q) {
    if (!q) { showRecent(); return; }
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      fetch('/api/search?q=' + encodeURIComponent(q))
        .then(r => r.json())
        .then(hits => {
          paletteRows = hits.map(h => ({
            slug: h.slug, title: h.title, snippet: h.snippet, tags: h.tags || [], create: false
          }));
          paletteSelected = 0;
          renderPaletteRows(q);
        })
        .catch(() => {});
    }, 150);
  }

  function paletteNavigate(dir) {
    const max = paletteRows.length + builtinCount() + (paletteInput.value ? 1 : 0);
    if (max === 0) return;
    paletteSelected = (paletteSelected + dir + max) % max;
    renderPaletteRows(paletteInput.value);
    const sel = $('.palette-row.selected', paletteResults);
    if (sel) sel.scrollIntoView({block: 'nearest'});
  }

  function paletteOpenSelected(editMode) {
    const isHidden = !paletteCreateMode && paletteSelected === paletteRows.length;
    const isSettings = !paletteCreateMode && paletteSelected === paletteRows.length + 1;
    const isCreate = paletteSelected === paletteRows.length + builtinCount() && paletteInput.value;
    if (isCreate) {
      const slug = slugifyQuery(paletteInput.value);
      window.location.href = '/page/' + slug + (editMode ? '/edit' : '/edit');
      return;
    }
    if (isHidden) {
      window.location.href = '/hidden';
      return;
    }
    if (isSettings) {
      window.location.href = '/settings';
      return;
    }
    const row = paletteRows[paletteSelected];
    if (row) window.location.href = '/page/' + row.slug + (editMode ? '/edit' : '');
  }

  if (paletteBackdrop) {
    paletteBackdrop.addEventListener('click', e => {
      if (e.target === paletteBackdrop) closePalette();
    });
  }
  if (paletteInput) {
    paletteInput.addEventListener('input', () => searchPalette(paletteInput.value));
    paletteInput.addEventListener('keydown', e => {
      if (e.key === 'Escape') { e.preventDefault(); closePalette(); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); paletteNavigate(1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); paletteNavigate(-1); }
      else if (e.key === 'Enter') {
        e.preventDefault();
        paletteOpenSelected(e.ctrlKey || e.metaKey);
      }
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
      const slug = document.body.dataset.slug;
      if (slug) { e.preventDefault(); window.location.href = '/page/' + slug + '/edit'; }
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

    function markDirty() {
      dirty = true;
      if (dirtyMark) dirtyMark.style.display = 'inline';
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

    // Create editor — zenState defined before listener references it
    const editor = new HMD.EditorView({
      doc: textarea.value,
      extensions: [
        HMD.basicSetup,
        HMD.markdown(),
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

    // Preview scheduling with error handling and loading indicator
    function schedulePreview() {
      clearTimeout(previewTimeout);
      previewTimeout = setTimeout(() => {
        preview.classList.add('preview-loading');
        preview.classList.remove('preview-error');
        fetch('/api/preview', {
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
      editForm.addEventListener('submit', () => {
        if (zenState.manuscript) toggleZen('manuscript');
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

    const toolbarActions = {
      bold: () => wrapSelection('**', '**'),
      italic: () => wrapSelection('_', '_'),
      header: () => insertHeaderLevel(1),
      link: insertLink,
      code: () => wrapSelection('`', '`'),
      table: insertTable,
      image: insertImagePlaceholder,
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

    // Handle paste and drop for image uploads
    function escapeAltText(text) {
      return text.replace(/[\[\]()]/g, '\\$&');
    }

    function handleImageFiles(files) {
      for (let file of files) {
        if (!file.type.startsWith('image/')) continue;

        const formData = new FormData();
        formData.append('file', file);

        fetch(`/api/attachments/${cmHost.dataset.slug}`, {
          method: 'POST',
          body: formData
        })
        .then(r => {
          if (!r.ok) throw new Error('Upload failed');
          return r.json();
        })
        .then(data => {
          const alt = escapeAltText(file.name);
          const markdown = `![${alt}](${data.url})\n`;
          editor.dispatch({
            changes: {
              from: editor.state.selection.main.head,
              insert: markdown
            }
          });
        })
        .catch(err => {
          alert('Failed to upload image: ' + file.name);
        });
      }
    }

    cmHost.addEventListener('paste', e => {
      if (e.clipboardData.files && e.clipboardData.files.length) {
        const hasImages = Array.from(e.clipboardData.files).some(f => f.type.startsWith('image/'));
        if (hasImages) {
          e.preventDefault();
          handleImageFiles(e.clipboardData.files);
        }
      }
    });

    cmHost.addEventListener('drop', e => {
      if (e.dataTransfer.files && e.dataTransfer.files.length) {
        const hasImages = Array.from(e.dataTransfer.files).some(f => f.type.startsWith('image/'));
        if (hasImages) {
          e.preventDefault();
          handleImageFiles(e.dataTransfer.files);
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
})();
