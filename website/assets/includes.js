(function () {
  function currentPage() {
    var bodyPage = document.body && document.body.dataset.page;
    if (bodyPage) {
      return bodyPage;
    }

    var filename = window.location.pathname.split('/').pop();

    if (!filename || filename === 'index.html') {
      return 'home';
    }

    return filename.replace(/\.html$/, '');
  }

  function isDocsPage(page) {
    if (page && page !== 'home') {
      return true;
    }

    return window.location.pathname.indexOf('/documentation/') !== -1;
  }

  function setActiveNav() {
    var page = currentPage();
    var onDocsPage = isDocsPage(page);
    var navDropdown = document.querySelector('.nav-dropdown');

    document.querySelectorAll('.nav-menu a[data-page]').forEach(function (link) {
      if (link.dataset.page === page) {
        link.setAttribute('aria-current', 'page');
      } else {
        link.removeAttribute('aria-current');
      }
    });

    if (navDropdown) {
      navDropdown.classList.toggle('is-active', onDocsPage);
    }

    var docSummary = document.querySelector('.nav-dropdown summary.nav-link');
    if (docSummary) {
      if (onDocsPage) {
        docSummary.setAttribute('aria-current', 'page');
      } else {
        docSummary.removeAttribute('aria-current');
      }
    }
  }

  function initToc() {
    var tocLinks = document.querySelectorAll('.docs-toc-nav a[href^="#"]');
    if (!tocLinks.length) {
      return;
    }

    var sections = Array.prototype.map.call(tocLinks, function (link) {
      var id = link.getAttribute('href').slice(1);
      return document.getElementById(id);
    }).filter(Boolean);

    function setActiveLink(id) {
      tocLinks.forEach(function (link) {
        if (link.getAttribute('href') === '#' + id) {
          link.classList.add('is-active');
        } else {
          link.classList.remove('is-active');
        }
      });
    }

    if ('IntersectionObserver' in window) {
      var observer = new IntersectionObserver(
        function (entries) {
          var visible = entries
            .filter(function (entry) {
              return entry.isIntersecting;
            })
            .sort(function (a, b) {
              return a.target.offsetTop - b.target.offsetTop;
            });

          if (visible.length) {
            setActiveLink(visible[0].target.id);
          }
        },
        {
          rootMargin: '-20% 0px -70% 0px',
          threshold: 0,
        }
      );

      sections.forEach(function (section) {
        observer.observe(section);
      });
    }

    tocLinks.forEach(function (link) {
      link.addEventListener('click', function (event) {
        var id = link.getAttribute('href').slice(1);
        var target = document.getElementById(id);
        if (target) {
          event.preventDefault();
          target.scrollIntoView({ behavior: 'smooth', block: 'start' });
          setActiveLink(id);
          history.replaceState(null, '', '#' + id);
        }
      });
    });
  }

  var copyIconSvg =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';

  function initCodeBlockCopyButtons() {
    document.querySelectorAll('pre > code').forEach(function (code) {
      var pre = code.parentElement;
      if (!pre || pre.dataset.copyWrap === 'true' || pre.closest('.cmd') || pre.closest('.code-block')) {
        return;
      }

      var text = code.textContent;
      if (!text || !text.trim()) {
        return;
      }

      pre.dataset.copyWrap = 'true';

      var wrapper = document.createElement('div');
      wrapper.className = 'code-block';
      pre.parentNode.insertBefore(wrapper, pre);
      wrapper.appendChild(pre);

      var btn = document.createElement('button');
      btn.className = 'copy-btn code-block-copy-btn';
      btn.type = 'button';
      btn.dataset.command = text;
      btn.setAttribute('aria-label', 'Copy code block');
      btn.innerHTML = copyIconSvg + '<span>Copy</span>';
      wrapper.appendChild(btn);
    });
  }

  function initCopyButtons() {
    document.querySelectorAll('.copy-btn').forEach(function (btn) {
      if (btn.dataset.copyInit === 'true') {
        return;
      }

      btn.dataset.copyInit = 'true';
      var label = btn.querySelector('span');
      var resetTimer;

      btn.addEventListener('click', function () {
        navigator.clipboard.writeText(btn.dataset.command).then(function () {
          btn.classList.add('copied');
          label.textContent = 'Copied';
          clearTimeout(resetTimer);
          resetTimer = setTimeout(function () {
            btn.classList.remove('copied');
            label.textContent = 'Copy';
          }, 1800);
        });
      });
    });
  }

  function loadInclude(target) {
    var src = target.dataset.include;

    return fetch(src)
      .then(function (response) {
        if (!response.ok) {
          throw new Error('Unable to load include: ' + src);
        }

        return response.text();
      })
      .then(function (html) {
        target.outerHTML = html;
      });
  }

  function loadAllIncludes() {
    var targets = document.querySelectorAll('[data-include]');
    if (!targets.length) {
      return Promise.resolve();
    }

    return Promise.all(
      Array.prototype.map.call(targets, loadInclude)
    ).then(loadAllIncludes);
  }

  loadAllIncludes().then(function () {
    setActiveNav();
    initToc();
    initCodeBlockCopyButtons();
    initCopyButtons();
  });
})();
