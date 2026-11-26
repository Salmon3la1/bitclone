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
  space: {}, spaceLoaded: false, images: null, imagesLoading: false,
  view: 'backup',
  backup: { source: null, mode: 'used', plan: null, planErr: null, analyzing: false, planKey: '',
            dir: null, name: '', nameTouched: false, level: 1, split: false, verify: true, vol: null, dirNote: '', browsing: false },
  restore: { image: null, info: null, infoErr: null, target: null, pick: null, browsing: false, verifyFirst: false, fixGPT: true, zeroFill: false },
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
  if (S.sys.rawDisk) loadSpace();
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

// ---------- drives and folders ----------
const FOLDER = 'IMAGen'; // default folder for images, at the top of a drive
const FS_NAMES = { ntfs: 'NTFS', exfat: 'exFAT', fat12: 'FAT', fat16: 'FAT', fat32: 'FAT32', fat: 'FAT', ext2: 'ext2', ext3: 'ext3',
  ext4: 'ext4', ext: 'ext', xfs: 'XFS', btrfs: 'Btrfs', bitlocker: 'BitLocker', swap: 'swap', luks: 'LUKS', lvm2: 'LVM' };
function fsName(fs) { return FS_NAMES[fs] || fs || 'unknown'; }
const FOLDER_ICON = 'data:image/svg+xml,' + enc('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#4c9aff" stroke-width="1.8" stroke-linejoin="round"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4.6l2 2.2h8.4A1.5 1.5 0 0 1 21 8.7v9.8a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18.5z"/></svg>');

function partOf(path) {
  for (const d of S.disks) for (const p of d.partitions || []) if (p.path === path) return { d, p };
  return null;
}
// The name people know a drive by: its label, else the disk's make and model.
function volName(d, p) {
  if (p.label) return p.label;
  const n = (d.partitions || []).filter((x) => x.fs && !BAD_FS.has(x.fs)).length;
  return (d.model || d.name) + (n > 1 ? ` (partition ${p.number})` : '');
}
function visibleMount(p) { return (p.mountpoints || []).find((mp) => mp !== '[swap]' && !mp.includes('/.ro-')); }
function spaceOf(p) {
  const s = S.space[p.path];
  if (!s || s.total <= 0 || s.used < 0) return null;
  return { used: s.used, total: s.total, free: Math.max(0, s.total - s.used) };
}
// "Windows (NTFS, 180 GB used), Recovery (NTFS)" — what a disk holds now.
function holds(d) {
  const parts = (d.partitions || []).filter((p) => p.fs || p.size >= 1e9);
  if (!parts.length) return 'nothing (no partitions)';
  return parts.map((p) => {
    const sp = spaceOf(p);
    const name = p.label || (p.fs ? fsName(p.fs) + ' partition' : 'Unknown partition');
    const detail = [p.label && p.fs ? fsName(p.fs) : null, sp ? fmtBytes(sp.used) + ' used' : null].filter(Boolean).join(', ');
    return detail ? `${name} (${detail})` : name;
  }).join(', ');
}

async function loadSpace() {
  try { S.space = (await api('space')).space || {}; } catch (e) { /* free space stays unknown */ }
  S.spaceLoaded = true;
  render();
}

// Looks for drives, free space and images again.
function rescan() {
  S.disksLoaded = false; S.images = null; S.spaceLoaded = false;
  Object.values(S.browse).forEach((b) => { b.data = null; });
  render(); loadDisks();
}
function rescanBtn() { return h('button', { class: 'small', onclick: rescan }, 'Look for drives again'); }

// Mounts a partition if needed and returns where its files are.
async function openVolume(p, readOnlyOK) {
  const mp = visibleMount(p);
  if (mp) return mp;
  const r = await api('mount', { device: p.path, readOnlyOK: !!readOnlyOK });
  await loadDisks();
  return r.mountpoint;
}

// Partitions an image could be saved on or read from, and what was left out.
function driveVolumes(excludeDisk) {
  const vols = [], skipped = [];
  for (const d of S.disks) {
    if (d.bootMedia) { skipped.push(`${d.model || d.name} (this USB stick)`); continue; }
    if (d.path === excludeDisk) { skipped.push(`${d.model || d.name} (the disk being backed up)`); continue; }
    for (const p of d.partitions || []) {
      if (p.fs === 'bitlocker') { skipped.push(`${volName(d, p)} (locked with BitLocker)`); continue; }
      if (!p.fs || BAD_FS.has(p.fs) || p.size < 1e9) continue;
      vols.push({ d, p });
    }
  }
  return { vols, skipped };
}

