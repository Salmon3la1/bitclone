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

const S = { status: {}, isos: [], disks: null, disksErr: null, sel: null, dismissed: 0, bci: null };

function renderISO() {
  const iso = S.status.iso;
  const box = $('#iso');
  if (!iso) {
    box.replaceChildren(h('div', { class: 'empty' }, S.status.repo
      ? 'No boot image yet. Click “Download latest”, or choose an ISO file you already have.'
      : 'Choose the IMAGen ISO file (imagen-…-amd64.iso).'));
  } else {
    box.replaceChildren(h('div', { class: 'isobox' },
      h('div', { class: 'row' }, h('b', { class: 'grow' }, iso.name),
        iso.imagen ? h('span', { class: 'tag green' }, 'IMAGen image') : h('span', { class: 'tag orange' }, 'not an IMAGen image')),
      h('div', { class: 'muted small' }, fmtBytes(iso.size), ' · ', iso.modified, ' · ', iso.path),
      !iso.imagen ? h('div', { class: 'notice' }, 'This ISO is not labelled IMAGEN. It can still be written, but it will not start imagen.') : null));
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
    const extra = diskContents(d);
    return h('div', { class: 'card' + (S.sel && S.sel.number === d.number ? ' sel' : ''),
                      onclick: () => { S.sel = d; renderDisks(); renderGo(); } },
      h('div', { class: 'title' }, h('span', {}, diskName(d)), h('span', {}, fmtBytes(d.size))),
      h('div', { class: 'sub' }, d.model || 'USB drive', h('span', { class: 'tag' }, d.bus), ' disk ', d.number,
        d.serial ? ' · serial ' + d.serial : ''),
      extra ? h('div', { class: 'sub' }, extra) : null,
      d.large ? h('div', { class: 'sub', style: 'color:var(--warn)' }, 'Large drive. Is this an external hard disk with your files?') : null,
      tooSmall ? h('div', { class: 'sub error' }, 'Too small for this image') : null);
  }));
}

// The name Explorer would show, "ESD-USB (E:)" or "USB Drive (E:, F:)", or for
// a stick without a letter the label read from the disk itself ("IMAGEN").
function diskName(d) {
  if (d.letters.length) {
    const label = d.labels.find((l) => l) || 'USB Drive';
    return `${label} (${d.letters.map((l) => l + ':').join(', ')})`;
  }
  const v = (d.volumes || []).find((v) => v.label);
  return v ? v.label : (d.model || 'USB drive');
}

// An extra line: each letter with its own label when there are several, or
// what was found on a stick Windows gives no letter.
function diskContents(d) {
  if (d.letters.length > 1) return d.letters.map((l, i) => l + ':' + (d.labels[i] ? ' ' + d.labels[i] : '')).join(', ');
  if (d.letters.length) return '';
  const vols = d.volumes || [];
  const name = diskName(d);
  const found = vols.map((v) => [v.label === name ? '' : v.label, v.fs, v.size ? '(' + fmtBytes(v.size) + ')' : ''].filter(Boolean).join(' '));
  const imagen = vols.some((v) => v.fs === 'ISO 9660' && /^IMAGEN/i.test(v.label));
  return 'No drive letter' + (imagen ? ', already an IMAGen stick' : '') + (found.length ? ' · ' + found.join(', ') : '');
}

