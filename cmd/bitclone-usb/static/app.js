'use strict';
const $ = (s) => document.querySelector(s);
const TOKEN = new URLSearchParams(location.search).get('t') || '';

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) if (kid != null && kid !== false) el.append(kid.nodeType ? kid : String(kid));
  return el;
}
async function api(path, body) {
  const opt = { method: body === undefined ? 'GET' : 'POST', headers: { 'X-Token': TOKEN, 'Content-Type': 'application/json' } };
  if (body !== undefined) opt.body = JSON.stringify(body);
  const r = await fetch('/api/' + path, opt);
  const j = await r.json().catch(() => ({ error: r.statusText }));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
function fmtBytes(n) {
  if (n == null || n < 0) return '–';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i];
}
function fmtDur(s) {
  if (s == null || s < 0 || !isFinite(s)) return '–';
  s = Math.round(s); const m = Math.floor(s / 60);
  return m ? `${m}m ${s % 60}s` : `${s}s`;
}

const S = { status: {}, isos: [], disks: null, disksErr: null, sel: null, dismissed: 0 };

function renderISO() {
  const iso = S.status.iso;
  const box = $('#iso');
  if (!iso) {
    box.replaceChildren(h('div', { class: 'empty' }, S.status.repo
      ? 'No boot image yet. Click “Download latest”, or choose an ISO file you already have.'
      : 'Choose the bitclone ISO file (bitclone-…-amd64.iso).'));
  } else {
    box.replaceChildren(h('div', { class: 'isobox' },
      h('div', { class: 'row' }, h('b', { class: 'grow' }, iso.name),
        iso.bitclone ? h('span', { class: 'tag green' }, 'bitclone image') : h('span', { class: 'tag orange' }, 'not a bitclone image')),
      h('div', { class: 'muted small' }, fmtBytes(iso.size), ' · ', iso.modified, ' · ', iso.path),
      !iso.bitclone ? h('div', { class: 'notice' }, 'This ISO is not labelled BITCLONE. It can still be written, but it will not start bitclone.') : null));
  }
  $('#btn-download').classList.toggle('hidden', !S.status.repo);
  const sel = $('#iso-list');
  const others = S.isos.filter((x) => !iso || x.path !== iso.path);
  sel.classList.toggle('hidden', !others.length);
  sel.replaceChildren(h('option', { value: '' }, `Other images found (${others.length})…`),
    ...others.map((x) => h('option', { value: x.path }, `${x.name} — ${fmtBytes(x.size)}, ${x.modified}`)));
}

function renderDisks() {
  const box = $('#disks');
  if (S.disksErr) { box.replaceChildren(h('div', { class: 'empty error' }, S.disksErr)); return; }
  if (!S.disks) { box.replaceChildren(h('div', { class: 'empty' }, 'Looking for USB drives…')); return; }
  if (!S.disks.length) { box.replaceChildren(h('div', { class: 'empty' }, 'No USB stick found. Plug one in (8 GB or larger) and press Refresh.')); return; }
  if (S.sel && !S.disks.some((d) => d.number === S.sel.number && d.size === S.sel.size)) S.sel = null;
  const isoSize = S.status.iso ? S.status.iso.size : 0;
  box.replaceChildren(...S.disks.map((d) => {
    const tooSmall = isoSize && d.size < isoSize;
    const vols = d.letters.map((l, i) => l + ':' + (d.labels[i] ? ' ' + d.labels[i] : '')).join(', ');
    return h('div', { class: 'card' + (S.sel && S.sel.number === d.number ? ' sel' : ''),
                      onclick: () => { S.sel = d; renderDisks(); renderGo(); } },
      h('div', { class: 'title' }, h('span', {}, d.model || 'USB drive'), h('span', {}, fmtBytes(d.size))),
      h('div', { class: 'sub' }, vols || 'no drive letter', h('span', { class: 'tag' }, d.bus), ' disk ', d.number),
      d.large ? h('div', { class: 'sub', style: 'color:var(--warn)' }, 'Large drive. Is this an external hard disk with your files?') : null,
      tooSmall ? h('div', { class: 'sub error' }, 'Too small for this image') : null);
  }));
}

function renderGo() {
  const iso = S.status.iso, d = S.sel;
  const ok = iso && d && d.size >= iso.size;
  $('#btn-write').disabled = !ok || (S.status.job && S.status.job.state === 'running');
  $('#summary').textContent = ok ? `${iso.name} → ${d.model} (${fmtBytes(d.size)})` : '';
}

async function loadDisks() {
  S.disks = null; S.disksErr = null; renderDisks();
  try { S.disks = (await api('disks')).disks; } catch (e) { S.disksErr = e.message; }
  renderDisks(); renderGo();
}
async function loadISOs() {
  try { S.isos = (await api('isos')).isos || []; } catch (e) { S.isos = []; }
  renderISO();
}