// Drive cards with free space. need: bytes a backup needs (0: just browsing).
function driveCards(vols, selected, need, onPick) {
  return h('div', { class: 'cards' }, vols.map(({ d, p }) => {
    const sp = spaceOf(p);
    let fit = null;
    if (sp && need) {
      fit = sp.free >= need ? h('span', { class: 'ok' }, 'Fits')
        : sp.free >= need / 2 ? h('span', { class: 'warn' }, 'May not fit')
          : h('span', { class: 'error' }, 'Too small');
    }
    const sel = selected === p.path;
    return h('div', { class: 'card' + (sel ? ' sel' : ''), onclick: () => onPick(d, p) },
      h('div', { class: 'title' }, h('span', {}, volName(d, p)), sel ? h('span', { class: 'ok' }, '✓') : null),
      h('div', { class: 'sub' }, [p.label ? d.model || d.name : null, d.transport ? d.transport.toUpperCase() : null, fsName(p.fs)].filter(Boolean).join(' · ')),
      sp ? h('div', { class: 'space' }, h('div', { style: `width:${Math.min(100, Math.round((sp.used / sp.total) * 100))}%` })) : null,
      h('div', { class: 'sub' }, fit, fit ? ' · ' : '',
        sp ? `${fmtBytes(sp.free)} free of ${fmtBytes(sp.total)}`
          : `${fmtBytes(p.size)} · ` + (S.spaceLoaded ? 'free space shown once chosen' : 'measuring free space…')));
  }));
}

// "Backup drive › IMAGen › Laptop" for a folder inside a drive.
function crumbText(root, rootName, dir) {
  const rel = dir && dir.startsWith(root) ? dir.slice(root.length).split(/[\\/]/).filter(Boolean) : [];
  return [rootName, ...rel].join(' › ');
}

// Folder browser inside one drive (root, shown as rootName). mode 'dir':
// "Use this folder" reports the folder; mode 'image': clicking an image
// reports it. Without a root (not on the boot USB) it shows full paths.
function fileBrowser(key, mode, root, rootName, onPick) {
  const st = (S.browse[key] = S.browse[key] || { path: null, data: null, err: null, loading: false });
  const go = async (path) => {
    st.loading = true; st.path = path;
    try { st.data = await api('browse?path=' + enc(path || '')); st.path = st.data.path; st.err = null; }
    catch (e) { st.err = e.message; }
    st.loading = false;
    render();
  };
  st.go = go;
  if (!st.data && !st.loading && !st.err) go(st.path || root || '');
  const d = st.data;
  const sep = (d && d.sep) || '/';
  const crumbs = [];
  if (root) {
    crumbs.push(h('button', { class: 'crumb', onclick: () => go(root) }, rootName));
    const rel = d && d.path.startsWith(root) ? d.path.slice(root.length).split(/[\\/]/).filter(Boolean) : [];
    rel.forEach((name, i) => {
      const target = root.replace(/[\\/]$/, '') + sep + rel.slice(0, i + 1).join(sep);
      crumbs.push(h('span', { class: 'muted' }, '›'), h('button', { class: 'crumb', onclick: () => go(target) }, name));
    });
  } else {
    crumbs.push(h('button', { class: 'small', disabled: !d || !d.parent, onclick: () => go(d.parent) }, '↑ Up'),
      h('span', { class: 'mono' }, d ? d.path : ''));
  }
  const head = h('div', { class: 'path' }, h('div', { class: 'crumbs' }, crumbs),
    mode === 'dir' && d ? h('button', { class: 'small', onclick: async () => {
      const name = (prompt('New folder name:') || '').trim();
      if (!name) return;
      const p = d.path.replace(/[\\/]$/, '') + sep + name;
      try { await api('mkdir', { path: p }); go(p); } catch (e) { alert(e.message); }
    } }, '+ New folder') : null,
    mode === 'dir' && d ? h('button', { class: 'small primary', onclick: () => onPick(d.path) }, 'Use this folder') : null);
  const ul = h('ul');
  if (st.err) ul.append(h('li', { class: 'empty error' }, st.err));
  else if (!d) ul.append(h('li', { class: 'empty' }, h('span', { class: 'spinner' }), 'Loading…'));
  else {
    if (!d.entries.length) ul.append(h('li', { class: 'empty' }, mode === 'image' ? 'No images in this folder.' : 'Empty folder.'));
    for (const e of d.entries) {
      const sel = mode === 'image' && S[key] && S[key].image === e.path;
      const click = e.dir ? () => go(e.path) : mode === 'image' ? () => onPick(e.path) : null;
      ul.append(h('li', { class: (sel ? 'sel' : '') + (click ? '' : ' info'), onclick: click },
        e.dir ? h('img', { class: 'icon', src: FOLDER_ICON, alt: '' }) : h('span', { class: 'icon tag' }, 'img'),
        h('span', { class: 'n' }, e.name),
        h('span', { class: 'muted small' }, e.dir ? '' : fmtBytes(e.size) + '  ·  ' + e.mod)));
    }
  }
  return h('div', { class: 'browser' }, head, ul);
}

