const state = {
  invocationFilters: { offset: 0, limit: 50, server: '', tool: '', status: '' },
  serverFilters: { offset: 0, limit: 100, enabled: null },
  selectedServerName: '',
  editingServerName: '',
  selectedInvocationID: '',
  invocationSource: null,
  lastInvocationSignature: '',
  invocationViews: { detail: 'raw', events: 'raw' },
  friendlyExpanded: { detail: false, events: false },
  connectStatusPoll: null,
};

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

$('#show-invocations').addEventListener('click', () => toggleView('invocations'));
$('#show-servers').addEventListener('click', () => toggleView('servers'));
$('#apply-filters').addEventListener('click', () => {
  state.invocationFilters.server = $('#filter-server').value.trim();
  state.invocationFilters.tool = $('#filter-tool').value.trim();
  state.invocationFilters.status = $('#filter-status').value.trim();
  state.lastInvocationSignature = '';
  connectInvocationStream();
  loadInvocations();
});
$('#new-server').addEventListener('click', () => startNewServer());
$('#refresh-servers').addEventListener('click', () => loadServers());
$('#show-disabled-servers').addEventListener('change', () => {
  state.serverFilters.enabled = $('#show-disabled-servers').checked ? null : true;
  loadServers();
});
$('#server-mode').addEventListener('change', updateServerModeFields);
$('#server-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  await saveServer();
});
$('#reset-server-form').addEventListener('click', () => {
  if (state.selectedServerName) {
    loadServerDetail(state.selectedServerName);
    return;
  }
  startNewServer();
});
$('#test-server').addEventListener('click', async () => {
  if (!state.editingServerName) {
    setServerStatus('Select or save a server first.', true);
    return;
  }
  await testServer(state.editingServerName);
});
$('#connect-server').addEventListener('click', async () => {
  if (!state.editingServerName) {
    setServerStatus('Select or save a server first.', true);
    return;
  }
  await connectServer(state.editingServerName);
});
$('#toggle-server-enabled').addEventListener('click', async () => {
  if (!state.editingServerName) {
    setServerStatus('Select or save a server first.', true);
    return;
  }
  await toggleServerEnabled();
});
$('#delete-server').addEventListener('click', async () => {
  if (!state.editingServerName) {
    setServerStatus('Select or save a server first.', true);
    return;
  }
  await deleteServer(state.editingServerName);
});
$$('.view-toggle').forEach((toggle) => {
  toggle.addEventListener('click', (event) => {
    const button = event.target.closest('.toggle-button');
    if (!button || button.disabled) return;
    const target = toggle.dataset.target;
    state.invocationViews[target] = button.dataset.view;
    syncInvocationView(target);
  });
});

document.addEventListener('toggle', (event) => {
  if (event.target.matches('.friendly-details[data-target]')) {
    state.friendlyExpanded[event.target.dataset.target] = event.target.open;
  }
}, true);

function toggleView(view) {
  $('#invocations-view').classList.toggle('hidden', view !== 'invocations');
  $('#servers-view').classList.toggle('hidden', view !== 'servers');
  $('#show-invocations').classList.toggle('active', view === 'invocations');
  $('#show-servers').classList.toggle('active', view === 'servers');
  if (view === 'servers') {
    disconnectInvocationStream();
    loadServers();
    return;
  }
  connectInvocationStream();
}

async function fetchJSON(url, options) {
  const res = await fetch(url, options);
  const body = await res.json();
  if (!res.ok) throw new Error(body?.error?.message || res.statusText);
  return body;
}

async function loadInvocations() {
  const params = new URLSearchParams();
  Object.entries(state.invocationFilters).forEach(([k, v]) => {
    if (v !== '' && v !== null && v !== undefined) params.set(k, String(v));
  });
  const data = await fetchJSON(`/api/v1/admin/invocations?${params.toString()}`);
  applyInvocationStreamData(data.items);
}

