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
  .status { font-size: 13px; color: #64748b; margin-bottom: 20px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 16px; }
  .swap-group { background: #1e293b; border-radius: 10px; padding: 16px; border: 1px solid #334155; }
  .swap-group-header { font-size: 14px; font-weight: 600; color: #cbd5e1; margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
  .swap-group-header .node { color: #38bdf8; }
  .worker { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border-radius: 8px; margin-bottom: 6px; background: #0f172a; border: 1px solid #334155; transition: all 0.3s; cursor: pointer; user-select: none; }
  .worker:hover { border-color: #60a5fa; background: #172554; }
  .worker.warm { border-color: #22c55e; background: #052e16; }
  .worker.warm:hover { border-color: #4ade80; background: #053c1a; }
  .worker .dot { width: 10px; height: 10px; border-radius: 50%; background: #475569; flex-shrink: 0; }
  .worker.warm .dot { background: #22c55e; box-shadow: 0 0 8px #22c55e80; }
  .worker .info { flex: 1; min-width: 0; }
  .worker .pod-name { font-size: 13px; font-weight: 500; color: #e2e8f0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .worker .instance-id { font-size: 11px; color: #64748b; font-family: monospace; }
  .worker .badge { font-size: 10px; font-weight: 600; padding: 2px 8px; border-radius: 4px; text-transform: uppercase; letter-spacing: 0.5px; flex-shrink: 0; }
  .worker.warm .badge { background: #16a34a30; color: #4ade80; }
  .empty { color: #475569; font-size: 13px; font-style: italic; text-align: center; padding: 32px; }
</style>
</head>
<body>
<h1>Swap Coordinator Dashboard</h1>
<div class="status" id="status">Loading...</div>
<div class="grid" id="grid"></div>
<script>
async function refresh() {
  try {
    const res = await fetch('/state');
    const groups = await res.json();
    const grid = document.getElementById('grid');
    const status = document.getElementById('status');

    if (!groups || groups.length === 0) {
      grid.innerHTML = '<div class="empty">No swap groups registered</div>';
      status.textContent = 'Last updated: ' + new Date().toLocaleTimeString() + ' — 0 swap groups, 0 workers';
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
        html += '<div class="worker' + (warm ? ' warm' : '') + '" onclick="setWarm(\'' + esc(sg.swap_group_uuid) + '\',\'' + esc(w.instance_id) + '\')">';
        html += '<div class="dot"></div>';
        html += '<div class="info">';
        html += '<div class="pod-name">' + esc(w.pod_name || 'unknown') + '</div>';
        html += '<div class="instance-id">' + w.instance_id + '</div>';
        html += '</div>';
        if (warm) html += '<div class="badge">warm</div>';
        html += '</div>';
      }
      html += '</div>';
    }

    grid.innerHTML = html;
    status.textContent = 'Last updated: ' + new Date().toLocaleTimeString() + ' — ' + groups.length + ' swap groups, ' + totalWorkers + ' workers';
  } catch (e) {
    document.getElementById('status').textContent = 'Error: ' + e.message;
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

function esc(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

refresh();
setInterval(refresh, 2000);
</script>
</body>
</html>`
