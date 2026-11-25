'use strict';

// ---------- helpers ----------
const $ = (s) => document.querySelector(s);
const PROPS = new Set(['value', 'checked', 'disabled', 'selected']);
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (PROPS.has(k)) el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}
async function api(path, body) {
  const opt = body === undefined ? {} : {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Imagen': '1' },
    body: JSON.stringify(body),
  };
  const r = await fetch('/api/' + path, opt);
  const j = await r.json().catch(() => ({ error: r.statusText }));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
function fmtBytes(n) {
  if (n == null || n < 0) return '–';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 2 : n < 100 ? 1 : 0)) + ' ' + u[i];
}
function fmtDur(s) {
  if (s == null || s < 0 || !isFinite(s)) return '–';
  s = Math.round(s);
  const hh = Math.floor(s / 3600), mm = Math.floor((s % 3600) / 60), ss = s % 60;
  return hh ? `${hh}h ${mm}m` : mm ? `${mm}m ${ss}s` : `${ss}s`;
}
const enc = encodeURIComponent;
const BAD_FS = new Set(['swap', 'lvm2', 'luks', 'bitlocker']);

// ---------- state ----------
const S = {
  sys: { rawDisk: true },
  disks: [], disksErr: null, disksLoaded: false,
  view: 'backup',
  backup: { source: null, mode: 'used', plan: null, planErr: null, analyzing: false, planKey: '',
            dir: null, name: '', nameTouched: false, level: 1, split: false, verify: true },
  restore: { image: null, info: null, infoErr: null, target: null, verifyFirst: false, fixGPT: true, zeroFill: false },
  verify: { image: null, info: null },
  accounts: { scanned: false, loading: false, list: [], err: null, folder: '', builtin: {} },
  browse: {},
  dismissed: 0,
};

function render() {
  document.querySelectorAll('#nav button').forEach((b) => b.classList.toggle('active', b.dataset.view === S.view));
  const main = $('#main');
  const scroll = window.scrollY;
  main.replaceChildren(({ backup: viewBackup, restore: viewRestore, verify: viewVerify, disks: viewDisks, accounts: viewAccounts })[S.view]());
  window.scrollTo(0, scroll);
}

async function loadDisks() {
  try {
    const d = await api('disks');
    S.disks = d.disks || [];
    S.disksErr = d.error || null;
  } catch (e) { S.disksErr = e.message; }
  S.disksLoaded = true;
  render();
}

// ---------- shared components ----------
function diskTitle(d) { return d.model || d.name; }

function diskCard(d, { selected, disabled, reason, onClick, extraTags }) {
  return h('div', { class: 'card' + (selected ? ' sel' : '') + (disabled ? ' disabled' : ''),
                    onclick: disabled ? null : onClick },
    h('div', { class: 'title' }, h('span', {}, diskTitle(d)), h('span', {}, fmtBytes(d.size))),
    h('div', { class: 'sub' }, h('span', { class: 'mono' }, d.path), d.serial ? '  ·  S/N ' + d.serial : '',
      d.transport ? h('span', { class: 'tag' }, d.transport.toUpperCase()) : null,
      d.bootMedia ? h('span', { class: 'tag orange' }, 'boot USB') : null,
      d.removable && !d.bootMedia ? h('span', { class: 'tag' }, 'removable') : null,
      extraTags || null),
    h('div', { class: 'parts' }, (d.partitions || []).map((p) =>
      h('div', {}, h('span', { class: 'mono' }, p.name), '  ', (p.fs || '?').toUpperCase(), p.label ? ` “${p.label}”` : '',
        '  ', fmtBytes(p.size), p.mountpoints && p.mountpoints.length ? h('span', { class: 'tag blue' }, 'mounted') : null))),
    reason ? h('div', { class: 'sub warn' }, reason) : null);
}

