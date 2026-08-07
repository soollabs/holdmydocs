// Every page nav is a full page load (server-rendered <a> links, no SPA
// routing), so the sidebar's scroll position resets to the top on every
// click — even though the tree only auto-expands the ancestors of the page
// you're on (see writeLiveTreeNodes in namespace.go). Scroll the current
// item back into view so you land where you navigated to, not the top.
(function() {
  var current = document.querySelector('.sidebar .current, .sidebar .active');
  if (current) current.scrollIntoView({ block: 'center' });
})();