function modal(title, body, yes, requireText) {
  return new Promise((resolve) => {
    $('#modal-title').textContent = title;
    $('#modal-body').replaceChildren(body);
    const y = $('#modal-yes'); y.textContent = yes;
    let input = null;
    if (requireText) {
      input = h('input', { type: 'text', autocomplete: 'off', spellcheck: 'false' });
      $('#modal-body').append(input);
      y.disabled = true;
      input.oninput = () => { y.disabled = input.value.trim().toUpperCase() !== requireText; };
      input.onkeydown = (e) => { if (e.key === 'Enter' && !y.disabled) y.click(); };
      setTimeout(() => input.focus(), 0);
    } else y.disabled = false;
    $('#modal').classList.remove('hidden');
    const done = (v) => { $('#modal').classList.add('hidden'); resolve(v); };
    y.onclick = () => done(true); $('#modal-no').onclick = () => done(false);
  });
}

function showJob() {
  const j = S.status.job, p = S.status.progress, ov = $('#job');
  if (!j || (j.state !== 'running' && j.id === S.dismissed)) { ov.classList.add('hidden'); return; }
  ov.classList.remove('hidden');
  const titles = { download: ['Downloading boot image…', 'Download complete'], write: ['Writing USB stick…', 'USB stick ready'] };
  $('#job-title').textContent = j.state === 'running' ? titles[j.kind][0] : j.state === 'done' ? titles[j.kind][1] : j.summary;
  $('#job-state').textContent = j.state; $('#job-state').className = 'pill ' + j.state;
  $('#job-phase').textContent = p.phase;
  const pct = j.state === 'done' ? 100 : p.percent;
  $('#job-bar').style.width = pct.toFixed(1) + '%';
  $('#job-pct').textContent = pct.toFixed(1) + '%';
  $('#job-done').textContent = fmtBytes(p.done) + (p.total > 0 ? ' / ' + fmtBytes(p.total) : '');
  $('#job-rate').textContent = p.rate ? fmtBytes(p.rate) + '/s' : '–';
  const running = j.state === 'running';
  $('#job-eta-k').textContent = running ? 'Time left' : 'Took';
  $('#job-eta').textContent = running ? fmtDur(p.eta) : fmtDur((new Date(j.ended) - new Date(j.started)) / 1000);
  $('#job-error').textContent = j.error || ''; $('#job-error').classList.toggle('hidden', !j.error || j.state === 'cancelled');
  const msg = $('#job-done-msg');
  msg.classList.toggle('hidden', !(j.state === 'done' && j.kind === 'write'));
  msg.replaceChildren('Your bitclone USB stick is ready. Remove it safely, plug it into the PC whose disk you want to back up, ',
    'and pick the USB stick in that PC’s boot menu (usually F12, F11, F8 or Esc right after power-on). ',
    'If Windows asks to format the stick now, click Cancel.');
  const log = $('#job-log'); log.textContent = p.log.join('\n'); log.scrollTop = log.scrollHeight;
  $('#job-cancel').classList.toggle('hidden', !running);
  $('#job-close').classList.toggle('hidden', running);
}

async function poll() {
  try {
    const prevJob = S.status.job;
    S.status = await api('status');
    $('#ver').textContent = S.status.version ? 'version ' + S.status.version : '';
    showJob();
    if (prevJob && prevJob.state === 'running' && S.status.job && S.status.job.state !== 'running') {
      renderISO(); loadDisks(); if (S.status.job.kind === 'download') loadISOs();
    }
    renderGo();
  } catch (e) { /* backend gone */ }
  setTimeout(poll, 1000);
}

$('#btn-refresh').onclick = loadDisks;
$('#btn-browse').onclick = async () => {
  try { const r = await api('iso/browse', {}); if (!r.cancelled) { S.status.iso = r.iso; renderISO(); renderDisks(); renderGo(); } }
  catch (e) { alert(e.message); }
};
$('#iso-list').onchange = async (e) => {
  if (!e.target.value) return;
  try { S.status.iso = (await api('iso/select', { path: e.target.value })).iso; } catch (er) { alert(er.message); }
  renderISO(); renderDisks(); renderGo();
};
$('#btn-download').onclick = () => api('iso/download', {}).catch((e) => alert(e.message));
$('#btn-write').onclick = async () => {
  const d = S.sel, iso = S.status.iso;
  const ok = await modal(`Erase ${d.model}?`, h('div', {},
    h('p', {}, 'All files on ', h('b', {}, d.model), ` (${fmtBytes(d.size)}`, d.letters.length ? ', drive ' + d.letters.join(', ') + ':' : '', ') will be permanently deleted, and it will be replaced with ', h('b', {}, iso.name), '.'),
    h('p', {}, 'Type ', h('b', {}, 'ERASE'), ' to continue:')), 'Erase and write', 'ERASE');
  if (!ok) return;
  try { await api('write', d); } catch (e) { alert(e.message); }
};
$('#job-cancel').onclick = async () => {
  if (await modal('Cancel?', h('p', {}, 'The USB stick will be left unusable until you write it again.'), 'Cancel writing')) api('cancel', {});
};
$('#job-close').onclick = () => { S.dismissed = S.status.job.id; showJob(); };

renderISO(); renderDisks();
poll(); loadISOs(); loadDisks();