// Where can images live? Every recognized filesystem not on the excluded disk.
function volumeList(excludeDisk, onOpen) {
  const vols = [];
  for (const d of S.disks) {
    if (d.path === excludeDisk) continue;
    for (const p of d.partitions || []) {
      if (!p.fs || BAD_FS.has(p.fs)) continue;
      vols.push({ d, p });
    }
  }
  if (!S.sys.rawDisk) return h('p', { class: 'muted small' }, 'Disk listing is only available on the bootable USB (Linux). Browse folders below.');
  if (!vols.length) return h('p', { class: 'muted' }, 'No usable drives found. Plug in a USB/external drive and press Refresh.');
  return h('table', {},
    h('tr', {}, h('th', {}, 'Drive'), h('th', {}, 'Filesystem'), h('th', { class: 'r' }, 'Size'), h('th', {}, '')),
    vols.map(({ d, p }) => {
      const mp = (p.mountpoints || [])[0];
      const btn = mp
        ? h('button', { class: 'small', onclick: () => onOpen(mp) }, 'Open')
        : h('button', { class: 'small', onclick: async (ev) => {
            ev.target.disabled = true; ev.target.textContent = 'Mounting…';
            try { const r = await api('mount', { device: p.path }); await loadDisks(); onOpen(r.mountpoint); }
            catch (e) { alert(e.message); await loadDisks(); }
          } }, 'Mount');
      return h('tr', {},
        h('td', {}, h('div', {}, diskTitle(d)), h('div', { class: 'muted small mono' }, p.path + (mp ? ' → ' + mp : ''))),
        h('td', {}, (p.fs || '').toUpperCase(), p.label ? h('div', { class: 'muted small' }, p.label) : null),
        h('td', { class: 'r' }, fmtBytes(p.size)),
        h('td', { class: 'r' }, btn));
    }));
}

// File browser. mode 'dir' reports the current folder; mode 'image' reports a picked .bci file.
function fileBrowser(key, mode, onPick) {
  const st = (S.browse[key] = S.browse[key] || { path: null, data: null, err: null, loading: false });
  const box = h('div', { class: 'browser' });
  const go = async (path) => {
    st.loading = true;
    try {
      st.data = await api('browse?path=' + enc(path || ''));
      st.path = st.data.path; st.err = null;
      if (mode === 'dir') onPick(st.path);
    } catch (e) { st.err = e.message; }
    st.loading = false;
    render();
  };
  st.go = go;
  if (!st.data && !st.loading && !st.err) { go(st.path); }
  const d = st.data;
  box.append(h('div', { class: 'path' },
    h('button', { class: 'small', disabled: !d || !d.parent, onclick: () => go(d.parent) }, '↑ Up'),
    h('input', { type: 'text', class: 'p mono', value: d ? d.path : '', title: 'Type a folder and press Enter',
                 onkeydown: (e) => { if (e.key === 'Enter') go(e.target.value.trim()); } }),
    mode === 'dir' && d ? h('button', { class: 'small', onclick: async () => {
      const name = prompt('New folder name:');
      if (!name) return;
      const p = d.path.replace(/[\\/]$/, '') + d.sep + name;
      try { await api('mkdir', { path: p }); go(p); } catch (e) { alert(e.message); }
    } }, '+ New folder') : null,
    h('button', { class: 'small', onclick: () => go(st.path) }, 'Refresh')));
  const ul = h('ul');
  if (st.err) ul.append(h('li', { class: 'empty error' }, st.err));
  else if (!d) ul.append(h('li', { class: 'empty' }, h('span', { class: 'spinner' }), 'Loading…'));
  else {
    const ents = d.entries.filter((e) => e.dir || mode === 'image' || e.image);
    if (!ents.length) ul.append(h('li', { class: 'empty' }, mode === 'image' ? 'No .bci images in this folder.' : 'Empty folder.'));
    for (const e of ents) {
      const sel = mode === 'image' && S[key] && S[key].image === e.path;
      ul.append(h('li', { class: sel ? 'sel' : '', onclick: () => (e.dir ? go(e.path) : mode === 'image' ? onPick(e.path) : null) },
        h('span', {}, e.dir ? '📁' : '💾'),
        h('span', { class: 'n' }, e.name),
        h('span', { class: 'muted small' }, e.dir ? '' : fmtBytes(e.size) + '  ·  ' + e.mod)));
    }
  }
  box.append(ul);
  return box;
}

function openIn(key, path) {
  const st = (S.browse[key] = S.browse[key] || {});
  st.path = path; st.data = null; st.err = null;
  if (st.go) st.go(path); else render();
}

function step(n, title, locked, ...body) {
  return h('section', { class: 'step' + (locked ? ' locked' : '') }, h('h2', {}, h('span', { class: 'num' }, n), title), ...body);
}

function refreshBtn() {
  return h('button', { class: 'small', onclick: () => { S.disksLoaded = false; render(); loadDisks(); } }, 'Refresh');
}

function manualPath(label, value, onChange) {
  return h('div', { class: 'row', style: 'margin-top:10px' },
    h('span', { class: 'muted small' }, label),
    h('input', { type: 'text', class: 'grow', value: value || '', placeholder: '/dev/sdX or path to a disk image file',
                 onchange: (e) => onChange(e.target.value.trim()) }));
}

function diskErr() {
  if (!S.disksLoaded) return h('p', { class: 'muted' }, h('span', { class: 'spinner' }), 'Scanning disks…');
  if (S.disksErr) return h('div', { class: 'notice' }, S.disksErr);
  return null;
}

