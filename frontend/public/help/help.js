(function () {
  var sourceNav = document.querySelector('[data-nav]');
  var mobileNavTarget = document.querySelector('[data-nav-mobile]');
  if (sourceNav && mobileNavTarget) mobileNavTarget.innerHTML = sourceNav.innerHTML;

  var searchInput = document.querySelector('[data-help-search]');
  var liveRegion = document.querySelector('[data-search-status]');
  var sections = Array.prototype.slice.call(document.querySelectorAll('.section[id]'));
  var links = Array.prototype.slice.call(document.querySelectorAll('[data-nav-link][href^="#"]'));
  var brandImages = Array.prototype.slice.call(document.querySelectorAll('.brand-icon'));
  var brandFallbacks = Array.prototype.slice.call(document.querySelectorAll('.brand-badge'));

  function showBrandImage(image) {
    image.hidden = false;
    brandFallbacks.forEach(function (fallback) { fallback.hidden = true; });
  }

  function showBrandFallback(image) {
    image.hidden = true;
    brandFallbacks.forEach(function (fallback) { fallback.hidden = false; });
  }

  brandImages.forEach(function (image) {
    image.addEventListener('load', function () {
      showBrandImage(image);
    });
    image.addEventListener('error', function () {
      showBrandFallback(image);
    });
    if (image.complete) {
      if (image.naturalWidth > 0) showBrandImage(image);
      else showBrandFallback(image);
    }
  });

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

  var searchResults;

  function getSearchMatches(query) {
    var terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    return sections.filter(function (section) {
      var content = section.textContent.toLocaleLowerCase();
      return terms.every(function (term) { return content.indexOf(term) !== -1; });
    });
  }

  function getSectionLabel(section) {
    var heading = section.querySelector('h2');
    return heading ? heading.textContent.trim() : section.id;
  }

  function locateSection(section) {
    window.history.replaceState(null, '', '#' + section.id);
    section.scrollIntoView({ behavior: 'smooth', block: 'start' });
    setActive(section.id);
    searchInput.value = '';
    searchResults.hidden = true;
    if (liveRegion) liveRegion.textContent = '已定位到“' + getSectionLabel(section) + '”。';
    searchInput.blur();
  }

  function renderSearchResults(matches, query) {
    searchResults.replaceChildren();
    searchResults.hidden = !query || !matches.length;
    matches.slice(0, 8).forEach(function (section) {
      var result = document.createElement('button');
      result.type = 'button';
      result.textContent = getSectionLabel(section);
      result.addEventListener('click', function () { locateSection(section); });
      searchResults.appendChild(result);
    });
    if (liveRegion) {
      liveRegion.textContent = query
        ? matches.length ? '找到 ' + matches.length + ' 个章节，按 Enter 定位第一个结果。' : '未找到匹配章节。'
        : '';
    }
  }

  function applySearch() {
    if (!searchInput) return [];
    var query = searchInput.value.trim();
    var matches = getSearchMatches(query);
    renderSearchResults(matches, query);
    return matches;
  }

  if (searchInput) {
    searchResults = document.createElement('div');
    searchResults.className = 'search-results';
    searchResults.hidden = true;
    searchInput.parentElement.appendChild(searchResults);
    searchInput.addEventListener('input', applySearch);
    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && document.activeElement === searchInput) {
        searchInput.value = '';
        applySearch();
        searchInput.blur();
      }
      if (event.key === 'Enter' && document.activeElement === searchInput) {
        var matches = applySearch();
        if (matches.length) {
          event.preventDefault();
          locateSection(matches[0]);
        }
      }
    });
  }

  var mobileNav = document.querySelector('.mobile-nav');
  if (mobileNav) {
    links.forEach(function (link) {
      link.addEventListener('click', function () {
        if (window.matchMedia('(max-width: 900px)').matches) mobileNav.removeAttribute('open');
      });
    });
  }

  if (document.body.classList.contains('help-gate')) {
    fetch('/__aisys__/api/auth/me', { credentials: 'include' })
      .then(function (response) {
        if (!response.ok) throw new Error('未登录');
        return response.json();
      })
      .then(function (payload) {
        var role = payload && payload.data && payload.data.role;
        window.location.assign(role === 'admin' || role === 'super_admin' ? '/__aisys__/help/admin/' : '/__aisys__/help/user/');
      })
      .catch(function () {
        window.location.assign('/__aisys__/login?redirect=' + encodeURIComponent('/__aisys__/help/'));
      });
  }
})();