function openIn(key, path) {
  const st = (S.browse[key] = S.browse[key] || {});
  st.path = path; st.data = null; st.err = null;
  if (st.go) st.go(path); else render();
}

function step(n, title, locked, ...body) {
  return h('section', { class: 'step' + (locked ? ' locked' : '') }, h('h2', {}, h('span', { class: 'num' }, n), title), ...body);
}

function refreshBtn() { return h('button', { class: 'small', onclick: rescan }, 'Refresh'); }

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

  const s3 = step(3, 'Where to save the image', !b.plan, ...destSection(onFat));

  return h('div', {}, h('h1', {}, 'Back up a disk'), h('p', { class: 'lead' }, 'Create an image file you can restore to this or another disk.'), s1, s2, s3);
}

const LEVELS = [[0, 'None (fastest, largest)', 'no compression'], [1, 'Fast (recommended)', 'fast compression'], [6, 'Better (slower)', 'better compression']];

function destSection(onFat) {
  const b = S.backup;
  const need = b.plan ? b.plan.dataBytes : 0;
  const out = [];
  let sel = null, root = null, rootName = '';
  if (S.sys.rawDisk) {
    const { vols, skipped } = driveVolumes(b.source);
    sel = b.vol ? partOf(b.vol) : null;
    if (b.vol && (!sel || sel.d.path === b.source)) { b.vol = null; b.dir = null; sel = null; } // unplugged, or now the source
    root = sel ? visibleMount(sel.p) : null;
    rootName = sel ? volName(sel.d, sel.p) : '';
    out.push(h('div', { class: 'row', style: 'margin-bottom:10px' },
      h('span', { class: 'grow muted small' }, need
        ? `The image needs up to ${fmtBytes(need)} (usually less after compression). Choose a drive with enough free space.`
        : 'Choose the drive to save the image on.'),
      rescanBtn()));
    out.push(diskErr());
    if (S.disksLoaded && !vols.length) out.push(h('p', { class: 'muted' }, 'No drive to save on. Plug in a USB or external drive, then press "Look for drives again".'));
    else out.push(driveCards(vols, b.vol, need, chooseDest));
    if (skipped.length) out.push(h('p', { class: 'muted small' }, 'Not listed: ' + skipped.join(', ')));
    if (b.volErr) out.push(h('div', { class: 'notice danger' }, b.volErr));
    if (b.vol && !b.dir) out.push(h('p', { class: 'muted' }, h('span', { class: 'spinner' }), 'Opening the drive…'));
  } else {
    // Not on the boot USB: any folder of this computer.
    out.push(h('p', { class: 'muted small' }, 'Choose the folder to save the image in.'));
    if (!b.dir) out.push(fileBrowser('backup', 'dir', null, '', (dir) => setDestDir(dir)));
  }

  if (b.dir && (root || !S.sys.rawDisk)) {
    const where = root ? crumbText(root, rootName, b.dir) : b.dir;
    out.push(h('div', { class: 'folder' },
      h('img', { class: 'icon big', src: FOLDER_ICON, alt: '' }),
      h('div', { class: 'grow' }, h('div', { class: 'muted small' }, 'Folder'), h('div', { class: 'fname' }, where),
        h('div', { class: 'muted small' }, b.dirNote || '')),
      h('button', { class: 'small', onclick: () => {
        b.browsing = !b.browsing;
        if (b.browsing) openIn('backup', b.dirExists ? b.dir : root || b.dir); else render();
      } }, b.browsing ? 'Cancel' : 'Change folder')));
    if (b.browsing) out.push(fileBrowser('backup', 'dir', root, rootName, (dir) => { b.browsing = false; setDestDir(dir); }));
  }

  out.push(h('div', { style: 'margin-top:14px' }, h('div', { class: 'muted small' }, 'Image name'),
    h('div', { class: 'row' }, h('input', { type: 'text', id: 'imgname', class: 'grow', value: b.name,
      oninput: (e) => { b.name = e.target.value; b.nameTouched = true; renderSoon(); } }), h('span', { class: 'muted' }, '.bci'))));

  const split = b.split || onFat;
  const optText = [LEVELS.find((l) => l[0] === b.level)[2], split ? 'split into 4 GB files' : null,
    b.verify ? 'verify after writing' : 'no check after writing'].filter(Boolean).join(', ');
  out.push(h('details', { class: 'opts', open: b.optsOpen, ontoggle: (e) => { b.optsOpen = e.target.open; } },
    h('summary', {}, 'Options: ' + optText),
    h('div', { style: 'max-width:360px' }, h('div', { class: 'muted small' }, 'Compression'),
      h('select', { onchange: (e) => { b.level = +e.target.value; render(); } },
        LEVELS.map(([v, t]) => h('option', { value: v, selected: b.level === v }, t)))),
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: split, disabled: onFat, onchange: (e) => { b.split = e.target.checked; render(); } }),
      h('span', {}, 'Split into 4 GB files', h('div', { class: 'muted small' },
        onFat ? 'Required: this drive is FAT, which cannot hold files over 4 GB.' : 'Use when the image will be copied to a FAT32 drive.'))),
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: b.verify, onchange: (e) => { b.verify = e.target.checked; render(); } }),
      h('span', {}, 'Verify the image after writing', h('div', { class: 'muted small' }, 'Reads the image back and checks every checksum.')))));

  const sp = sel ? spaceOf(sel.p) : null;
  const left = sp && need ? sp.free - need : null;
  const ready = b.dir && b.name.trim() && b.plan;
  out.push(h('div', { class: 'go' },
    b.dir ? h('div', { class: 'grow' },
      h('div', {}, 'Saves to ', h('b', {}, (root ? crumbText(root, rootName, b.dir) : b.dir) + ' › ' + b.name.trim().replace(/\.bci$/i, '') + '.bci')),
      need ? h('div', { class: 'muted small' }, `Up to ${fmtBytes(need)}` +
        (left == null ? '' : left >= 0 ? ` · about ${fmtBytes(left)} will be left free` : ' · may not fit on this drive')) : null)
      : h('div', { class: 'grow muted small' }, S.sys.rawDisk ? 'Choose a drive to save on.' : 'Choose a folder to save in.'),
    h('button', { class: 'primary', disabled: !ready, onclick: () => startBackup(onFat) }, 'Start backup')));
  return out;
}