// ---------- backup ----------
function analyzeIfNeeded() {
  const b = S.backup;
  const key = b.source + '|' + b.mode;
  if (!b.source || b.planKey === key) return;
  b.planKey = key; b.plan = null; b.planErr = null; b.analyzing = true;
  api('analyze?disk=' + enc(b.source) + '&mode=' + b.mode)
    .then((r) => { if (b.planKey === key) b.plan = r.plan; })
    .catch((e) => { if (b.planKey === key) b.planErr = e.message; })
    .finally(() => { if (b.planKey === key) { b.analyzing = false; render(); } });
}

function defaultName() {
  const b = S.backup;
  const d = S.disks.find((x) => x.path === b.source);
  const base = (d ? (d.model || d.name) : (b.source || 'disk').split(/[\\/]/).pop()).replace(/[^A-Za-z0-9._-]+/g, '_');
  const t = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${base}-${t.getFullYear()}${p(t.getMonth() + 1)}${p(t.getDate())}-${p(t.getHours())}${p(t.getMinutes())}`;
}

function fsOfDir(dir) {
  let best = null;
  for (const d of S.disks) for (const p of d.partitions || []) for (const mp of p.mountpoints || []) {
    if (dir && (dir === mp || dir.startsWith(mp + '/')) && (!best || mp.length > best.mp.length)) best = { mp, p, d };
  }
  return best;
}

function planTable(plan) {
  const strat = (p) => p.strategy === 'used'
    ? h('span', { class: 'tag green' }, 'used blocks only')
    : h('span', { class: 'tag orange' }, 'every sector');
  return h('div', {},
    h('table', {},
      h('tr', {}, h('th', {}, '#'), h('th', {}, 'Partition'), h('th', {}, 'Filesystem'), h('th', { class: 'r' }, 'Size'),
        h('th', {}, 'Copy'), h('th', { class: 'r' }, 'To read')),
      plan.parts.map((p) => h('tr', {},
        h('td', {}, p.index || '–'),
        h('td', {}, p.typeName, p.name ? h('div', { class: 'muted small' }, p.name) : null),
        h('td', {}, (p.fs || '?').toUpperCase(), p.label ? h('div', { class: 'muted small' }, p.label) : null),
        h('td', { class: 'r' }, fmtBytes(p.size)),
        h('td', {}, strat(p), p.note ? h('div', { class: 'muted small' }, p.note) : null),
        h('td', { class: 'r' }, fmtBytes(p.usedBytes))))),
    h('div', { class: 'summary grid2' },
      h('div', {}, h('div', { class: 'muted small' }, 'Data to copy'), h('div', { class: 'big' }, fmtBytes(plan.dataBytes)),
        h('div', { class: 'muted small' }, 'of ' + fmtBytes(plan.diskSize) + ' disk (image is usually smaller after compression)')),
      h('div', {}, h('div', { class: 'muted small' }, 'Disk identity (kept exactly)'),
        plan.table
          ? h('div', { class: 'mono' }, plan.table.scheme.toUpperCase() + '  ' + plan.table.diskId,
              plan.table.scheme === 'gpt' ? h('div', { class: 'muted small' }, 'MBR signature ' + plan.table.mbrSignature) : null)
          : h('div', { class: 'muted' }, 'No partition table'))),
    (plan.warnings || []).map((w) => h('div', { class: 'notice' }, w)));
}

function viewBackup() {
  const b = S.backup;
  analyzeIfNeeded();
  const srcDisk = S.disks.find((d) => d.path === b.source);
  const destFS = fsOfDir(b.dir);
  const onFat = destFS && /^fat/.test(destFS.p.fs || '');
  if (!b.nameTouched && b.source) b.name = defaultName();

  const s1 = step(1, 'Choose the disk to back up', false,
    h('div', { class: 'row', style: 'margin-bottom:10px' }, h('span', { class: 'grow muted small' },
      'The whole disk is imaged: partition table, boot code and every partition.'), refreshBtn()),
    diskErr(),
    h('div', { class: 'cards' }, S.disks.map((d) => diskCard(d, {
      selected: d.path === b.source,
      disabled: d.bootMedia,
      reason: d.bootMedia ? 'This is the IMAGen USB stick' : null,
      onClick: () => { b.source = d.path; b.nameTouched = false; render(); },
    }))),
    !S.sys.rawDisk || S.disksErr ? manualPath('Source:', b.source, (v) => { b.source = v; render(); }) : null);

  const s2 = step(2, 'What to copy', !b.source,
    h('div', { class: 'cards' },
      h('div', { class: 'card' + (b.mode === 'used' ? ' sel' : ''), onclick: () => { b.mode = 'used'; render(); } },
        h('div', { class: 'title' }, 'Used data only', h('span', { class: 'tag green' }, 'recommended')),
        h('div', { class: 'sub' }, 'Skips free space on NTFS, ext2/3/4 and FAT. Other partitions, the partition table and boot areas are still copied sector by sector. Fast, small image.')),
      h('div', { class: 'card' + (b.mode === 'full' ? ' sel' : ''), onclick: () => { b.mode = 'full'; render(); } },
        h('div', { class: 'title' }, 'Every sector', h('span', { class: 'tag' }, 'forensic')),
        h('div', { class: 'sub' }, 'Exact bit-for-bit copy of the entire disk, including free space and deleted data. Slowest, largest image.'))),
    b.analyzing ? h('p', {}, h('span', { class: 'spinner' }), 'Analysing disk…') : null,
    b.planErr ? h('div', { class: 'notice danger' }, b.planErr) : null,
    b.plan ? planTable(b.plan) : null);

  const s3 = step(3, 'Where to save the image', !b.plan,
    h('div', { class: 'row', style: 'margin-bottom:6px' }, h('span', { class: 'grow muted small' },
      'Pick a drive other than the one being backed up, then a folder.'), refreshBtn()),
    volumeList(b.source, (mp) => openIn('backup', mp)),
    fileBrowser('backup', 'dir', (dir) => { if (b.dir !== dir) { b.dir = dir; render(); } }),
    h('div', { class: 'grid2', style: 'margin-top:14px' },
      h('div', {}, h('div', { class: 'muted small' }, 'Image name'),
        h('div', { class: 'row' }, h('input', { type: 'text', class: 'grow', value: b.name,
          oninput: (e) => { b.name = e.target.value; b.nameTouched = true; } }), h('span', { class: 'muted' }, '.bci'))),
      h('div', {}, h('div', { class: 'muted small' }, 'Compression'),
        h('select', { onchange: (e) => { b.level = +e.target.value; } },
          [[0, 'None (fastest, largest)'], [1, 'Fast (recommended)'], [6, 'Better (slower)']].map(([v, t]) =>
            h('option', { value: v, selected: b.level === v }, t))))),
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: b.split || onFat,
      disabled: onFat, onchange: (e) => { b.split = e.target.checked; } }),
      h('span', {}, 'Split into 4 GB files', h('div', { class: 'muted small' },
        onFat ? 'Required: the destination is FAT, which cannot hold files over 4 GB.' : 'Use when the destination is FAT32 or for easier copying.'))),
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: b.verify, onchange: (e) => { b.verify = e.target.checked; } }),
      h('span', {}, 'Verify the image after writing', h('div', { class: 'muted small' }, 'Reads the image back and checks every checksum.'))),
    h('div', { class: 'go' },
      b.dir ? h('span', { class: 'muted small mono' }, (b.dir.replace(/[\\/]$/, '') + '/' + b.name + '.bci')) : null,
      h('button', { class: 'primary', disabled: !b.dir || !b.name || !b.plan, onclick: () => startBackup(onFat) }, 'Start backup')));

  return h('div', {}, h('h1', {}, 'Back up a disk'), h('p', { class: 'lead' }, 'Create an image file you can restore to this or another disk.'), s1, s2, s3);
}

async function startBackup(onFat) {
  const b = S.backup;
  const sep = (S.browse.backup && S.browse.backup.data && S.browse.backup.data.sep) || '/';
  const dest = b.dir.replace(/[\\/]$/, '') + sep + b.name.replace(/\.bci$/i, '') + '.bci';
  const ok = await modal('Start backup?', h('div', {},
    h('p', {}, 'Source: ', h('b', {}, b.source), srcLabel(b.source)),
    h('p', {}, 'Image: ', h('span', { class: 'mono' }, dest)),
    h('p', {}, 'Mode: ', b.mode === 'used' ? 'used data only' : 'every sector', ' — about ', h('b', {}, fmtBytes(b.plan.dataBytes)), ' to read.'),
    h('p', { class: 'muted small' }, 'The source disk is only read, never written.')), { yes: 'Start backup' });
  if (!ok) return;
  try {
    await api('backup', { source: b.source, dest, mode: b.mode, level: b.level,
      splitSize: (b.split || onFat) ? 4000 * 1024 * 1024 : 0, verify: b.verify });
    pollJob();
  } catch (e) { alert(e.message); }
}

function srcLabel(path) {
  const d = S.disks.find((x) => x.path === path);
  return d ? ` (${diskTitle(d)}, ${fmtBytes(d.size)})` : '';
}

// ---------- restore ----------
function loadImageInfo(sec, path) {
  sec.image = path; sec.info = null; sec.infoErr = null;
  render();
  api('image?path=' + enc(path)).then((r) => { if (sec.image === path) sec.info = r; })
    .catch((e) => { if (sec.image === path) sec.infoErr = e.message; })
    .finally(render);
}

function imageSummary(info) {
  const m = info.meta;
  return h('div', { class: 'summary' },
    h('div', { class: 'grid2' },
      h('div', {}, h('div', { class: 'muted small' }, 'Taken from'), h('div', { class: 'big' }, m.source.model || m.source.path.split(/[\\/]/).pop()),
        h('div', { class: 'muted small' }, fmtBytes(m.source.size) + (m.source.serial ? '  ·  S/N ' + m.source.serial : '') + '  ·  ' + new Date(m.created).toLocaleString())),
      h('div', {}, h('div', { class: 'muted small' }, 'Contents'),
        h('div', {}, (m.mode === 'used' ? 'Used data only' : 'Every sector') + ' — ' + fmtBytes(m.dataBytes) + ' of disk data'),
        h('div', { class: 'muted small' }, 'Image size ' + fmtBytes(info.fileSize) + (info.files > 1 ? ` in ${info.files} files` : '')),
        m.table ? h('div', { class: 'mono small' }, m.table.scheme.toUpperCase() + ' disk ID ' + m.table.diskId) : null)),
    m.parts && m.parts.length ? h('table', { style: 'margin-top:10px' },
      m.parts.map((p) => h('tr', {}, h('td', {}, p.index || '–'), h('td', {}, p.typeName), h('td', {}, (p.fs || '?').toUpperCase() + (p.label ? ' “' + p.label + '”' : '')),
        h('td', { class: 'r' }, fmtBytes(p.size))))) : null);
}

function viewRestore() {
  const r = S.restore;
  const m = r.info && r.info.meta;
  const imgVol = r.image ? fsOfDir(r.image) : null;

  const s1 = step(1, 'Choose the image', false,
    h('div', { class: 'row', style: 'margin-bottom:6px' }, h('span', { class: 'grow muted small' }, 'Open the drive that holds your .bci image.'), refreshBtn()),
    volumeList(null, (mp) => openIn('restore', mp)),
    fileBrowser('restore', 'image', (p) => loadImageInfo(r, p)),
    r.infoErr ? h('div', { class: 'notice danger' }, r.infoErr) : null,
    r.image && !r.info && !r.infoErr ? h('p', {}, h('span', { class: 'spinner' }), 'Reading image…') : null,
    r.info ? imageSummary(r.info) : null);

  const s2 = step(2, 'Choose the target disk', !m,
    h('div', { class: 'notice danger' }, 'Everything on the target disk will be overwritten.'),
    diskErr(),
    h('div', { class: 'cards' }, S.disks.map((d) => {
      let reason = null;
      if (d.bootMedia) reason = 'This is the IMAGen USB stick';
      else if (imgVol && imgVol.d.path === d.path) reason = 'The image is stored on this disk';
      else if (m && d.size < m.source.size) reason = `Too small: needs at least ${fmtBytes(m.source.size)}`;
      const mounted = (d.partitions || []).some((p) => p.mountpoints && p.mountpoints.length);
      if (!reason && mounted) reason = 'Has mounted partitions — unmount them first';
      const same = m && m.source.serial && d.serial === m.source.serial;
      return diskCard(d, { selected: r.target === d.path, disabled: !!reason, reason,
        extraTags: same ? h('span', { class: 'tag blue' }, 'original disk') : null,
        onClick: () => { r.target = d.path; render(); } });
    })),
    !S.sys.rawDisk || S.disksErr ? manualPath('Target:', r.target, (v) => { r.target = v; render(); }) : null);

  const tgt = S.disks.find((d) => d.path === r.target);
  const larger = m && tgt && tgt.size > m.source.size;
  const s3 = step(3, 'Options', !m || !r.target,
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: r.verifyFirst, onchange: (e) => { r.verifyFirst = e.target.checked; } }),
      h('span', {}, 'Verify the image before writing', h('div', { class: 'muted small' }, 'Optional. Reads the whole image first; if it is damaged, the target is not touched.'))),
    m && m.table && m.table.scheme === 'gpt' ? h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: r.fixGPT, onchange: (e) => { r.fixGPT = e.target.checked; } }),
      h('span', {}, 'Move the GPT backup table to the end of a larger disk', h('div', { class: 'muted small' },
        (larger ? 'The target is larger than the source. ' : '') + 'Only the header location changes; disk and partition GUIDs stay identical.'))) : null,
    m && m.mode === 'used' ? h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: r.zeroFill, onchange: (e) => { r.zeroFill = e.target.checked; } }),
      h('span', {}, 'Erase free space on the target (write zeros)', h('div', { class: 'muted small' }, 'Slower. Removes leftover data from the target disk\'s previous contents.'))) : null,
    larger ? h('div', { class: 'notice info' }, `The target is ${fmtBytes(tgt.size - m.source.size)} larger. Partitions keep their original size; you can extend them later in Windows Disk Management or GParted.`) : null,
    h('div', { class: 'go' }, h('button', { class: 'primary', disabled: !m || !r.target, onclick: startRestore }, 'Restore')));

  return h('div', {}, h('h1', {}, 'Restore an image'), h('p', { class: 'lead' }, 'Write an image back to a disk. The result is a bit-exact copy of the original disk, including its disk signature and GUIDs.'), s1, s2, s3);
}

async function startRestore() {
  const r = S.restore;
  const ok = await modal('Overwrite ' + r.target + '?',
    h('p', {}, 'All data on ', h('b', {}, r.target), srcLabel(r.target), ' will be permanently replaced by the image.'),
    { yes: 'Erase and restore', danger: true });
  if (!ok) return;
  try {
    await api('restore', { image: r.image, target: r.target, fixGPT: r.fixGPT, zeroFill: r.zeroFill, verifyFirst: r.verifyFirst });
    pollJob();
  } catch (e) { alert(e.message); }
}

// ---------- verify ----------
function viewVerify() {
  const v = S.verify;
  return h('div', {}, h('h1', {}, 'Verify an image'), h('p', { class: 'lead' }, 'Read an image end to end and check every checksum, without writing anything.'),
    step(1, 'Choose the image', false,
      h('div', { class: 'row', style: 'margin-bottom:6px' }, h('span', { class: 'grow' }), refreshBtn()),
      volumeList(null, (mp) => openIn('verify', mp)),
      fileBrowser('verify', 'image', (p) => loadImageInfo(v, p)),
      v.infoErr ? h('div', { class: 'notice danger' }, v.infoErr) : null,
      v.info ? imageSummary(v.info) : null,
      h('div', { class: 'go' }, h('button', { class: 'primary', disabled: !v.info, onclick: async () => {
        try { await api('verify', { image: v.image }); pollJob(); } catch (e) { alert(e.message); }
      } }, 'Verify image'))));
}

// ---------- disks ----------
function viewDisks() {
  return h('div', {}, h('div', { class: 'row' }, h('h1', { class: 'grow' }, 'Disks'), refreshBtn()),
    h('p', { class: 'lead' }, 'All disks detected by the system.'),
    diskErr(),
    S.disks.map((d) => h('section', { class: 'step' },
      h('h2', {}, diskTitle(d), h('span', { class: 'muted' }, ' — ' + fmtBytes(d.size))),
      h('div', { class: 'muted small mono' }, d.path, '  ·  S/N ', d.serial || '–', '  ·  ', d.sectorSize, '-byte sectors', d.bootMedia ? '  ·  boot USB' : ''),
      h('table', { style: 'margin-top:10px' },
        h('tr', {}, h('th', {}, 'Partition'), h('th', {}, 'Filesystem'), h('th', {}, 'Label'), h('th', {}, 'UUID / serial'), h('th', { class: 'r' }, 'Size'), h('th', {}, 'Mounted')),
        (d.partitions || []).map((p) => h('tr', {},
          h('td', { class: 'mono' }, p.path), h('td', {}, (p.fs || '?').toUpperCase()), h('td', {}, p.label || ''),
          h('td', { class: 'mono small' }, p.uuid || ''), h('td', { class: 'r' }, fmtBytes(p.size)),
          h('td', {}, (p.mountpoints || []).map((mp) => h('div', {}, h('span', { class: 'mono small' }, mp), ' ',
            mp.startsWith('/mnt/imagen') ? h('button', { class: 'small', onclick: async () => {
              try { await api('unmount', { mountpoint: mp }); } catch (e) { alert(e.message); }
              loadDisks();
            } }, 'Unmount') : null)))))))));
}

// ---------- accounts ----------
const KIND = {
  local: ['Local account', ''],
  microsoft: ['Microsoft account', 'blue'],
  azuread: ['Work or school', 'green'],
  domain: ['Domain', 'orange'],
};

async function scanAccounts(folder) {
  const a = S.accounts;
  a.loading = true; a.err = null; render();
  try {
    a.list = (await api('accounts', folder ? { folder } : {})).installs || [];
    a.scanned = true;
  } catch (e) { a.err = e.message; }
  a.loading = false; render();
}

function fmtWhen(t) {
  if (!t) return h('span', { class: 'muted' }, 'Never');
  const d = new Date(t);
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) + ' ' +
         d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

function accountRow(x) {
  const [kind, color] = KIND[x.kind] || [x.kind, ''];
  return h('tr', { class: x.disabled ? 'dim' : '' },
    h('td', {}, h('b', {}, x.name),
      x.fullName ? h('div', { class: 'muted small' }, x.fullName) : null,
      x.email ? h('div', { class: 'muted small' }, x.email) : null),
    h('td', {}, h('span', { class: 'tag ' + color, style: 'margin-left:0' }, kind),
      x.builtin ? h('span', { class: 'tag' }, 'built-in') : null),
    h('td', {}, x.admin ? h('span', { class: 'tag red', style: 'margin-left:0' }, 'Administrator') : h('span', { class: 'muted' }, 'Standard')),
    h('td', {}, x.disabled ? h('span', { class: 'muted' }, 'Disabled') : 'Active'),
    h('td', {}, fmtWhen(x.lastLogon), x.logons ? h('div', { class: 'muted small' }, x.logons + ' sign-ins') : null),
    h('td', { class: 'mono small' }, x.profile || h('span', { class: 'muted' }, '–')));
}

function installCard(w, i) {
  const a = S.accounts;
  const where = h('div', { class: 'muted small' }, h('span', { class: 'mono' }, w.partition),
    w.diskModel ? '  ·  ' + w.diskModel : '', w.size ? '  ·  ' + fmtBytes(w.size) : '', w.label ? `  ·  “${w.label}”` : '');
  if (w.error) {
    return h('section', { class: 'step' }, h('h2', {}, 'Partition could not be read'), where, h('div', { class: 'notice' }, w.error));
  }
  const all = w.accounts || [];
  const builtin = all.filter((x) => x.builtin), users = all.filter((x) => !x.builtin);
  const shown = a.builtin[i] ? all : users;
  return h('section', { class: 'step' },
    h('h2', {}, [w.product, w.version].filter(Boolean).join(' '),
      w.computerName ? h('span', { class: 'muted', style: 'font-weight:400' }, ' — PC name ' + w.computerName) : null),
    where, w.build ? h('div', { class: 'muted small' }, 'Build ' + w.build) : null,
    (w.warnings || []).map((m) => h('div', { class: 'notice' }, m)),
    h('p', { style: 'margin:12px 0 6px' }, `${users.length} user account${users.length === 1 ? '' : 's'}`,
      users.some((x) => x.admin) ? `, ${users.filter((x) => x.admin).length} with administrator rights` : ''),
    h('table', {},
      h('tr', {}, h('th', {}, 'Account'), h('th', {}, 'Type'), h('th', {}, 'Rights'), h('th', {}, 'Status'), h('th', {}, 'Last sign-in'), h('th', {}, 'Profile folder')),
      shown.map(accountRow)),
    builtin.length ? h('div', { style: 'margin-top:8px' }, h('button', { class: 'small', onclick: () => { a.builtin[i] = !a.builtin[i]; render(); } },
      (a.builtin[i] ? 'Hide' : 'Show') + ` built-in accounts (${builtin.length})`)) : null);
}

function viewAccounts() {
  const a = S.accounts;
  if (!a.scanned && !a.loading && !a.err && S.sys.rawDisk) setTimeout(() => scanAccounts(), 0);
  const body = [];
  if (a.loading) body.push(h('p', { class: 'muted' }, h('span', { class: 'spinner' }), 'Looking for Windows on every disk…'));
  else if (a.err) body.push(h('div', { class: 'notice danger' }, a.err));
  else if (a.scanned && !a.list.length) body.push(h('div', { class: 'notice info' }, 'No Windows installation was found on any disk.'));
  else body.push(a.list.map(installCard));
  return h('div', {},
    h('div', { class: 'row' }, h('h1', { class: 'grow' }, 'Windows accounts'),
      S.sys.rawDisk ? h('button', { class: 'small', disabled: a.loading, onclick: () => scanAccounts() }, 'Scan again') : null),
    h('p', { class: 'lead' }, 'User accounts of every Windows installation on this computer’s disks. Read-only: nothing is changed and no passwords are read.'),
    !S.sys.rawDisk ? h('div', { class: 'notice' }, 'Disk scanning only works on the bootable USB. Here you can scan a folder that contains a Windows volume.') : null,
    body,
    h('details', { open: !S.sys.rawDisk, style: 'margin-top:16px' }, h('summary', {}, 'Scan a folder instead'),
      h('div', { class: 'row', style: 'margin-top:8px' },
        h('input', { type: 'text', class: 'grow', value: a.folder, placeholder: 'Folder that contains Windows\\System32\\config',
                     oninput: (e) => { a.folder = e.target.value.trim(); },
                     onkeydown: (e) => { if (e.key === 'Enter' && a.folder) scanAccounts(a.folder); } }),
        h('button', { class: 'primary', onclick: () => a.folder && scanAccounts(a.folder) }, 'Scan'))));
}

// ---------- modal ----------
function modal(title, body, { yes = 'OK', danger = false } = {}) {
  return new Promise((resolve) => {
    $('#modal-title').textContent = title;
    $('#modal-body').replaceChildren(body);
    const yesBtn = $('#modal-yes');
    yesBtn.textContent = yes;
    yesBtn.className = danger ? 'danger' : 'primary';
    yesBtn.disabled = false;
    $('#modal').classList.remove('hidden');
    const done = (v) => { $('#modal').classList.add('hidden'); yesBtn.onclick = null; $('#modal-no').onclick = null; resolve(v); };
    yesBtn.onclick = () => done(true);
    $('#modal-no').onclick = () => done(false);
  });
}

// ---------- job overlay ----------
let pollTimer = null;
function pollJob() {
  clearTimeout(pollTimer);
  api('job').then(showJob).catch(() => {}).finally(() => { pollTimer = setTimeout(pollJob, 1000); });
}

function showJob(r) {
  const j = r.job;
  const ov = $('#job');
  if (!j || (j.state !== 'running' && j.id === S.dismissed)) { ov.classList.add('hidden'); return; }
  ov.classList.remove('hidden');
  const p = r.progress;
  const names = { backup: 'Backing up', restore: 'Restoring', verify: 'Verifying' };
  const doneNames = { backup: 'Backup complete', restore: 'Restore complete', verify: 'Image verified' };
  $('#job-title').textContent = j.state === 'done' ? doneNames[j.kind] : j.state === 'running' ? names[j.kind] + '…' : j.summary;
  const st = $('#job-state');
  st.textContent = j.state; st.className = 'pill ' + j.state;
  $('#job-phase').textContent = p.phase;
  const pct = j.state === 'done' ? 100 : p.percent;
  $('#job-bar').style.width = pct.toFixed(1) + '%';
  $('#job-pct').textContent = pct.toFixed(1) + '%';
  $('#job-done').textContent = fmtBytes(p.done) + ' / ' + fmtBytes(p.total);
  $('#job-rate').textContent = p.rate ? fmtBytes(p.rate) + '/s' : '–';
  $('#job-eta').textContent = j.state === 'running' ? fmtDur(p.eta) : fmtDur((new Date(j.ended) - new Date(j.started)) / 1000) + ' total';
  $('#job-eta').previousElementSibling.textContent = j.state === 'running' ? 'Time left' : 'Duration';
  const err = $('#job-error');
  err.textContent = j.error || '';
  err.classList.toggle('hidden', !j.error);
  const log = $('#job-log');
  const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 10;
  log.textContent = p.log.join('\n');
  if (atBottom) log.scrollTop = log.scrollHeight;
  $('#job-cancel').classList.toggle('hidden', j.state !== 'running');
  $('#job-close').classList.toggle('hidden', j.state === 'running');
  $('#job-close').onclick = () => { S.dismissed = j.id; ov.classList.add('hidden'); loadDisks(); Object.values(S.browse).forEach((b) => { b.data = null; }); render(); };
}

$('#job-cancel').onclick = async () => {
  if (await modal('Cancel the running job?', h('p', {}, 'A cancelled restore leaves the target disk incomplete and unusable until you restore again.'), { yes: 'Cancel job', danger: true })) {
    api('cancel', {}).catch(() => {});
  }
};

// ---------- init ----------
document.querySelectorAll('#nav button').forEach((b) => b.addEventListener('click', () => { S.view = b.dataset.view; render(); }));
for (const [id, action, label] of [['#btn-reboot', 'reboot', 'Restart'], ['#btn-poweroff', 'poweroff', 'Shut down']]) {
  $(id).onclick = async () => {
    if (await modal(label + ' the computer?', h('p', {}, 'Remove the IMAGen USB stick afterwards if you want to boot the restored disk.'), { yes: label })) {
      api('power', { action }).catch((e) => alert(e.message));
    }
  };
}
api('system').then((s) => { S.sys = s; render(); }).catch(() => {});
render();
loadDisks();
pollJob();
