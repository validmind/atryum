const state = {
  invocationFilters: { offset: 0, limit: 50, server: '', tool: '', status: '' },
  serverFilters: { offset: 0, limit: 100, enabled: true },
  selectedServerName: '',
  editingServerName: '',
};

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

$('#show-invocations').addEventListener('click', () => toggleView('invocations'));
$('#show-servers').addEventListener('click', () => toggleView('servers'));
$('#apply-filters').addEventListener('click', () => {
  state.invocationFilters.server = $('#filter-server').value.trim();
  state.invocationFilters.tool = $('#filter-tool').value.trim();
  state.invocationFilters.status = $('#filter-status').value.trim();
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

function toggleView(view) {
  $('#invocations-view').classList.toggle('hidden', view !== 'invocations');
  $('#servers-view').classList.toggle('hidden', view !== 'servers');
  $('#show-invocations').classList.toggle('active', view === 'invocations');
  $('#show-servers').classList.toggle('active', view === 'servers');
  if (view === 'servers') loadServers();
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
  const tbody = $('#invocation-table tbody');
  tbody.innerHTML = '';
  for (const item of data.items) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${escapeHTML(item.invocation_id)}</td><td>${escapeHTML(item.status)}</td><td>${escapeHTML(item.submitted_at)}</td>`;
    tr.addEventListener('click', () => loadInvocationDetail(item.invocation_id));
    tbody.appendChild(tr);
  }
}

async function loadInvocationDetail(id) {
  const detail = await fetchJSON(`/api/v1/admin/invocations/${id}`);
  $('#invocation-detail').textContent = JSON.stringify(detail, null, 2);
  const events = await fetchJSON(`/api/v1/admin/invocations/${id}/events?limit=200`);
  $('#invocation-events').textContent = JSON.stringify(events.items, null, 2);
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
    tr.innerHTML = `<td>${escapeHTML(item.name)}</td><td>${escapeHTML(item.mode)}</td><td>${item.enabled ? 'yes' : 'no'}</td>`;
    tr.addEventListener('click', () => loadServerDetail(item.name));
    if (item.name === state.selectedServerName) tr.classList.add('selected-row');
    tbody.appendChild(tr);
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
  setServerStatus(`Loaded server ${detail.name}.`, false);
  $('#toggle-server-enabled').textContent = detail.enabled ? 'Disable' : 'Enable';
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
  $('#server-auth-token').value = detail.auth_token || '';
  $('#server-command').value = detail.command || '';
  $('#server-args').value = JSON.stringify(detail.args || [], null, 2);
  $('#server-env').value = JSON.stringify(detail.env || {}, null, 2);
  $('#server-enabled').checked = Boolean(detail.enabled);
  updateServerModeFields();
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
  updateServerModeFields();
  setServerStatus('Creating a new server.', false);
  $('#server-name').focus();
  loadServersSelectionOnly();
}

function updateServerModeFields() {
  const mode = $('#server-mode').value;
  $$('.field-http').forEach((el) => el.classList.toggle('hidden', mode !== 'http'));
  $$('.field-stdio').forEach((el) => el.classList.toggle('hidden', mode !== 'stdio'));
}

async function saveServer() {
  try {
    const payload = buildServerPayload();
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
    $('#toggle-server-enabled').textContent = saved.enabled ? 'Disable' : 'Enable';
    setServerStatus(`Saved server ${saved.name}.`, false);
    await loadServers();
  } catch (err) {
    setServerStatus(err.message || String(err), true);
  }
}

function buildServerPayload() {
  const mode = $('#server-mode').value;
  return {
    name: $('#server-name').value.trim(),
    mode,
    base_url: $('#server-base-url').value.trim(),
    auth_token: $('#server-auth-token').value,
    timeout_seconds: Number.parseInt($('#server-timeout').value, 10) || 30,
    command: $('#server-command').value.trim(),
    args: parseJSONField('#server-args', 'Args JSON array', true),
    env: parseJSONField('#server-env', 'Env JSON object', false),
    enabled: $('#server-enabled').checked,
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
  } catch (err) {
    setServerStatus(err.message || String(err), true);
  }
}

async function toggleServerEnabled() {
  try {
    const currentlyEnabled = $('#server-enabled').checked;
    if (currentlyEnabled) {
      await fetchJSON(`/api/v1/admin/servers/${encodeURIComponent(state.editingServerName)}?disable=true`, { method: 'DELETE' });
      setServerStatus(`Disabled server ${state.editingServerName}.`, false);
    } else {
      await saveServer();
      setServerStatus(`Enabled server ${state.editingServerName}.`, false);
    }
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

function setServerStatus(message, isError) {
  const el = $('#server-status');
  el.textContent = message;
  el.classList.toggle('error-text', Boolean(isError));
}

function escapeHTML(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

startNewServer();
loadInvocations().catch((err) => {
  $('#invocation-detail').textContent = err.message;
});
