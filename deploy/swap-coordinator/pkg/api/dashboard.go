package api

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Swap Coordinator</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0f172a; color: #e2e8f0; padding: 24px; }
  h1 { font-size: 22px; margin-bottom: 16px; color: #94a3b8; }
  h2 { font-size: 18px; margin: 24px 0 12px; color: #94a3b8; }
  .status { font-size: 13px; color: #64748b; margin-bottom: 20px; }
  .dgd-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 12px; margin-bottom: 24px; }
  .dgd-card { background: #1e293b; border-radius: 10px; padding: 14px 16px; border: 2px solid #334155; }
  .dgd-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
  .dgd-name { font-size: 15px; font-weight: 600; }
  .dgd-ns { font-size: 11px; color: #64748b; }
  .dgd-warm-badge { font-size: 13px; font-weight: 600; padding: 2px 10px; border-radius: 6px; }
  .dgd-status-icon { font-size: 11px; margin-left: 6px; }
  .dgd-controls { display: flex; gap: 12px; align-items: center; margin-top: 8px; }
  .dgd-control { display: flex; align-items: center; gap: 6px; }
  .dgd-control label { font-size: 12px; color: #64748b; font-weight: 500; }
  .dgd-control input { width: 52px; padding: 4px 8px; border-radius: 6px; border: 1px solid #475569; background: #0f172a; color: #e2e8f0; font-size: 13px; text-align: center; }
  .dgd-control input:focus { outline: none; border-color: #60a5fa; }
  .dgd-save { padding: 5px 14px; border-radius: 6px; border: 1px solid #475569; background: #334155; color: #e2e8f0; font-size: 12px; font-weight: 500; cursor: pointer; transition: all 0.2s; }
  .dgd-save:hover { background: #475569; border-color: #60a5fa; }
  .dgd-save.saving { opacity: 0.5; pointer-events: none; }
  .dgd-save.saved { background: #16a34a30; border-color: #22c55e; color: #4ade80; }
  .dgd-save.error { background: #dc262630; border-color: #ef4444; color: #f87171; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 16px; }
  .swap-group { background: #1e293b; border-radius: 10px; padding: 16px; border: 1px solid #334155; }
  .swap-group-header { font-size: 14px; font-weight: 600; color: #cbd5e1; margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
  .swap-group-header .node { color: #38bdf8; }
  .worker { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border-radius: 8px; margin-bottom: 6px; background: #0f172a; border: 1px solid #334155; transition: all 0.3s; cursor: pointer; user-select: none; }
  .worker:hover { background: #172554; }
  .worker .dot { width: 10px; height: 10px; border-radius: 50%; background: #475569; flex-shrink: 0; transition: all 0.3s; }
  .worker .info { flex: 1; min-width: 0; }
  .worker .pod-name { font-size: 13px; font-weight: 500; color: #e2e8f0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .worker .instance-id { font-size: 11px; color: #64748b; font-family: monospace; }
  .worker .logit-label { font-size: 11px; color: #94a3b8; font-family: monospace; }
  .worker .dgd-label { font-size: 10px; }
  .worker .badge { font-size: 10px; font-weight: 600; padding: 2px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: 0.5px; flex-shrink: 0; }
  .empty { color: #475569; font-size: 13px; font-style: italic; text-align: center; padding: 32px; }
</style>
</head>
<body>
<h1>Swap Coordinator Dashboard</h1>
<div class="status" id="status">Loading...</div>
<div id="dgd-section"></div>
<h2>Swap Groups</h2>
<div class="grid" id="grid"></div>
<script>
// Color palette for DGDs — each entry: [main, light, dark-bg, border-warm]
const DGD_COLORS = [
  { main: '#22c55e', light: '#4ade80', bg: '#052e16', border: '#22c55e', glow: '#22c55e80', badge: '#16a34a30', label: '#4ade80' },
  { main: '#3b82f6', light: '#60a5fa', bg: '#172554', border: '#3b82f6', glow: '#3b82f680', badge: '#1d4ed830', label: '#60a5fa' },
  { main: '#f59e0b', light: '#fbbf24', bg: '#451a03', border: '#f59e0b', glow: '#f59e0b80', badge: '#b4540730', label: '#fbbf24' },
  { main: '#ec4899', light: '#f472b6', bg: '#500724', border: '#ec4899', glow: '#ec489980', badge: '#be185d30', label: '#f472b6' },
  { main: '#8b5cf6', light: '#a78bfa', bg: '#2e1065', border: '#8b5cf6', glow: '#8b5cf680', badge: '#6d28d930', label: '#a78bfa' },
  { main: '#06b6d4', light: '#22d3ee', bg: '#083344', border: '#06b6d4', glow: '#06b6d480', badge: '#0e749030', label: '#22d3ee' },
  { main: '#ef4444', light: '#f87171', bg: '#450a0a', border: '#ef4444', glow: '#ef444480', badge: '#dc262630', label: '#f87171' },
  { main: '#14b8a6', light: '#2dd4bf', bg: '#042f2e', border: '#14b8a6', glow: '#14b8a680', badge: '#0f766e30', label: '#2dd4bf' },
];

// Map DGD name -> color
let dgdColorMap = {};

function assignColors(dgds) {
  const names = dgds.map(d => d.name).sort();
  dgdColorMap = {};
  names.forEach((name, i) => {
    dgdColorMap[name] = DGD_COLORS[i % DGD_COLORS.length];
  });
}

function getColor(dgdName) {
  return dgdColorMap[dgdName] || { main: '#475569', light: '#64748b', bg: '#1e293b', border: '#475569', glow: '#47556980', badge: '#47556930', label: '#64748b' };
}

function dgdKey(d) { return d.namespace + '/' + d.name; }

function statusInfo(d) {
  const max = d.max_warm_workers, min = d.min_warm_workers, cur = d.current_warm;
  if (max > 0 && cur > max) return { icon: '\u26a0', text: 'over max' };
  if (min > 0 && cur < min) return { icon: '\u25b2', text: 'below min' };
  return { icon: '\u2713', text: 'ok' };
}

function renderDGDCard(d) {
  const key = dgdKey(d);
  const col = getColor(d.name);
  const st = statusInfo(d);
  const cur = d.current_warm;

  let h = '<div class="dgd-card" id="dgd-card-' + css(key) + '" style="border-color:' + col.main + '">';
  h += '<div class="dgd-header">';
  h += '<div><span class="dgd-name" style="color:' + col.light + '">' + esc(d.name) + '</span> <span class="dgd-ns">' + esc(d.namespace) + '</span></div>';
  h += '<span class="dgd-warm-badge" id="dgd-badge-' + css(key) + '" style="background:' + col.badge + ';color:' + col.light + '">' + cur + ' warm <span class="dgd-status-icon">' + st.icon + '</span></span>';
  h += '</div>';
  h += '<div class="dgd-controls">';
  h += '<div class="dgd-control"><label>min</label>';
  h += '<input type="number" min="0" value="' + d.min_warm_workers + '" id="dgd-min-' + css(key) + '" /></div>';
  h += '<div class="dgd-control"><label>max</label>';
  h += '<input type="number" min="0" value="' + d.max_warm_workers + '" id="dgd-max-' + css(key) + '" /></div>';
  h += '<button class="dgd-save" id="dgd-btn-' + css(key) + '" onclick="saveDGD(\'' + esc(d.name) + '\',\'' + esc(d.namespace) + '\')">Save</button>';
  h += '</div></div>';
  return h;
}

function updateDGDCard(d) {
  const key = css(dgdKey(d));
  const card = document.getElementById('dgd-card-' + key);
  const badge = document.getElementById('dgd-badge-' + key);
  if (!card || !badge) return false;

  const col = getColor(d.name);
  const st = statusInfo(d);
  card.style.borderColor = col.main;
  badge.style.background = col.badge;
  badge.style.color = col.light;
  badge.innerHTML = d.current_warm + ' warm <span class="dgd-status-icon">' + st.icon + '</span>';
  return true;
}

function cardHasEdits(d) {
  const key = css(dgdKey(d));
  const minEl = document.getElementById('dgd-min-' + key);
  const maxEl = document.getElementById('dgd-max-' + key);
  if (!minEl || !maxEl) return false;
  return parseInt(minEl.value, 10) !== d.min_warm_workers ||
         parseInt(maxEl.value, 10) !== d.max_warm_workers;
}

async function refresh() {
  try {
    const [stateRes, dgdRes] = await Promise.all([fetch('/state'), fetch('/dgds')]);
    const groups = await stateRes.json();
    const dgds = await dgdRes.json();
    const grid = document.getElementById('grid');
    const status = document.getElementById('status');
    const dgdSection = document.getElementById('dgd-section');

    // Assign colors based on DGD names
    if (dgds && dgds.length > 0) assignColors(dgds);

    // Render DGD cards
    if (dgds && dgds.length > 0) {
      const existingCards = document.querySelectorAll('[id^="dgd-card-"]');
      if (existingCards.length === dgds.length && existingCards.length > 0) {
        for (const d of dgds) {
          if (cardHasEdits(d)) {
            updateDGDCard(d);
          } else {
            if (!updateDGDCard(d)) { rebuildDGDs(dgds, dgdSection); break; }
            const key = css(dgdKey(d));
            const minEl = document.getElementById('dgd-min-' + key);
            const maxEl = document.getElementById('dgd-max-' + key);
            if (minEl) minEl.value = d.min_warm_workers;
            if (maxEl) maxEl.value = d.max_warm_workers;
          }
        }
      } else {
        rebuildDGDs(dgds, dgdSection);
      }
    } else {
      dgdSection.innerHTML = '';
    }

    // Render swap groups
    if (!groups || groups.length === 0) {
      grid.innerHTML = '<div class="empty">No swap groups registered</div>';
      status.textContent = 'Last updated: ' + new Date().toLocaleTimeString() + ' \u2014 0 swap groups, 0 workers';
      return;
    }

    let totalWorkers = 0;
    let html = '';
    groups.sort((a, b) => a.swap_group_uuid.localeCompare(b.swap_group_uuid));

    for (const sg of groups) {
      const workers = sg.workers || [];
      totalWorkers += workers.length;
      workers.sort((a, b) => (a.pod_name || '').localeCompare(b.pod_name || ''));

      html += '<div class="swap-group">';
      html += '<div class="swap-group-header"><span class="node">' + esc(sg.swap_group_uuid) + '</span> (' + workers.length + ' workers)</div>';

      for (const w of workers) {
        const warm = w.is_warm;
        const col = getColor(w.dgd_name || '');
        let workerStyle = '';
        let dotStyle = 'background:' + col.main + '40;';
        let badgeHtml = '';
        if (warm) {
          workerStyle = 'border-color:' + col.main + ';background:' + col.bg + ';';
          dotStyle = 'background:' + col.main + ';box-shadow:0 0 8px ' + col.glow + ';';
          badgeHtml = '<div class="badge" style="background:' + col.badge + ';color:' + col.light + '">warm</div>';
        }
        html += '<div class="worker" style="' + workerStyle + '" onclick="setWarm(\'' + esc(sg.swap_group_uuid) + '\',\'' + esc(w.instance_id) + '\')">';
        html += '<div class="dot" style="' + dotStyle + '"></div>';
        html += '<div class="info">';
        html += '<div class="pod-name">' + esc(w.pod_name || 'unknown') + '</div>';
        html += '<div class="instance-id">' + w.instance_id + '</div>';
        if (w.logit != null) html += '<div class="logit-label">logit: ' + w.logit.toFixed(3) + '</div>';
        if (w.dgd_name) html += '<div class="dgd-label" style="color:' + col.label + '">' + esc(w.dgd_name) + '</div>';
        html += '</div>';
        html += badgeHtml;
        html += '</div>';
      }
      html += '</div>';
    }

    grid.innerHTML = html;
    status.textContent = 'Last updated: ' + new Date().toLocaleTimeString() + ' \u2014 ' + groups.length + ' swap groups, ' + totalWorkers + ' workers';
  } catch (e) {
    document.getElementById('status').textContent = 'Error: ' + e.message;
  }
}

function rebuildDGDs(dgds, container) {
  let h = '<h2>DGD Configurations</h2><div class="dgd-grid">';
  for (const d of dgds) h += renderDGDCard(d);
  h += '</div>';
  container.innerHTML = h;
}

async function saveDGD(name, namespace) {
  const key = css(namespace + '/' + name);
  const minInput = document.getElementById('dgd-min-' + key);
  const maxInput = document.getElementById('dgd-max-' + key);
  const btn = document.getElementById('dgd-btn-' + key);
  if (!minInput || !maxInput || !btn) return;

  const minVal = parseInt(minInput.value, 10) || 0;
  const maxVal = parseInt(maxInput.value, 10) || 0;

  btn.textContent = 'Saving...';
  btn.className = 'dgd-save saving';

  try {
    const res = await fetch('/dgds', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, namespace, min_warm_workers: minVal, max_warm_workers: maxVal }),
    });
    if (!res.ok) {
      const err = await res.json();
      btn.textContent = 'Error';
      btn.className = 'dgd-save error';
      setTimeout(() => { btn.textContent = 'Save'; btn.className = 'dgd-save'; }, 2000);
      alert('Failed: ' + (err.error || res.statusText));
      return;
    }
    btn.textContent = 'Saved!';
    btn.className = 'dgd-save saved';
    setTimeout(() => { btn.textContent = 'Save'; btn.className = 'dgd-save'; refresh(); }, 1000);
  } catch (e) {
    btn.textContent = 'Error';
    btn.className = 'dgd-save error';
    setTimeout(() => { btn.textContent = 'Save'; btn.className = 'dgd-save'; }, 2000);
    alert('Error: ' + e.message);
  }
}

async function setWarm(swapGroupUUID, instanceID) {
  try {
    const res = await fetch('/state/warm', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ swap_group_uuid: swapGroupUUID, instance_id: instanceID }),
    });
    if (!res.ok) {
      const err = await res.json();
      alert('Failed: ' + (err.error || res.statusText));
      return;
    }
    refresh();
  } catch (e) {
    alert('Error: ' + e.message);
  }
}

function esc(s) { const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function css(s) { return s.replace(/[^a-zA-Z0-9-]/g, '-'); }

refresh();
setInterval(refresh, 2000);
</script>
</body>
</html>`
