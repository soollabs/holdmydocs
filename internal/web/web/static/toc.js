// TOC rail (page view, >= 1200px via CSS; inline disclosure 900-1199px).
// Standalone so it can load for anonymous public-namespace views too — it
// only reads the DOM, no authenticated fetches — while the rest of app.js
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
  if (!(tocList && tocRail && pageContent)) return;

  const headings = $$('h2, h3', pageContent);
  if (headings.length <= 1) return;

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
  const observer = new IntersectionObserver(entries => {
    entries.forEach(en => {
      if (en.isIntersecting) {
        links.forEach(a => a.classList.remove('active'));
        links.filter(a => a.getAttribute('href') === '#' + en.target.id)
          .forEach(a => a.classList.add('active'));
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
})();
