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

  function setActiveNav() {
    var page = currentPage();

    document.querySelectorAll('[data-page]').forEach(function (link) {
      if (link.dataset.page === page) {
        link.setAttribute('aria-current', 'page');
      } else {
        link.removeAttribute('aria-current');
      }
    });
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
    initCopyButtons();
  });
})();