// Typing the name updates the summary line without losing focus.
let renderTimer = 0;
function renderSoon() {
  clearTimeout(renderTimer);
  renderTimer = setTimeout(() => {
    const el = document.activeElement, pos = el && el.selectionStart;
    render();
    const again = document.getElementById('imgname');
    if (el && el.id === 'imgname' && again) { again.focus(); again.setSelectionRange(pos, pos); }
  }, 250);
}

async function chooseDest(d, p) {
  const b = S.backup;
  b.vol = p.path; b.volErr = null; b.browsing = false; b.dir = null;
  render();
  try {
    const mp = await openVolume(p, false);
    loadSpace();
    await setDestDir(mp.replace(/\/$/, '') + '/' + FOLDER);
  } catch (e) {
    b.vol = null;
    b.volErr = `Could not open ${volName(d, p)}: ${e.message}`;
    render();
  }
}

// Sets the destination folder and notes whether it exists yet.
async function setDestDir(dir) {
  const b = S.backup;
  b.dir = dir; b.dirNote = ''; b.dirExists = false;
  render();
  try {
    const r = await api('browse?path=' + enc(dir));
    const n = r.entries.filter((e) => e.image).length;
    b.dirExists = true;
    b.dirNote = n ? `${n} earlier image${n > 1 ? 's' : ''} here` : 'Empty folder';
  } catch (e) { b.dirNote = 'Created when the backup starts'; }
  if (b.dir === dir) render();
}