function applyInvocationStreamData(items) {
  const signature = JSON.stringify(items.map((item) => [item.invocation_id, item.status, item.completed_at || '']));
  const changed = signature !== state.lastInvocationSignature;
  state.lastInvocationSignature = signature;
  if (!changed) {
    return;
  }

  const tbody = $('#invocation-table tbody');
  tbody.innerHTML = '';
  for (const item of items) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${escapeHTML(item.invocation_id)}</td><td>${escapeHTML(item.status)}</td><td>${escapeHTML(item.submitted_at)}</td>`;
    tr.addEventListener('click', () => loadInvocationDetail(item.invocation_id));
    if (item.invocation_id === state.selectedInvocationID) tr.classList.add('selected-row');
    tbody.appendChild(tr);
  }

  if (!state.selectedInvocationID && items.length > 0) {
    loadInvocationDetail(items[0].invocation_id);
  } else if (state.selectedInvocationID) {
    const selected = items.find((item) => item.invocation_id === state.selectedInvocationID);
    if (!selected && items.length > 0) {
      loadInvocationDetail(items[0].invocation_id);
    } else if (selected) {
      loadInvocationDetail(state.selectedInvocationID);
    }
  }
  loadInvocationSelectionOnly();
}

async function loadInvocationDetail(id) {
  state.selectedInvocationID = id;
  const detail = await fetchJSON(`/api/v1/admin/invocations/${id}`);
  renderJSONWithText('#invocation-detail', '#invocation-detail-text', detail, 'Human-friendly text from invocation result/error', 'detail');
  const events = await fetchJSON(`/api/v1/admin/invocations/${id}/events?limit=200`);
  renderJSONWithText('#invocation-events', '#invocation-events-text', events.items, 'Human-friendly text from invocation events', 'events');
  await loadInvocationSelectionOnly();
}

async function loadInvocationSelectionOnly() {
  const rows = Array.from(document.querySelectorAll('#invocation-table tbody tr'));
  rows.forEach((row) => {
    row.classList.toggle('selected-row', row.firstElementChild?.textContent === state.selectedInvocationID);
  });
}

function connectInvocationStream() {
  disconnectInvocationStream();
  const params = new URLSearchParams();
  params.set('limit', String(state.invocationFilters.limit));
  if (state.invocationFilters.server) params.set('server', state.invocationFilters.server);
  if (state.invocationFilters.tool) params.set('tool', state.invocationFilters.tool);
  if (state.invocationFilters.status) params.set('status', state.invocationFilters.status);
  const source = new EventSource(`/api/v1/admin/invocations/stream?${params.toString()}`);
  state.invocationSource = source;
  $('#invocation-live-status').textContent = 'Live updates via SSE';
  source.addEventListener('invocations', (event) => {
    const payload = JSON.parse(event.data);
    applyInvocationStreamData(payload.items || []);
    $('#invocation-live-status').textContent = 'Live updates via SSE';
  });
  source.addEventListener('error', () => {
    $('#invocation-live-status').textContent = 'Live updates reconnecting…';
  });
}

function disconnectInvocationStream() {
  if (state.invocationSource) {
    state.invocationSource.close();
    state.invocationSource = null;
  }
}

async function loadServers() {
  const params = new URLSearchParams();
  params.set('limit', String(state.serverFilters.limit));
  if (state.serverFilters.enabled !== null) {
    params.set('enabled', String(state.serverFilters.enabled));
  }
  const data = await fetchJSON(`/api/v1/admin/servers?${params.toString()}`);
  const tbody = $('#server-table tbody');
  tbody.innerHTML = '';
  for (const item of data.items) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${escapeHTML(item.name)}</td><td>${renderBadge(item.connection_status)}</td><td>${renderBadge(item.auth_status)}</td><td>${item.enabled ? 'yes' : 'no'}</td>`;
    tr.addEventListener('click', () => loadServerDetail(item.name));
    if (item.name === state.selectedServerName) tr.classList.add('selected-row');
    tbody.appendChild(tr);
  }
  if (state.selectedServerName) {
    const selected = data.items.find((item) => item.name === state.selectedServerName);
    if (!selected) {
      setServerStatus('Selected server is hidden by the current filter. Enable “Show disabled servers” to see disabled entries.', false);
    }
  }
  if (!state.selectedServerName && data.items.length > 0) {
    await loadServerDetail(data.items[0].name);
  }
}

