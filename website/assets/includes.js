(function () {
  function currentPage() {
    var filename = window.location.pathname.split('/').pop();

    if (!filename || filename === 'index.html') {
      return 'home';
    }

    if (filename === 'quickstart.html') {
      return 'quickstart';
    }

    return '';
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

  Promise.all(
    Array.prototype.map.call(document.querySelectorAll('[data-include]'), loadInclude)
  ).then(setActiveNav);
})();