async function startBackup(onFat) {
  const b = S.backup;
  const sep = b.dir.includes('\\') && !b.dir.includes('/') ? '\\' : '/';
  const dest = b.dir.replace(/[\\/]$/, '') + sep + b.name.trim().replace(/\.bci$/i, '') + '.bci';
  const sel = b.vol ? partOf(b.vol) : null;
  const root = sel ? visibleMount(sel.p) : null;
  const where = root ? crumbText(root, volName(sel.d, sel.p), b.dir) : b.dir;
  const ok = await modal('Start backup?', h('div', {},
    h('p', {}, 'Disk: ', h('b', {}, srcName(b.source))),
    h('p', {}, 'Saves to: ', h('b', {}, where + ' › ' + dest.split(/[\\/]/).pop())),
    h('p', {}, b.mode === 'used' ? 'Used data only' : 'Every sector', ', about ', h('b', {}, fmtBytes(b.plan.dataBytes)), ' to read.'),
    h('p', { class: 'muted small' }, 'The disk is only read, never written.')), { yes: 'Start backup' });
  if (!ok) return;
  try {
    await api('backup', { source: b.source, dest, mode: b.mode, level: b.level,
      splitSize: (b.split || onFat) ? 4000 * 1024 * 1024 : 0, verify: b.verify });
    pollJob();
  } catch (e) { alert(e.message); }
}

function srcName(path) {
  const d = S.disks.find((x) => x.path === path);
  return d ? `${diskTitle(d)} (${fmtBytes(d.size)})` : path;
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

// ---------- choosing an image (restore and verify) ----------
async function loadImages() {
  S.imagesLoading = true;
  try { S.images = (await api('images')).images || []; } catch (e) { S.images = []; }
  S.imagesLoading = false;
  render();
}
function needImages() {
  if (S.sys.rawDisk && S.disksLoaded && S.images == null && !S.imagesLoading) { S.imagesLoading = true; setTimeout(loadImages); }
}
function imgDate(m) { return new Date(m.created).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }); }
function imgTitle(m) { return `${m.source.model || m.source.path.split(/[\\/]/).pop()} · ${imgDate(m)}`; }
function imgWhere(img) {
  const pd = partOf(img.device);
  const drive = pd ? volName(pd.d, pd.p) : img.label || img.disk || img.device;
  return [drive, ...img.rel.split('/').slice(0, -1)].join(' › ');
}

async function pickFound(sec, img) {
  sec.pick = img.device + '|' + img.rel; sec.infoErr = null;
  render();
  try {
    let path = img.path;
    if (!path) {
      const pd = partOf(img.device);
      const mp = await openVolume(pd ? pd.p : { path: img.device }, true);
      path = mp.replace(/\/$/, '') + '/' + img.rel;
    }
    loadImageInfo(sec, path);
  } catch (e) { sec.infoErr = 'Could not open the drive: ' + e.message; render(); }
}