function renderGo() {
  const iso = S.status.iso, d = S.sel;
  const ok = iso && d && d.size >= iso.size;
  $('#btn-write').disabled = !ok || (S.status.job && S.status.job.state === 'running');
  $('#summary').textContent = ok ? `${iso.name} → ${diskName(d)} (${fmtBytes(d.size)})` : '';
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

function modal(title, body, yes) {
  return new Promise((resolve) => {
    $('#modal-title').textContent = title;
    $('#modal-body').replaceChildren(body);
    const y = $('#modal-yes'); y.textContent = yes; y.disabled = false;
    $('#modal').classList.remove('hidden');
    const done = (v) => { $('#modal').classList.add('hidden'); resolve(v); };
    y.onclick = () => done(true); $('#modal-no').onclick = () => done(false);
  });
}

function showJob() {
  const j = S.status.job, p = S.status.progress, ov = $('#job');
  if (!j || (j.state !== 'running' && j.id === S.dismissed)) { ov.classList.add('hidden'); return; }
  ov.classList.remove('hidden');
  const titles = { download: ['Downloading boot image…', 'Download complete'], write: ['Writing USB stick…', 'USB stick ready'],
                   convert: ['Converting to .img…', 'Disk image saved'] };
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
  msg.classList.toggle('hidden', !(j.state === 'done' && (j.kind === 'write' || j.kind === 'convert')));
  if (j.kind === 'convert') msg.replaceChildren('Saved ', h('b', {}, S.bci ? S.bci.out : 'the .img file'), '. Open it in your recovery tool or virtual machine as a raw disk image (in Digger: Open a disk image).');
  else msg.replaceChildren('Your IMAGen USB stick is ready. Remove it safely, plug it into the PC whose disk you want to back up, ',
    'and pick the USB stick in that PC’s boot menu (usually F12, F11, F8 or Esc right after power-on). ',
    'If Windows asks to format the stick now, click Cancel.');
  const log = $('#job-log'); log.textContent = p.log.join('\n'); log.scrollTop = log.scrollHeight;
  $('#job-cancel').classList.toggle('hidden', !running);
  $('#job-close').classList.toggle('hidden', running);
}


// ---------- convert a .bci backup to .img ----------
const FOUR_GB = 4 * 1024 * 1024 * 1024 - 1;
// Bytes the .img will really take on its drive: NTFS keeps the areas that
// were not backed up as empty space; other filesystems store every byte.
function bciNeed(b) { return b.outFS === 'NTFS' || b.outFS === 'ReFS' ? b.data : b.diskSize; }
function bciProblem(b) {
  if (/^FAT/i.test(b.outFS) && b.diskSize > FOUR_GB) return `That drive is ${b.outFS}, which holds files up to 4 GB. Save the .img on an NTFS or exFAT drive.`;
  if (b.outFree && bciNeed(b) > b.outFree) return `Not enough free space there: needs about ${fmtBytes(bciNeed(b))}, ${fmtBytes(b.outFree)} free.`;
  return '';
}

function renderBCI() {
  const b = S.bci, box = $('#bci');
  $('#btn-bci').textContent = b ? 'Choose another backup…' : 'Choose backup…';
  $('#btn-convert').classList.toggle('hidden', !b);
  if (!b) { box.replaceChildren(); $('#bci-summary').textContent = ''; return; }
  const created = b.created ? new Date(b.created).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '';
  const problem = bciProblem(b);
  box.replaceChildren(h('div', { class: 'isobox' },
    h('div', { class: 'row' }, h('b', { class: 'grow' }, b.name),
      h('span', { class: 'tag' + (b.mode === 'full' ? ' green' : '') }, b.mode === 'full' ? 'Every sector' : 'Used data only')),
    h('div', { class: 'muted small' }, 'Backup of ', b.model || 'a disk', ` (${fmtBytes(b.diskSize)})`, created ? ' · ' + created : '',
      ' · ', fmtBytes(b.fileSize), b.parts > 1 ? ` in ${b.parts} files` : ''),
    b.mode !== 'full' ? h('div', { class: 'notice' }, 'This backup copied only the data in use. Free space was not saved, so deleted files cannot be recovered from it. Areas that were not saved read as zeros.') : null,
    h('div', { class: 'row', style: 'margin-top:10px' },
      h('div', { class: 'grow small' }, h('span', { class: 'muted' }, 'Saves to '), h('span', { class: 'mono' }, b.out)),
      h('button', { class: 'small', onclick: chooseImgPath }, 'Change…')),
    h('div', { class: 'muted small', style: 'margin-top:4px' },
      `A ${fmtBytes(b.diskSize)} disk image`,
      bciNeed(b) < b.diskSize ? `, taking about ${fmtBytes(bciNeed(b))} of space on ${b.outFS}` : '',
      b.outFree ? ` · ${fmtBytes(b.outFree)} free there` : ''),
    b.exists ? h('div', { class: 'notice' }, 'A file with this name already exists. It will be replaced.') : null,
    problem ? h('div', { class: 'notice danger' }, problem) : null));
  const busy = S.status.job && S.status.job.state === 'running';
  $('#btn-convert').disabled = !!problem || busy;
  $('#bci-summary').textContent = '';
}

async function chooseBCI() {
  try { const r = await api('bci/browse', {}); if (!r.cancelled) { S.bci = r.bci; renderBCI(); } }
  catch (e) { alert(e.message); }
}
async function chooseImgPath() {
  try { const r = await api('bci/saveas', { path: S.bci.path, out: S.bci.out }); if (!r.cancelled) { S.bci = r.bci; renderBCI(); } }
  catch (e) { alert(e.message); }
}
async function startConvert() {
  const b = S.bci;
  if (b.exists && !await modal('Replace the existing file?', h('p', {}, h('b', {}, b.out), ' already exists and will be replaced.'), 'Replace')) return;
  try { await api('convert', { path: b.path, out: b.out, overwrite: b.exists }); } catch (e) { alert(e.message); }
}

async function poll() {
  try {
    const prevJob = S.status.job;
    S.status = await api('status');
    $('#ver').textContent = S.status.version ? 'version ' + S.status.version : '';
    showJob();
    if (prevJob && prevJob.state === 'running' && S.status.job && S.status.job.state !== 'running') {
      renderISO(); loadDisks(); if (S.status.job.kind === 'download') loadISOs();
      if (S.status.job.kind === 'convert' && S.bci && S.status.job.state === 'done') S.bci.exists = true;
    }
    renderGo(); renderBCI();
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
  const ok = await modal(`Erase ${diskName(d)}?`, h('div', {},
    h('p', {}, 'All files on ', h('b', {}, diskName(d)), ` (${d.model ? d.model + ', ' : ''}${fmtBytes(d.size)}`, d.letters.length ? ', drive ' + d.letters.join(', ') + ':' : '', ') will be permanently deleted, and it will be replaced with ', h('b', {}, iso.name), '.')), 'Erase and write');
  if (!ok) return;
  try { await api('write', d); } catch (e) { alert(e.message); }
};
$('#job-cancel').onclick = async () => {
  const conv = S.status.job && S.status.job.kind === 'convert';
  if (await modal('Cancel?', h('p', {}, conv ? 'The unfinished .img file is deleted. The backup is not changed.' : 'The USB stick will be left unusable until you write it again.'),
    conv ? 'Cancel converting' : 'Cancel writing')) api('cancel', {});
};
$('#btn-bci').onclick = chooseBCI;
$('#btn-convert').onclick = startConvert;
$('#job-close').onclick = () => { S.dismissed = S.status.job.id; showJob(); };

renderISO(); renderDisks();
poll(); loadISOs(); loadDisks();