async function loadServerDetail(name) {
  const detail = await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(name)}`);
  state.selectedServerName = detail.name;
  state.editingServerName = detail.name;
  fillServerForm(detail);
  renderServerMeta(detail);
  setServerStatus(`Loaded server ${detail.name}.`, false);
  $('#toggle-server-enabled').textContent = detail.enabled ? 'Disable' : 'Enable';
  $('#connect-server').textContent = detail.reauth_needed || detail.auth_status === 'missing_credentials' ? 'Reconnect' : 'Connect';
  $('#connect-server').disabled = !detail.oauth_provider_id;
  await loadServersSelectionOnly();
}

async function loadServersSelectionOnly() {
  const rows = Array.from(document.querySelectorAll('#server-table tbody tr'));
  rows.forEach((row) => {
    row.classList.toggle('selected-row', row.firstElementChild?.textContent === state.selectedServerName);
  });
}

function fillServerForm(detail) {
  $('#server-name').value = detail.name || '';
  $('#server-mode').value = detail.mode || 'http';
  $('#server-base-url').value = detail.base_url || '';
  $('#server-timeout').value = detail.timeout_seconds || 30;
  $('#server-auth-summary').value = detail.oauth_provider_label || detail.auth_type || 'none';
  $('#server-command').value = detail.command || '';
  $('#server-args').value = JSON.stringify(detail.args || [], null, 2);
  $('#server-env').value = JSON.stringify(detail.env || {}, null, 2);
  $('#server-enabled').checked = Boolean(detail.enabled);
  updateServerModeFields();
}

function renderServerMeta(detail) {
  const badges = [];
  badges.push(renderBadge(detail.connection_status));
  badges.push(renderBadge(detail.auth_status));
  if (detail.reauth_needed) badges.push(renderBadge('reauth_needed', 'warn'));
  if (detail.auth_type) badges.push(renderBadge(detail.auth_type, 'neutral'));
  if (!detail.enabled) badges.push(renderBadge('disabled', 'bad'));
  $('#server-badges').innerHTML = badges.join('');

  const parts = [
    `<div><strong>Enabled:</strong> ${detail.enabled ? 'yes' : 'no'}</div>`,
    `<div><strong>Connection:</strong> ${escapeHTML(detail.connection_status || 'unknown')}</div>`,
    `<div><strong>Auth:</strong> ${escapeHTML(detail.auth_status || 'unknown')}</div>`,
    `<div><strong>Auth type:</strong> ${escapeHTML(detail.auth_type || 'none')}</div>`,
    `<div><strong>Last checked:</strong> ${escapeHTML(detail.last_checked_at || 'never')}</div>`,
    `<div><strong>Last check ok:</strong> ${detail.last_check_ok ? 'yes' : 'no'}</div>`,
  ];
  if (detail.last_error_summary) {
    parts.push(`<div><strong>Last error:</strong> ${escapeHTML(detail.last_error_summary)}</div>`);
  }
  if (detail.action_required) {
    parts.push(`<div><strong>Action required:</strong> ${escapeHTML(detail.action_required)}</div>`);
  }
  $('#server-detail-summary').innerHTML = parts.join('');
}

function startNewServer() {
  state.selectedServerName = '';
  state.editingServerName = '';
  $('#server-form').reset();
  $('#server-timeout').value = 30;
  $('#server-args').value = '[]';
  $('#server-env').value = '{}';
  $('#server-enabled').checked = true;
  $('#server-mode').value = 'http';
  $('#toggle-server-enabled').textContent = 'Disable';
  $('#connect-server').textContent = 'Connect';
  $('#connect-server').disabled = true;
  $('#server-badges').innerHTML = [renderBadge('unknown'), renderBadge('unknown'), renderBadge('not_tested', 'neutral')].join('');
  $('#server-detail-summary').innerHTML = '<div><strong>Status:</strong> Create and test a server to see readiness and auth state.</div>';
  updateServerModeFields();
  setServerStatus('Creating a new server. Runtime servers are DB-backed; TOML is bootstrap-only when the DB is empty.', false);
  $('#server-name').focus();
  loadServersSelectionOnly();
}

function updateServerModeFields() {
  const mode = $('#server-mode').value;
  $$('.field-http').forEach((el) => el.classList.toggle('hidden', mode !== 'http'));
  $$('.field-stdio').forEach((el) => el.classList.toggle('hidden', mode !== 'stdio'));
}

async function saveServer(overrideEnabled) {
  try {
    const payload = buildServerPayload(overrideEnabled);
    const isUpdate = state.editingServerName !== '';
    const url = isUpdate
      ? `/api/v1/admin/servers/${encodeURIComponent(state.editingServerName)}`
      : '/api/v1/admin/servers';
    const method = isUpdate ? 'PUT' : 'POST';
    const saved = await fetchJSON(url, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    state.selectedServerName = saved.name;
    state.editingServerName = saved.name;
    fillServerForm(saved);
    renderServerMeta(saved);
    $('#toggle-server-enabled').textContent = saved.enabled ? 'Disable' : 'Enable';
    $('#connect-server').textContent = saved.reauth_needed || saved.auth_status === 'missing_credentials' ? 'Reconnect' : 'Connect';
  $('#connect-server').disabled = !saved.oauth_provider_id;
    setServerStatus(`Saved server ${saved.name}.`, false);
    await loadServers();
    return saved;
  } catch (err) {
    setServerStatus(err.message || String(err), true);
    throw err;
  }
}

function buildServerPayload(overrideEnabled) {
  const enabled = typeof overrideEnabled === 'boolean' ? overrideEnabled : $('#server-enabled').checked;
  return {
    name: $('#server-name').value.trim(),
    mode: $('#server-mode').value,
    base_url: $('#server-base-url').value.trim(),
    timeout_seconds: Number.parseInt($('#server-timeout').value, 10) || 30,
    command: $('#server-command').value.trim(),
    args: parseJSONField('#server-args', 'Args JSON array', true),
    env: parseJSONField('#server-env', 'Env JSON object', false),
    enabled,
  };
}

function parseJSONField(selector, label, expectArray) {
  let parsed;
  try {
    parsed = JSON.parse($(selector).value.trim() || (expectArray ? '[]' : '{}'));
  } catch {
    throw new Error(`${label} must be valid JSON.`);
  }
  if (expectArray && !Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON array.`);
  }
  if (!expectArray && (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object')) {
    throw new Error(`${label} must be a JSON object.`);
  }
  return parsed;
}

