const csrf = { 'x-hearth-csrf': '1', 'content-type': 'application/json' };
const $ = (s) => document.querySelector(s);

async function api(path, opts = {}) {
  const r = await fetch(path, { credentials: 'same-origin', ...opts });
  if (!r.ok) throw new Error(`${path}: ${r.status}`);
  return r.status === 204 ? null : r.json();
}

$('#login').addEventListener('submit', async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  $('#login-err').textContent = '';
  try {
    await api('/api/auth/login', {
      method: 'POST', headers: csrf,
      body: JSON.stringify({ username: f.get('username'), password: f.get('password') }),
    });
    await start();
  } catch { $('#login-err').textContent = 'login failed'; }
});

$('#logout').addEventListener('click', async () => {
  try { await api('/api/auth/logout', { method: 'POST', headers: csrf }); } catch {}
  location.reload();
});

$('#new').addEventListener('click', async () => {
  const name = prompt('workspace name', 'scratch');
  if (!name) return;
  try {
    await api('/api/workspaces', {
      method: 'POST', headers: csrf,
      body: JSON.stringify({ name, image: 'docker.io/library/busybox:stable' }),
    });
  } catch (err) { alert('create failed: ' + err.message); }
  await refreshList();
});

$('#ws').addEventListener('change', () => attach($('#ws').value));

async function start() {
  $('#login').classList.add('hidden');
  $('header').classList.remove('hidden');
  $('#term').classList.remove('hidden');
  await refreshList();
}

async function refreshList() {
  const { workspaces } = await api('/api/workspaces');
  const sel = $('#ws');
  sel.innerHTML = '';
  for (const w of workspaces) {
    const o = document.createElement('option');
    o.value = w.id;
    o.textContent = `${w.name} (${w.state})`;
    o.disabled = w.state !== 'running';
    sel.appendChild(o);
  }
  const running = workspaces.find((w) => w.state === 'running');
  if (running) { sel.value = running.id; attach(running.id); }
}

let term, fit, sock;
function attach(id) {
  if (!id) return;
  if (sock) { sock.onclose = null; sock.onmessage = null; sock.close(); }
  if (!term) {
    term = new Terminal({ cursorBlink: true, fontSize: 13 });
    fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open($('#term'));
    term.onData(send);
    window.addEventListener('resize', sendResize);
  }
  fit.fit();
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  sock = new WebSocket(`${proto}://${location.host}/api/workspaces/${id}/terminal?cols=${term.cols}&rows=${term.rows}`);
  sock.binaryType = 'arraybuffer';
  sock.onmessage = (ev) => term.write(new Uint8Array(ev.data));
  sock.onopen = () => { term.clear(); sendResize(); };
  sock.onclose = () => term.write('\r\n[disconnected]\r\n');
}

function send(data) {
  if (sock && sock.readyState === WebSocket.OPEN) {
    const b = new TextEncoder().encode(data);
    const f = new Uint8Array(b.length + 1);
    f[0] = 0x00;
    f.set(b, 1);
    sock.send(f);
  }
}

function sendResize() {
  if (!term || !sock || sock.readyState !== WebSocket.OPEN) return;
  fit.fit();
  const f = new Uint8Array(5);
  f[0] = 0x01;
  const dv = new DataView(f.buffer);
  dv.setUint16(1, term.cols);
  dv.setUint16(3, term.rows);
  sock.send(f);
}

// Skip the login form if a session cookie is already valid.
api('/api/auth/me').then(start).catch(() => {});
