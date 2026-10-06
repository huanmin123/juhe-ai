// juhe-ai 在线帮助《使用手册》：manifest 篇目导航 + rendered 预渲染 HTML 加载 + Markdown 原文下载。
// 内容管线：docs/**/*.md（唯一人工维护层）→ scripts/render-help-docs.mjs → rendered/**/*.html。
// 本文件零依赖；帮助面不引入任何外部域脚本。单一手册、双分区（接入与调用 / 管理运维），无角色门槛。
(function () {
  'use strict';

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
    image.addEventListener('load', function () { showBrandImage(image); });
    image.addEventListener('error', function () { showBrandFallback(image); });
    if (image.complete) {
      if (image.naturalWidth > 0) showBrandImage(image);
      else showBrandFallback(image);
    }
  });

  // 角色分流门控页（/__aisys__/help/）：单一手册，确认登录态后直接进入。
  if (document.body.classList.contains('help-gate')) {
    fetch('/__aisys__/api/auth/me', { credentials: 'include' })
      .then(function (response) {
        if (!response.ok) throw new Error('未登录');
        window.location.assign('/__aisys__/help/user/');
      })
      .catch(function () {
        window.location.assign('/__aisys__/login?redirect=' + encodeURIComponent('/__aisys__/help/'));
      });
    return;
  }

  var searchInput = document.querySelector('[data-help-search]');
  var liveRegion = document.querySelector('[data-search-status]');
  var navRoot = document.querySelector('[data-doc-nav]');
  var titleNode = document.querySelector('[data-doc-title]');
  var summaryNode = document.querySelector('[data-doc-summary]');
  var bodyNode = document.querySelector('[data-doc-body]');
  var downloadLink = document.querySelector('[data-download-link]');
  var pagerRoot = document.querySelector('[data-doc-pager]');
  var updatedAtNode = document.querySelector('[data-updated-at]');
  var flat = [];     // 全部篇目（跨分区扁平，供定位/翻页/搜索）
  var currentId = null;

  function entryById(id) {
    for (var i = 0; i < flat.length; i += 1) {
      if (flat[i].id === id) return { entry: flat[i], index: i };
    }
    return null;
  }

  function renderNav(filter) {
    if (!navRoot) return;
    var query = (filter || '').trim().toLocaleLowerCase();
    navRoot.replaceChildren();
    var shown = 0;
    manifest_sections.forEach(function (section) {
      var groupShown = [];
      section.docs.forEach(function (entry) {
        var haystack = (entry.title + ' ' + (entry.summary || '')).toLocaleLowerCase();
        if (query && haystack.indexOf(query) === -1) return;
        groupShown.push(entry);
      });
      if (!groupShown.length) return;
      var heading = document.createElement('strong');
      heading.textContent = section.title;
      navRoot.appendChild(heading);
      groupShown.forEach(function (entry) {
        shown += 1;
        var link = document.createElement('a');
        link.href = '#/doc/' + encodeURIComponent(entry.id);
        link.textContent = entry.navLabel;
        link.setAttribute('data-doc-link', entry.id);
        if (entry.id === currentId) {
          link.classList.add('active');
          link.setAttribute('aria-current', 'page');
        }
        navRoot.appendChild(link);
      });
    });
    if (liveRegion) {
      liveRegion.textContent = query
        ? (shown ? '匹配到 ' + shown + ' 篇，按 Enter 打开第一篇。' : '没有匹配的篇目。')
        : '';
    }
  }

  function renderPager(index) {
    if (!pagerRoot) return;
    pagerRoot.replaceChildren();
    var previous = flat[index - 1];
    var next = flat[index + 1];
    [previous && { target: previous, label: '← 上一篇：' }, next && { target: next, label: '下一篇：' }]
      .filter(Boolean)
      .forEach(function (item) {
        var link = document.createElement('a');
        link.href = '#/doc/' + encodeURIComponent(item.target.id);
        link.className = item.label.indexOf('←') === 0 ? 'pager-prev' : 'pager-next';
        link.textContent = item.label + item.target.title;
        pagerRoot.appendChild(link);
      });
  }

  function loadDoc(id) {
    var found = entryById(id) || entryById(flat.length ? flat[0].id : '');
    if (!found) return;
    var entry = found.entry;
    currentId = entry.id;
    window.history.replaceState(null, '', '#/doc/' + encodeURIComponent(entry.id));

    renderNav(searchInput ? searchInput.value : '');
    renderPager(found.index);
    navRoot.querySelectorAll('[data-doc-link]').forEach(function (link) {
      var active = link.getAttribute('data-doc-link') === currentId;
      link.classList.toggle('active', active);
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    if (titleNode) titleNode.textContent = entry.title;
    if (summaryNode) summaryNode.textContent = entry.summary || '';
    if (downloadLink) {
      downloadLink.href = '../docs/' + entry.file;
      downloadLink.setAttribute('download', entry.file.split('/').pop());
    }
    if (bodyNode) {
      bodyNode.setAttribute('aria-busy', 'true');
      bodyNode.textContent = '正在加载…';
      var request = new XMLHttpRequest();
      request.open('GET', '../rendered/' + entry.file.replace(/\.md$/, '.html'), true);
      request.addEventListener('load', function () {
        if (request.status >= 200 && request.status < 400) {
          bodyNode.innerHTML = request.responseText
            .replace(/^<!--[\s\S]*?-->/, '');
          var heading = bodyNode.querySelector('h1');
          if (heading) heading.remove();
        } else {
          bodyNode.textContent = '这一篇加载失败了（HTTP ' + request.status + '）。请刷新重试；若持续失败请联系管理员。';
        }
        bodyNode.removeAttribute('aria-busy');
        window.scrollTo({ top: 0, behavior: 'auto' });
      });
      request.addEventListener('error', function () {
        bodyNode.textContent = '网络错误，加载失败。请刷新重试。';
        bodyNode.removeAttribute('aria-busy');
      });
      request.send();
    }
  }

  function currentHashId() {
    var match = /#\/doc\/([^/?#]+)/.exec(window.location.hash || '');
    return match ? decodeURIComponent(match[1]) : null;
  }

  window.addEventListener('hashchange', function () {
    var id = currentHashId();
    if (id && id !== currentId) loadDoc(id);
  });

  if (searchInput) {
    searchInput.addEventListener('input', function () { renderNav(searchInput.value); });
    searchInput.addEventListener('keydown', function (event) {
      if (event.key !== 'Enter') return;
      var query = searchInput.value.trim().toLocaleLowerCase();
      var match = flat.filter(function (entry) {
        return (entry.title + ' ' + (entry.summary || '')).toLocaleLowerCase().indexOf(query) !== -1;
      })[0];
      if (match) {
        event.preventDefault();
        loadDoc(match.id);
        searchInput.blur();
      }
    });
    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && document.activeElement === searchInput) {
        searchInput.value = '';
        renderNav('');
        searchInput.blur();
      }
    });
  }

  var manifest_sections = [];

  fetch('../manifest.json')
    .then(function (response) {
      if (!response.ok) throw new Error('manifest 加载失败');
      return response.json();
    })
    .then(function (manifest) {
      var sections = manifest.sections || [];
      var seq = 0;
      sections.forEach(function (section) {
        var view = { title: section.title, docs: [] };
        section.docs.forEach(function (doc) {
          seq += 1;
          var labelled = {};
          for (var key in doc) labelled[key] = doc[key];
          labelled.navLabel = seq + '. ' + doc.title;
          view.docs.push(labelled);
          flat.push(labelled);
        });
        manifest_sections.push(view);
      });
      if (updatedAtNode) updatedAtNode.textContent = '更新于 ' + manifest.updatedAt;
      var brandTitle = document.querySelector('[data-manual-title]');
      if (brandTitle) brandTitle.textContent = manifest.title || '使用手册';
      loadDoc(currentHashId() || (flat[0] && flat[0].id));
    })
    .catch(function () {
      if (bodyNode) bodyNode.textContent = '帮助目录加载失败，请刷新重试；若持续失败请联系管理员。';
    });
})();