async function testServer(name) {
  try {
    const result = await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(name)}/test`, { method: 'POST' });
    setServerStatus(`Test ${result.ok ? 'passed' : 'failed'}: ${result.message}`, !result.ok);
    const detail = await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(name)}`);
    renderServerMeta(detail);
    await loadServers();
  } catch (err) {
    setServerStatus(err.message || String(err), true);
  }
}

async function connectServer(name) {
  try {
    const result = await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(name)}/connect`, { method: 'POST' });
    setServerStatus('Opening OAuth connect flow…', false);
    window.open(result.connect_url, '_blank', 'noopener,noreferrer');
    startConnectStatusPolling(name);
  } catch (err) {
    setServerStatus(err.message || String(err), true);
  }
}

function startConnectStatusPolling(name) {
  stopConnectStatusPolling();
  state.connectStatusPoll = window.setInterval(async () => {
    try {
      const status = await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(name)}/connect/status`);
      if (status.status === 'succeeded') {
        stopConnectStatusPolling();
        setServerStatus(status.message || 'OAuth connect completed.', false);
        await loadServerDetail(name);
        return;
      }
      if (status.status === 'failed') {
        stopConnectStatusPolling();
        setServerStatus(status.message || 'OAuth connect failed.', true);
        await loadServerDetail(name);
      }
    } catch {
      // ignore temporary polling errors in UI
    }
  }, 2000);
}

function stopConnectStatusPolling() {
  if (state.connectStatusPoll !== null) {
    window.clearInterval(state.connectStatusPoll);
    state.connectStatusPoll = null;
  }
}

async function toggleServerEnabled() {
  try {
    const currentlyEnabled = $('#server-enabled').checked;
    if (currentlyEnabled) {
      await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(state.editingServerName)}?disable=true`, { method: 'DELETE' });
      $('#show-disabled-servers').checked = true;
      state.serverFilters.enabled = null;
      setServerStatus(`Disabled server ${state.editingServerName}. Disabled servers remain in the DB and are shown because “Show disabled servers” is enabled.`, false);
    } else {
      await saveServer(true);
      setServerStatus(`Enabled server ${state.editingServerName}.`, false);
    }
    await loadServers();
    await loadServerDetail(state.editingServerName);
  } catch (err) {
    setServerStatus(err.message || String(err), true);
  }
}

async function deleteServer(name) {
  if (!window.confirm(`Delete server ${name}?`)) return;
  try {
    await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(name)}`, { method: 'DELETE' });
    setServerStatus(`Deleted server ${name}.`, false);
    startNewServer();
    await loadServers();
  } catch (err) {
    setServerStatus(err.message || String(err), true);
  }
}