// Step body: images found on the drives, or browse for one.
function imageChooser(sec, key) {
  const out = [];
  if (S.sys.rawDisk) {
    needImages();
    out.push(h('div', { class: 'row', style: 'margin-bottom:8px' },
      h('span', { class: 'grow muted small' }, `Images found on your drives, in ${FOLDER} folders and at the top of each drive.`), rescanBtn()));
    if (S.images == null) out.push(h('p', { class: 'muted' }, h('span', { class: 'spinner' }), 'Looking for images…'));
    else if (!S.images.length) out.push(h('p', { class: 'muted' }, 'No images found. Plug in the drive that holds your image and press "Look for drives again", or browse for it.'));
    else {
      out.push(h('div', { class: 'imglist' }, S.images.map((img) => {
        const m = img.meta;
        const sel = sec.pick === img.device + '|' + img.rel;
        return h('div', { class: 'imgrow' + (sel ? ' sel' : ''), onclick: () => pickFound(sec, img) },
          h('div', { class: 'grow' }, h('div', { class: 'title' }, imgTitle(m)),
            h('div', { class: 'muted small' }, `${imgWhere(img)} · ${fmtBytes(img.size)} image of a ${fmtBytes(m.source.size)} disk · ${m.mode === 'used' ? 'used data only' : 'every sector'}`)),
          sel ? h('span', { class: 'ok' }, '✓') : null);
      })));
    }
    out.push(h('button', { class: 'small', style: 'margin-top:10px', onclick: () => { sec.browsing = !sec.browsing; render(); } },
      sec.browsing ? 'Hide' : 'Browse for another image'));
    if (sec.browsing) {
      const { vols } = driveVolumes(null);
      out.push(h('p', { class: 'muted small' }, 'Choose the drive that holds the image, then the image.'));
      out.push(driveCards(vols, sec.browseVol, 0, async (d, p) => {
        sec.browseVol = p.path; sec.browseErr = null; sec.browseRoot = null;
        render();
        try {
          const mp = await openVolume(p, true);
          sec.browseRoot = mp; sec.browseName = volName(d, p);
          const st = S.browse[key];
          if (st) { st.data = null; st.err = null; st.path = null; }
          render();
        } catch (e) { sec.browseErr = `Could not open ${volName(d, p)}: ${e.message}`; render(); }
      }));
      if (sec.browseErr) out.push(h('div', { class: 'notice danger' }, sec.browseErr));
      if (sec.browseRoot) out.push(fileBrowser(key, 'image', sec.browseRoot, sec.browseName, (p) => { sec.pick = null; loadImageInfo(sec, p); }));
    }
  } else {
    out.push(h('p', { class: 'muted small' }, 'Open the folder that holds your .bci image.'));
    out.push(fileBrowser(key, 'image', null, '', (p) => loadImageInfo(sec, p)));
  }
  if (sec.infoErr) out.push(h('div', { class: 'notice danger' }, sec.infoErr));
  if ((sec.image || sec.pick) && !sec.info && !sec.infoErr) out.push(h('p', {}, h('span', { class: 'spinner' }), 'Reading image…'));
  if (sec.info) out.push(imageSummary(sec.info));
  return out;
}

// ---------- restore ----------
function viewRestore() {
  const r = S.restore;
  const m = r.info && r.info.meta;
  const imgVol = r.image ? fsOfDir(r.image) : null;
  const imgDisk = imgVol ? imgVol.d.path : r.pick ? (partOf(r.pick.split('|')[0]) || { d: {} }).d.path : null;

  const s1 = step(1, 'Choose the image', false, ...imageChooser(r, 'restore'));

  const skipped = [];
  const cards = [];
  for (const d of S.disks) {
    if (d.bootMedia) { skipped.push(`${diskTitle(d)} (this USB stick)`); continue; }
    if (d.path === imgDisk) { skipped.push(`${diskTitle(d)} (holds the image)`); continue; }
    const small = m && d.size < m.source.size;
    const same = m && m.source.serial && d.serial === m.source.serial;
    const larger = m && d.size > m.source.size;
    const sel = r.target === d.path;
    if (sel && small) r.target = null;
    cards.push(h('div', { class: 'card' + (sel ? ' sel danger' : '') + (small ? ' disabled' : ''),
      onclick: small ? null : () => { r.target = d.path; render(); } },
      h('div', { class: 'title' }, h('span', {}, diskTitle(d)), h('span', {}, fmtBytes(d.size))),
      same ? h('div', { style: 'margin-top:6px' }, h('span', { class: 'tag blue', style: 'margin:0' }, 'The disk this image was taken from')) : null,
      h('div', { class: 'sub' }, 'Now holds: ' + holds(d)),
      larger ? h('div', { class: 'sub' }, `Larger than the original; the extra ${fmtBytes(d.size - m.source.size)} stays unused`) : null,
      small ? h('div', { class: 'sub warn' }, `Too small: needs at least ${fmtBytes(m.source.size)}`) : null));
  }
  if (r.target && !S.disks.some((d) => d.path === r.target && d.path !== imgDisk && !d.bootMedia)) r.target = null;
  const s2 = step(2, 'Choose the disk to overwrite', !m,
    diskErr(),
    h('div', { class: 'cards' }, cards),
    skipped.length ? h('p', { class: 'muted small' }, 'Not listed: ' + skipped.join(', ')) : null,
    !S.sys.rawDisk || S.disksErr ? manualPath('Target:', r.target, (v) => { r.target = v; render(); }) : null);

  const tgt = S.disks.find((d) => d.path === r.target);
  const larger = m && tgt && tgt.size > m.source.size;
  const optText = [r.verifyFirst ? 'check the image first' : 'no check first', r.zeroFill ? 'erase leftover data' : 'keep leftover data'].filter(Boolean).join(', ');
  const s3 = step(3, 'Check and restore', !m || !r.target,
    m && r.target ? h('div', { class: 'fromto' },
      h('div', { class: 'box' }, h('div', { class: 'muted small' }, 'Image'), h('div', {}, imgTitle(m)),
        h('div', { class: 'muted small' }, fmtBytes(m.dataBytes) + ' of disk data')),
      h('div', { class: 'arrow' }, '→'),
      h('div', { class: 'box danger' }, h('div', { class: 'small' }, 'Will be erased'),
        h('div', {}, tgt ? `${diskTitle(tgt)}, ${fmtBytes(tgt.size)}` : r.target),
        tgt ? h('div', { class: 'small' }, holds(tgt)) : null)) : null,
    h('details', { class: 'opts', open: r.optsOpen, ontoggle: (e) => { r.optsOpen = e.target.open; } },
      h('summary', {}, 'Options: ' + optText),
      h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: r.verifyFirst, onchange: (e) => { r.verifyFirst = e.target.checked; render(); } }),
        h('span', {}, 'Check the image first', h('div', { class: 'muted small' },
          'Optional. Reads the whole image before writing; if it is damaged, the disk is not touched. Without it, a damaged image stops the restore partway.'))),
      m && m.mode === 'used' ? h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: r.zeroFill, onchange: (e) => { r.zeroFill = e.target.checked; render(); } }),
        h('span', {}, 'Erase leftover data', h('div', { class: 'muted small' }, 'Slower. Overwrites the free space with zeros, so nothing of the disk\'s previous contents remains.'))) : null),
    larger ? h('div', { class: 'notice info' }, `The disk is ${fmtBytes(tgt.size - m.source.size)} larger than the original. Partitions keep their size; you can extend them later in Windows Disk Management or GParted.`) : null,
    h('div', { class: 'go' }, h('button', { class: 'danger', disabled: !m || !r.target, onclick: startRestore }, 'Erase and restore')));

  return h('div', {}, h('h1', {}, 'Restore an image'), h('p', { class: 'lead' }, 'Write an image back to a disk. The disk becomes an exact copy of the one the image was taken from.'), s1, s2, s3);
}

