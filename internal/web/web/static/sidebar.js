// Public and static views do not load app.js, so their mobile navigation
// needs its own controls. The authenticated drawer remains owned by app.js.
(function() {
  var sidebar = document.getElementById('sidebar');
  var toggle = document.getElementById('public-sidebar-toggle');
  if (sidebar && toggle) {
    var mobile = window.matchMedia('(max-width: 899px)');
    toggle.hidden = false;
    function setOpen(open) {
      sidebar.classList.toggle('open', open);
      sidebar.inert = mobile.matches && !open;
      toggle.setAttribute('aria-expanded', String(open));
    }
    toggle.addEventListener('click', function() {
      setOpen(!sidebar.classList.contains('open'));
    });
    document.addEventListener('keydown', function(event) {
      if (event.key === 'Escape' && sidebar.classList.contains('open')) {
        setOpen(false);
        toggle.focus();
      }
    });
    document.addEventListener('click', function(event) {
      if (!sidebar.contains(event.target) && !toggle.contains(event.target)) setOpen(false);
    });
    mobile.addEventListener('change', function() { setOpen(false); });
    setOpen(false);
  }

  // Keep the current page in view after a full-page navigation, without
  // scrolling the document itself (notably when opening a deep heading link).
  var current = document.querySelector('.sidebar .current, .sidebar .active');
  if (sidebar && current) {
    sidebar.scrollTop = current.getBoundingClientRect().top - sidebar.getBoundingClientRect().top
      + sidebar.scrollTop - sidebar.clientHeight / 2;
  }
})();