function renderJSONWithText(rawSelector, textSelector, value, title, target) {
  $(rawSelector).textContent = JSON.stringify(value, null, 2);
  const textBlocks = extractHumanTextBlocks(value);
  const box = $(textSelector);
  if (textBlocks.length === 0) {
    box.classList.add('hidden');
    box.innerHTML = '';
    state.invocationViews[target] = 'raw';
    state.friendlyExpanded[target] = false;
    syncInvocationView(target);
    return;
  }
  const isOpen = state.friendlyExpanded[target];
  box.innerHTML = `<details class="friendly-details" data-target="${escapeHTML(target)}" ${isOpen ? 'open' : ''}><summary>${escapeHTML(title)}</summary><div class="friendly-scroll">${textBlocks
    .map((block, index) => `<div class="text-render-item"><div class="text-render-label">Text ${index + 1}</div><pre class="text-render-pre">${escapeHTML(block)}</pre></div>`)
    .join('')}</div></details>`;
  syncInvocationView(target);
}

function syncInvocationView(target) {
  const rawEl = target === 'detail' ? $('#invocation-detail') : $('#invocation-events');
  const friendlyEl = target === 'detail' ? $('#invocation-detail-text') : $('#invocation-events-text');
  const desiredView = state.invocationViews[target];
  const hasFriendly = friendlyEl.innerHTML.trim() !== '';
  const showFriendly = desiredView === 'friendly' && hasFriendly;
  rawEl.classList.toggle('hidden', showFriendly);
  friendlyEl.classList.toggle('hidden', !showFriendly);
  const toggle = document.querySelector(`.view-toggle[data-target="${target}"]`);
  if (toggle) {
    toggle.classList.toggle('hidden', !hasFriendly);
    toggle.querySelectorAll('.toggle-button').forEach((button) => {
      const active = button.dataset.view === (showFriendly ? 'friendly' : 'raw');
      button.classList.toggle('active', active);
      if (button.dataset.view === 'friendly') {
        button.disabled = !hasFriendly;
      }
    });
  }
}

function extractHumanTextBlocks(value) {
  const out = [];
  walkForText(value, out);
  return dedupeStrings(out.filter((item) => typeof item === 'string' && item.trim() !== ''));
}

function walkForText(value, out) {
  if (Array.isArray(value)) {
    value.forEach((item) => walkForText(item, out));
    return;
  }
  if (!value || typeof value !== 'object') {
    return;
  }
  if (value.type === 'text' && typeof value.text === 'string') {
    out.push(value.text);
  }
  Object.values(value).forEach((item) => walkForText(item, out));
}

function dedupeStrings(items) {
  return Array.from(new Set(items));
}

function setServerStatus(message, isError) {
  const el = $('#server-status');
  el.textContent = message;
  el.classList.toggle('error-text', Boolean(isError));
}

function renderBadge(text, tone) {
  const resolvedTone = tone || badgeTone(text);
  return `<span class="badge badge-${escapeHTML(resolvedTone)}">${escapeHTML(text)}</span>`;
}

function badgeTone(text) {
  const value = String(text || '').toLowerCase();
  if (value.includes('ready')) return 'good';
  if (value.includes('invalid') || value.includes('missing') || value.includes('unreachable') || value.includes('disabled')) return 'bad';
  if (value.includes('reauth') || value.includes('attention') || value.includes('degraded')) return 'warn';
  return 'neutral';
}

function escapeHTML(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

$('#show-disabled-servers').checked = true;
startNewServer();
connectInvocationStream();
syncInvocationView('detail');
syncInvocationView('events');
loadInvocations().catch((err) => {
  $('#invocation-detail').textContent = err.message;
  $('#invocation-live-status').textContent = 'Live updates error';
});
window.addEventListener('beforeunload', () => {
  disconnectInvocationStream();
  stopConnectStatusPolling();
});
