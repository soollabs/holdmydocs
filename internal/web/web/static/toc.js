// TOC rail (page view, >= 1200px via CSS; inline disclosure 900-1199px).
// Standalone so it can load for anonymous public-namespace views too: it
// only reads the DOM and makes no authenticated fetches, while the rest of app.js
// (auth-only chrome) stays behind login.
(function() {
  const $ = (s, p) => (p || document).querySelector(s);
  const $$ = (s, p) => Array.from((p || document).querySelectorAll(s));

  function escapeHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  const pageContent = $('#page-content');
  const tocList = $('#toc-list');
  const tocRail = $('#toc-rail');
  const tocInline = $('#toc-inline');
  const tocListInline = $('#toc-list-inline');
  if (tocInline && window.HMDComponents) window.HMDComponents.enhanceDisclosure(tocInline);
  if (!(tocList && tocRail && pageContent)) return;

  const headings = $$('h2, h3', pageContent);
  if (headings.length === 0) return;

  tocRail.classList.add('has-headings');
  if (tocInline) tocInline.classList.add('has-headings');
  const tocHtml = headings.map((h, i) => {
    if (!h.id) {
      h.id = 'h-' + i + '-' + (h.textContent.toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-|-$/g,'') || 's');
    }
    const cls = h.tagName === 'H3' ? 'h3' : '';
    return `<a href="#${h.id}" class="${cls}">${escapeHtml(h.textContent)}</a>`;
  }).join('');
  tocList.innerHTML = tocHtml;
  if (tocListInline) tocListInline.innerHTML = tocHtml;
  const links = $$('a', tocList).concat(tocListInline ? $$('a', tocListInline) : []);
  // Scroll-spy. An observer only reports headings crossing a band near the
  // top, so the last, short sections never reach it and the outline stalls
  // part-way. Instead mark the last heading above a reading line, and the
  // final heading once the page is scrolled to its end.
  function scrollParent(el) {
    for (let p = el.parentElement; p; p = p.parentElement) {
      const o = getComputedStyle(p).overflowY;
      if (o === 'auto' || o === 'scroll') return p;
    }
    return null;
  }
  const scrollParentEl = scrollParent(pageContent);
  let locked = '';
  function setActive(id) {
    links.forEach(a => a.classList.toggle('active', a.getAttribute('href') === '#' + id));
  }
  function update() {
    // The container scrolls only while its content overflows; otherwise the window does.
    const scroller = scrollParentEl && scrollParentEl.scrollHeight > scrollParentEl.clientHeight ? scrollParentEl : null;
    if (locked) { setActive(locked); return; }
    const top = scroller ? scroller.getBoundingClientRect().top : 0;
    const height = scroller ? scroller.clientHeight : window.innerHeight;
    const line = top + Math.min(120, height * 0.25);
    let current = null;
    for (const h of headings) {
      if (h.getBoundingClientRect().top <= line) current = h; else break;
    }
    const end = scroller
      ? scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2
      : window.scrollY + window.innerHeight >= document.documentElement.scrollHeight - 2;
    if (end) current = headings[headings.length - 1];
    setActive(current ? current.id : '');
  }
  let queued = false;
  function schedule() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; update(); });
  }
  // Scroll events do not bubble; capture catches the container and the window.
  document.addEventListener('scroll', schedule, { passive: true, capture: true });
  window.addEventListener('resize', schedule);
  // A clicked outline entry stays active until the reader scrolls on their own:
  // a heading too close to the end cannot reach the reading line.
  ['wheel', 'touchmove', 'keydown'].forEach(ev => window.addEventListener(ev, () => { locked = ''; }, { passive: true }));
  update();

  // Click handler: scrollIntoView on the heading, not default anchor
  // jump, because main is the scroll container (overflow-y: auto in the grid),
  // so default #hash scrolling targets the document, not main.
  links.forEach(a => {
    a.addEventListener('click', e => {
      const id = a.getAttribute('href').slice(1);
      const target = document.getElementById(id);
      if (target) {
        e.preventDefault();
        target.scrollIntoView({ behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start' });
        history.replaceState(null, '', '#' + id);
        locked = id;
        setActive(id);
      }
    });
  });

  // IDs are added above, after the browser's initial fragment lookup.
  // Honour links copied from the outline when opening a page directly.
  function followFragment() {
    try {
      const target = document.getElementById(decodeURIComponent(location.hash.slice(1)));
      if (target && pageContent.contains(target)) target.scrollIntoView({ block: 'start' });
    } catch (_) { /* Ignore malformed URL fragments. */ }
  }
  followFragment();
  window.addEventListener('hashchange', followFragment);
})();
