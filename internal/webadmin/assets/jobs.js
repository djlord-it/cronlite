/* Progressive enhancement only. Go/PostgreSQL remain the source of truth. */
(() => {
  'use strict';
  const form = document.getElementById('fleet-filters');
  const feedback = document.getElementById('search-feedback');
  if (!form || !window.fetch || !window.AbortController) return;
  let timer, controller, generation = 0, composing = false;

  const pending = () => {
    const results = document.getElementById('fleet-results');
    results.setAttribute('aria-busy', 'true');
    // Disallow acting on an old selection while new filters are pending.
    results.inert = true;
    results.querySelectorAll('[name="job_id"]').forEach(input => { input.checked = false; });
    feedback.textContent = 'Updating jobs…';
  };
  const cancel = () => {
    clearTimeout(timer);
    if (controller) controller.abort();
    return ++generation;
  };
  const filterURL = () => {
    const url = new URL(form.action);
    url.search = new URLSearchParams(new FormData(form)).toString();
    return url;
  };
  async function load(url, version, historyMode) {
    controller = new AbortController();
    const request = controller;
    const timeout = setTimeout(() => request.abort(), 10000);
    try {
      const response = await fetch(url, {signal: request.signal, credentials: 'same-origin', cache: 'no-store'});
      if (version !== generation) return;
      const resolved = new URL(response.url);
      if (resolved.pathname === '/admin/login') { location.assign(resolved); return; }
      if (!response.ok || resolved.origin !== location.origin || resolved.pathname !== '/admin/jobs') throw new Error('Invalid response');
      const doc = new DOMParser().parseFromString(await response.text(), 'text/html');
      if (version !== generation) return;
      const results = doc.getElementById('fleet-results');
      const counts = doc.getElementById('fleet-counts');
      const filters = doc.getElementById('fleet-filters');
      if (!results || !counts || !filters) throw new Error('Invalid page');
      const old = document.getElementById('fleet-results');
      const saved = old.querySelector('.saved-views');
      const savedName = old.querySelector('[name="view_name"]');
      if (saved && saved.open) results.querySelector('.saved-views').open = true;
      if (savedName) results.querySelector('[name="view_name"]').value = savedName.value;
      old.replaceWith(results);
      document.getElementById('fleet-counts').replaceWith(counts);
      for (const name of ['name', 'enabled', 'tag', 'view']) form.elements.namedItem(name).value = filters.elements.namedItem(name).value;
      form.querySelector('a').href = filters.querySelector('a').href;
      if (historyMode === 'push') history.pushState(null, '', resolved);
      else if (historyMode === 'replace') history.replaceState(null, '', resolved);
      feedback.textContent = results.querySelector('.pagination span')?.textContent || 'Jobs updated.';
    } catch (error) {
      if (version !== generation) return;
      document.getElementById('fleet-results').setAttribute('aria-busy', 'false');
      // Keep stale actions inert. A retry or ordinary page load restores them.
      feedback.textContent = 'Could not update jobs. Retry with Filter or reload the page.';
    } finally {
      clearTimeout(timeout);
    }
  }
  function schedule(delay = 200) {
    const version = cancel();
    pending();
    if (!form.checkValidity()) {
      feedback.textContent = 'Enter a complete tag as key=value.';
      return;
    }
    timer = setTimeout(() => load(filterURL(), version, 'replace'), delay);
  }
  const tag = form.elements.namedItem('tag');
  // Incomplete tag input should not send invalid queries or interrupt typing.
  tag.addEventListener('input', () => {
    tag.setCustomValidity(tag.value && !/^[^=]+=.+$/.test(tag.value) ? 'Enter a complete tag as key=value.' : '');
  });
  form.addEventListener('input', event => {
    if (composing || event.isComposing) return;
    schedule();
  });
  form.addEventListener('compositionstart', () => { composing = true; cancel(); pending(); });
  form.addEventListener('compositionend', () => { composing = false; schedule(); });
  form.elements.namedItem('enabled').addEventListener('change', () => schedule(0));
  form.addEventListener('submit', event => { event.preventDefault(); schedule(0); });
  document.querySelector('main').addEventListener('click', event => {
    const link = event.target.closest('a');
    if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const url = new URL(link.href);
    if (url.origin !== location.origin || url.pathname !== '/admin/jobs') return;
    event.preventDefault();
    const version = cancel();
    pending();
    tag.setCustomValidity('');
    load(url, version, 'push');
  });
  window.addEventListener('popstate', () => {
    const version = cancel();
    pending();
    tag.setCustomValidity('');
    load(new URL(location.href), version, 'none');
  });
})();
