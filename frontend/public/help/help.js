// juhe-ai 在线帮助文档站：manifest 篇目导航 + rendered 预渲染 HTML 加载 + Markdown 原文下载。
// 内容管线：docs/**/*.md（唯一人工维护层）→ scripts/render-help-docs.mjs → rendered/**/*.html。
// 本文件零依赖；帮助面不引入任何外部域脚本。
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

  // 角色分流门控页（/__aisys__/help/）：按登录身份跳转对应受众文档站。
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
    return;
  }

  var audience = document.body.getAttribute('data-audience') === 'admin' ? 'admin' : 'user';
  var searchInput = document.querySelector('[data-help-search]');
  var liveRegion = document.querySelector('[data-search-status]');
  var navRoot = document.querySelector('[data-doc-nav]');
  var titleNode = document.querySelector('[data-doc-title]');
  var summaryNode = document.querySelector('[data-doc-summary]');
  var bodyNode = document.querySelector('[data-doc-body]');
  var downloadLink = document.querySelector('[data-download-link]');
  var pagerRoot = document.querySelector('[data-doc-pager]');
  var updatedAtNode = document.querySelector('[data-updated-at]');
  var docs = [];
  var currentId = null;

  function docById(id) {
    for (var i = 0; i < docs.length; i += 1) {
      if (docs[i].id === id) return { doc: docs[i], index: i };
    }
    return null;
  }

  function renderNav(filter) {
    if (!navRoot) return;
    var query = (filter || '').trim().toLocaleLowerCase();
    navRoot.replaceChildren();
    var shown = 0;
    docs.forEach(function (doc, index) {
      var haystack = (doc.title + ' ' + (doc.summary || '')).toLocaleLowerCase();
      if (query && haystack.indexOf(query) === -1) return;
      shown += 1;
      var link = document.createElement('a');
      link.href = '#/doc/' + encodeURIComponent(doc.id);
      link.textContent = (index + 1) + '. ' + doc.title;
      link.setAttribute('data-doc-link', doc.id);
      if (doc.id === currentId) {
        link.classList.add('active');
        link.setAttribute('aria-current', 'page');
      }
      navRoot.appendChild(link);
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
    var previous = docs[index - 1];
    var next = docs[index + 1];
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
    var found = docById(id) || docById(docs.length ? docs[0].id : '');
    if (!found) return;
    var doc = found.doc;
    currentId = doc.id;
    window.history.replaceState(null, '', '#/doc/' + encodeURIComponent(doc.id));

    renderNav(searchInput ? searchInput.value : '');
    renderPager(found.index);
    docs.forEach(function (item) {
      var link = navRoot && navRoot.querySelector('[data-doc-link="' + item.id + '"]');
      if (!link) return;
      var active = item.id === currentId;
      link.classList.toggle('active', active);
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    if (titleNode) titleNode.textContent = doc.title;
    if (summaryNode) summaryNode.textContent = doc.summary || '';
    if (downloadLink) {
      downloadLink.href = '../docs/' + doc.file;
      downloadLink.setAttribute('download', doc.file.split('/').pop());
    }
    if (bodyNode) {
      bodyNode.setAttribute('aria-busy', 'true');
      bodyNode.textContent = '正在加载…';
      var request = new XMLHttpRequest();
      request.open('GET', '../rendered/' + doc.file.replace(/\.md$/, '.html'), true);
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
      var match = docs.filter(function (doc) {
        return (doc.title + ' ' + (doc.summary || '')).toLocaleLowerCase().indexOf(query) !== -1;
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

  fetch('../manifest.json')
    .then(function (response) {
      if (!response.ok) throw new Error('manifest 加载失败');
      return response.json();
    })
    .then(function (manifest) {
      var section = manifest.audiences[audience];
      if (!section) throw new Error('manifest 缺少受众: ' + audience);
      docs = section.docs;
      if (updatedAtNode) updatedAtNode.textContent = '更新于 ' + manifest.updatedAt;
      var brandTitle = document.querySelector('[data-audience-title]');
      if (brandTitle) brandTitle.textContent = section.title;
      loadDoc(currentHashId() || (docs[0] && docs[0].id));
    })
    .catch(function () {
      if (bodyNode) bodyNode.textContent = '帮助目录加载失败，请刷新重试；若持续失败请联系管理员。';
    });
})();
