(function () {
  var fallbackBrand = 'juhe-ai';
  var nameTargets = Array.prototype.slice.call(document.querySelectorAll('[data-brand-name]'));
  var searchInput = document.querySelector('[data-help-search]');
  var liveRegion = document.querySelector('[data-search-status]');
  var sections = Array.prototype.slice.call(document.querySelectorAll('.section[id]'));
  var links = Array.prototype.slice.call(document.querySelectorAll('[data-nav-link][href^="#"]'));

  function setBrand(name) {
    var safeName = typeof name === 'string' && name.trim() ? name.trim() : fallbackBrand;
    nameTargets.forEach(function (target) { target.textContent = safeName + ' 使用手册'; });
  }

  function getBrandName(payload) {
    var data = payload && (payload.data || payload);
    return data && (data.brandName || data.appName || data.systemName || data.name);
  }

  setBrand(fallbackBrand);
  fetch('/__aisys__/api/settings/public', { credentials: 'include' })
    .then(function (response) { return response.ok ? response.json() : null; })
    .then(function (payload) { setBrand(getBrandName(payload)); })
    .catch(function () { setBrand(fallbackBrand); });

  function setActive(id) {
    links.forEach(function (link) {
      var active = link.getAttribute('href') === '#' + id;
      link.classList.toggle('active', active);
      if (active) link.setAttribute('aria-current', 'true');
      else link.removeAttribute('aria-current');
    });
  }

  if ('IntersectionObserver' in window && sections.length) {
    var observer = new IntersectionObserver(function (entries) {
      var visible = entries.filter(function (entry) { return entry.isIntersecting; })
        .sort(function (a, b) { return b.intersectionRatio - a.intersectionRatio; })[0];
      if (visible) setActive(visible.target.id);
    }, { rootMargin: '-16% 0px -72% 0px', threshold: [0.1, 0.3, 0.6] });
    sections.forEach(function (section) { observer.observe(section); });
  }

  function applySearch() {
    if (!searchInput) return;
    var query = searchInput.value.trim().toLocaleLowerCase();
    var count = 0;
    sections.forEach(function (section) {
      var matched = !query || section.textContent.toLocaleLowerCase().indexOf(query) !== -1;
      section.classList.toggle('search-result', true);
      section.hidden = !matched;
      if (matched) count += 1;
    });
    if (liveRegion) liveRegion.textContent = query ? '找到 ' + count + ' 个章节。' : '';
  }

  if (searchInput) {
    searchInput.addEventListener('input', applySearch);
    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && document.activeElement === searchInput) {
        searchInput.value = '';
        applySearch();
        searchInput.blur();
      }
    });
  }
})();