async function startRestore() {
  const r = S.restore;
  const m = r.info.meta;
  const tgt = S.disks.find((d) => d.path === r.target);
  const name = tgt ? `${diskTitle(tgt)} (${fmtBytes(tgt.size)})` : r.target;
  const ok = await modal(`Erase ${tgt ? diskTitle(tgt) : r.target}?`, h('div', {},
    h('p', {}, 'Everything on ', h('b', {}, name), ' will be permanently replaced by the image ', h('b', {}, imgTitle(m)), '.'),
    tgt ? h('p', {}, 'It now holds: ' + holds(tgt) + '.') : null,
    h('p', { class: 'muted small' }, 'This cannot be undone.')),
    { yes: 'Erase and restore', danger: true });
  if (!ok) return;
  try {
    await api('restore', { image: r.image, target: r.target, fixGPT: true, zeroFill: r.zeroFill, verifyFirst: r.verifyFirst });
    pollJob();
  } catch (e) { alert(e.message); }
}

// ---------- verify ----------
function viewVerify() {
  const v = S.verify;
  return h('div', {}, h('h1', {}, 'Verify an image'), h('p', { class: 'lead' }, 'Read an image end to end and check every checksum, without writing anything.'),
    step(1, 'Choose the image', false, ...imageChooser(v, 'verify'),
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
  $('#job-close').onclick = () => { S.dismissed = j.id; ov.classList.add('hidden'); rescan(); };
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
      try {
        await api('power', { action });
        clearTimeout(pollTimer); pollJob = () => {};
        document.body.append(h('div', { class: 'overlay' }, h('div', { class: 'jobcard', style: 'text-align:center' },
          h('h2', {}, action === 'reboot' ? 'Restarting…' : 'Turning off…'),
          h('p', { class: 'muted' }, action === 'reboot'
            ? 'Remove the IMAGen USB stick now if you want the PC to start from its own disk.'
            : 'You can remove the IMAGen USB stick once the screen goes dark.'))));
      } catch (e) { alert(e.message); }
    }
  };
}
api('system').then((s) => { S.sys = s; $('#ram-note').classList.toggle('hidden', !s.inRAM); render(); }).catch(() => {});
render();
loadDisks();
pollJob();
