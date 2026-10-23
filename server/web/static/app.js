import { World, decodeFrame, SCATTERED, baseName, polityTitle } from './data.js';
import { api, ensureKey, getKey } from './api.js';
import { MapView, buildLayers, GROUP_ORDER } from './map.js';
import { lutCss, polityColor } from './colors.js';
import { t, tr, EN, mountLang, fmtNum as fmt, fmtPop } from './i18n.js';

const $ = (id) => document.getElementById(id);
const fmtYear = (y) => t('第 {0} 年', y);
const paren = (s) => (EN ? ` (${s})` : `（${s}）`);

const S = {
  w: null, map: null, layers: [], layer: null,
  run: null, meta: null, metrics: [], polReg: new Map(),
  frames: new Map(), frame: null, year: 0, maxYear: 0,
  playing: false, timer: null,
  ws: null, req: 1, pending: new Map(),
  hover: -1, mouse: [0, 0], running: null,
  inspectCell: -1,
};

// ---------------- WebSocket ----------------
function connect() {
  return new Promise((resolve) => {
    const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws?key=${encodeURIComponent(getKey())}`);
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => { S.ws = ws; resolve(); };
    ws.onclose = () => { S.ws = null; setStatus(t('连接断开，3 秒后重连…')); setTimeout(connect, 3000); };
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        const dv = new DataView(ev.data);
        const req = dv.getUint32(0, true);
        const p = S.pending.get(req);
        if (p) { S.pending.delete(req); p.resolve(ev.data.slice(8)); }
        return;
      }
      const m = JSON.parse(ev.data);
      if (m.req && S.pending.has(m.req)) {
        const p = S.pending.get(m.req);
        S.pending.delete(m.req);
        if (m.type === 'error') p.reject(new Error(tr(m.message))); else p.resolve(m);
        return;
      }
      if (m.type === 'status') onStatus(m);
      if (m.type === 'job') onJob(m.job);
      if (m.type === 'error') setStatus(t('错误：') + tr(m.message));
    };
  });
}

function request(msg) {
  return new Promise((resolve, reject) => {
    if (!S.ws) { reject(new Error(t('未连接'))); return; }
    const req = S.req++;
    S.pending.set(req, { resolve, reject });
    S.ws.send(JSON.stringify({ ...msg, req }));
    setTimeout(() => { if (S.pending.has(req)) { S.pending.delete(req); reject(new Error(t('超时'))); } }, 120000);
  });
}

function setStatus(t) { $('status').textContent = t; }

function onStatus(m) {
  if (m.running) {
    S.running = m.run;
    setStatus(t('后台演化 {0}：{1} / {2}（{3} ms/年）', m.run, fmtYear(m.year), m.years, fmt(m.msPerYear, 0)));
    if (S.run === m.run) {
      const my = Math.floor((m.year - 1) / 5) * 5;
      if (my > S.maxYear) { S.maxYear = my; $('slider').max = my; }
      if (!S._mtimer) S._mtimer = setTimeout(async () => { S._mtimer = null; await loadMetrics(); }, 3000);
    }
  } else {
    S.running = null;
    setStatus(m.message ? `${tr(m.message)}${m.run ? (EN ? ': ' : '：') + m.run : ''}` : '');
    if (m.run && m.run === S.run) loadRunData(S.run, true);
  }
}

// ---------------- 帧 ----------------
async function getFrame(year) {
  year = Math.max(0, Math.min(S.maxYear, Math.round(year / 5) * 5));
  const key = S.run + ':' + year;
  if (S.frames.has(key)) return S.frames.get(key);
  const p = (async () => {
    const buf = await request({ type: 'frame', run: S.run, year });
    return decodeFrame(S.w, buf);
  })();
  S.frames.set(key, p);
  if (S.frames.size > 120) S.frames.delete(S.frames.keys().next().value);
  try { return await p; } catch (e) { S.frames.delete(key); throw e; }
}

async function seek(year, fromPlay = false) {
  if (!S.run) return;
  year = Math.max(0, Math.min(S.maxYear, Math.round(year / 5) * 5));
  S.year = year;
  $('slider').value = year;
  $('yearLabel').textContent = fmtYear(year);
  let fr;
  try { fr = await getFrame(year); } catch (e) { setStatus(t('读取帧失败：') + e.message); return; }
  if (S.year !== year) return;
  S.frame = fr;
  repaint();
  drawCharts();
  if (!fromPlay || year % 25 === 0) loadEvents();
  if (!S.playing) warmLater();
  if (S.hover >= 0) showTooltip(S.hover);
  for (let k = 1; k <= 3; k++) if (year + 5 * k <= S.maxYear) getFrame(year + 5 * k).catch(() => {});
}

// 暂停后让服务器在后台预先重算该年份的完整状态，点击格子时即可立即显示家族明细
let warmTimer = null;
function warmLater() {
  clearTimeout(warmTimer);
  warmTimer = setTimeout(() => { if (!S.playing && S.ws) S.ws.send(JSON.stringify({ type: 'warm', run: S.run, year: S.year })); }, 700);
}

function play(on) {
  S.playing = on;
  $('btnPlay').textContent = on ? '⏸' : '▶';
  clearTimeout(S.timer);
  if (!on) return;
  const tick = async () => {
    if (!S.playing) return;
    if (S.year + 5 > S.maxYear) {
      if (S.running === S.run) { S.timer = setTimeout(tick, 500); return; }
      play(false); return;
    }
    const t0 = performance.now();
    await seek(S.year + 5, true);
    const fps = +$('speed').value;
    S.timer = setTimeout(tick, Math.max(0, 1000 / fps - (performance.now() - t0)));
  };
  tick();
}

// ---------------- 渲染 ----------------
// 超级帝国：政权历史表里记录了鼎盛期 [superStart, superEnd)
function isSuper(uid) {
  const p = S.polReg.get(uid);
  return !!(p && p.superStart !== undefined && p.superEnd && S.year >= p.superStart && S.year < p.superEnd);
}

function customName(uid) {
  const p = S.polReg.get(uid);
  return p && p.custom ? p.name : '';
}

function opts() {
  return {
    borders: $('optBorders').checked, rivers: $('optRivers').checked, labels: $('optLabels').checked,
    polMode: (uid) => S.polReg.get(uid)?.mode, isSuper, customName,
  };
}

function repaint() {
  const L = S.layer;
  if (L.dyn && !S.frame) return;
  S.map.shadeOn = $('optShade').checked;
  S.map.paint(L, S.frame, {});
  redraw();
  renderLegend();
}

function redraw() {
  S.map.draw(S.layer, S.frame, opts());
}

function renderLayerList() {
  const groups = new Map();
  for (const l of S.layers) {
    if (!groups.has(l.group)) groups.set(l.group, []);
    groups.get(l.group).push(l);
  }
  const box = $('layerList');
  box.innerHTML = '';
  const order = [...GROUP_ORDER, ...[...groups.keys()].filter((g) => !GROUP_ORDER.includes(g))];
  for (const g of order) {
    const items = groups.get(g);
    if (!items) continue;
    const el = document.createElement('div');
    el.className = 'lgroup' + (['综合', '政治(第5层)'].includes(g) ? ' open' : '');
    el.innerHTML = `<div class="gname">${t(g)}</div><div class="items"></div>`;
    el.querySelector('.gname').onclick = () => el.classList.toggle('open');
    for (const l of items) {
      const a = document.createElement('span');
      a.className = 'litem';
      a.textContent = t(l.name);
      a.title = l.desc ? t(l.desc) : '';
      a.dataset.id = l.id;
      a.onclick = () => selectLayer(l.id);
      el.querySelector('.items').appendChild(a);
    }
    box.appendChild(el);
  }
}

function selectLayer(id) {
  S.layer = S.layers.find((l) => l.id === id) || S.layers[0];
  document.querySelectorAll('.litem').forEach((e) => e.classList.toggle('on', e.dataset.id === S.layer.id));
  try { localStorage.setItem('mapsim.layer', S.layer.id); } catch { /* 忽略 */ }
  $('layerTitle').innerHTML = `<b>${t(S.layer.name)}</b> <span class="d">${S.layer.desc ? t(S.layer.desc) : ''}</span>`;
  S.map.highlightUid = 0;
  repaint();
}

function renderLegend() {
  const L = S.layer, lg = L.legend, box = $('legend');
  if (!lg) { box.innerHTML = ''; return; }
  if (lg.type === 'cont') {
    const stops = Array.from({ length: 12 }, (_, k) => lutCss(lg.cmap, k / 11)).join(',');
    const tk = (v) => fmt(v, Math.abs(v) < 1 && v !== 0 ? 2 : 0);
    const mid = lg.log ? Math.sqrt(lg.lo * lg.hi) : (lg.lo + lg.hi) / 2;
    box.innerHTML = `<div>${t(L.name)}${lg.unit ? paren(t(lg.unit)) : ''}${lg.log ? ' · ' + t('对数') : ''}</div>
      <div class="bar" style="background:linear-gradient(90deg,${stops})"></div>
      <div class="ticks"><span>${tk(lg.lo)}</span><span>${tk(mid)}</span><span>${tk(lg.hi)}</span></div>`;
  } else if (lg.type === 'cat') {
    const used = new Set();
    const arr = lg.field ? S.w.f[lg.field] : (S.frame ? S.frame.f.dmode : null);
    if (arr) for (const i of S.w.landIdx) used.add(arr[i]);
    box.innerHTML = `<div>${t(L.name)}</div><div class="cat">` + lg.items.filter((it) => !arr || used.has(it.k))
      .map((it) => `<span><i class="sw" style="background:${it.color}"></i>${t(it.label)}</span>`).join('') + '</div>';
  } else if (lg.type === 'polity' && S.frame) {
    const key = L.borders === 'tribe' ? 'tribe' : 'top1';
    const cnt = new Map();
    const arr = S.frame.f[key];
    for (const i of S.w.landIdx) { const v = arr[i]; if (v && v !== SCATTERED) cnt.set(v, (cnt.get(v) || 0) + 1); }
    const top = [...cnt.entries()].sort((a, b) => b[1] - a[1]).slice(0, S.legendOpen ? 15 : 6);
    box.innerHTML = `<div class="lh" style="cursor:pointer">${t('{0} · 前 {1}（按格数）', t(L.name), top.length)}<span class="muted"> ${S.legendOpen ? t('收起 ▴') : t('展开 ▾')}</span></div>` + top.map(([k, c]) => {
      const p = S.frame.pols[k];
      const rgb = polityColor(p.uid);
      return `<div class="pl" data-uid="${p.uid}"><span><i class="sw" style="background:rgb(${rgb})"></i>${polityTitle(p.uid, p, S.polReg.get(p.uid)?.mode, isSuper(p.uid), customName(p.uid))}</span><span class="n">${t('{0} 格', c)} · ${fmtPop(p.pop)}</span></div>`;
    }).join('') + `<div class="muted" style="margin-top:4px">${t('共 {0} 个政权；灰色=零散小族', cnt.size)}</div>`;
    box.querySelector('.lh').onclick = () => { S.legendOpen = !S.legendOpen; renderLegend(); };
    box.querySelectorAll('.pl').forEach((el) => {
      el.onmouseenter = () => { S.map.highlightUid = +el.dataset.uid; redraw(); };
      el.onmouseleave = () => { S.map.highlightUid = 0; redraw(); };
      el.onclick = () => { const p = S.frame.pols.find((q) => q && q.uid === +el.dataset.uid); if (p && p.capital >= 0) inspect(p.capital); };
    });
  } else if (lg.type === 'bivar') {
    const B = [['#e8e8e8', '#b5c0da', '#6c83b5'], ['#b8d6be', '#90b2b3', '#567994'], ['#73ae80', '#5a9178', '#2a5a5b']];
    let g = '<div style="display:grid;grid-template-columns:auto repeat(3,18px);gap:2px;align-items:center">';
    for (let r = 2; r >= 0; r--) {
      g += `<span class="muted" style="font-size:11px;padding-right:4px">${t(['压力低', '压力中', '压力高'][r])}</span>`;
      for (let c = 0; c < 3; c++) g += `<span style="width:18px;height:14px;background:${B[r][c]}"></span>`;
    }
    g += `<span></span><span class="muted" style="font-size:10px">${t('低')}</span><span class="muted" style="font-size:10px">${t('中')}</span><span class="muted" style="font-size:10px">${t('高')}</span></div>`;
    box.innerHTML = `<div>${t('资源 × 人口')}</div>${g}<div class="muted">${t('横轴：承载力')}</div>`;
  } else box.innerHTML = '';
}

// ---------------- 悬浮窗 ----------------
const KOPPEN_DESC = { Af: '热带雨林', Am: '热带季风', Aw: '热带草原', BWh: '热沙漠', BWk: '冷沙漠', BSh: '热草原', BSk: '冷草原',
  Csa: '地中海(夏热)', Csb: '地中海(夏暖)', Cwa: '亚热带季风', Cwb: '高原温带季风', Cfa: '亚热带湿润', Cfb: '温带海洋', Cfc: '亚寒带海洋',
  Dsa: '大陆(夏干热)', Dsb: '大陆(夏干)', Dsc: '亚寒带(夏干)', Dwa: '温带季风(夏热)', Dwb: '温带季风', Dwc: '亚寒带季风', Dwd: '极寒季风',
  Dfa: '湿润大陆(夏热)', Dfb: '湿润大陆', Dfc: '亚寒带针叶林', Dfd: '极寒针叶林', ET: '苔原', EF: '冰原' };

function bar(label, v, max, txt) {
  const p = Math.max(0, Math.min(100, (v / max) * 100));
  return `<div class="row"><span>${label}</span><span class="bar"><i style="width:${p}%"></i></span><span class="v">${txt}</span></div>`;
}

function polName(idx, fr) {
  if (!idx) return `<span class="muted">${t('无')}</span>`;
  if (idx === SCATTERED) return `<span class="muted">${t('零散小族')}</span>`;
  const p = fr.pols[idx];
  return `<i class="dot" style="background:rgb(${polityColor(p.uid)})"></i>${polityTitle(p.uid, p, S.polReg.get(p.uid)?.mode, isSuper(p.uid), customName(p.uid))}`;
}

function tooltipHTML(i) {
  const w = S.w, f = w.f, fr = S.frame;
  const [lon, lat] = w.lonlat(i);
  const kd = w.desc.koppen.labels[f.koppen[i]];
  const lc = t(w.desc.landcover.labels[f.landcover[i]]);
  let h = `<div class="h">${fmt(Math.abs(lat), 2)}°${lat >= 0 ? 'N' : 'S'} ${fmt(lon, 2)}°E · ${kd} ${KOPPEN_DESC[kd] ? t(KOPPEN_DESC[kd]) : ''}</div>
    <div class="sub">${lc} · ${t('海拔')} ${fmt(f.elev[i])}m · ${t('阻力')} ${fmt(f.friction[i], 1)}× · ${fmt(f.t_ann[i], 1)}°C · ${fmt(f.p_ann[i])}mm</div>`;
  const g = S.layer.group;
  const secs = [];
  // 资源
  const caps = [[t('旱作'), f.cap_dry[i]], [t('水稻'), f.cap_rice[i]], [t('灌溉'), f.cap_irr[i]], [t('牧草'), f.cap_past[i]],
    [t('驯鹿'), f.cap_rein[i]], [t('狩猎采集'), f.cap_hg[i]], [t('渔业'), f.cap_fish[i]]];
  const cmax = Math.max(...caps.map((c) => c[1]), 0.01);
  const mode2 = t(w.modes[f.mode[i]]);
  secs.push({ key: ['资源', '承载(第2层)', '水', '气候', '基础'], t: t('资源（人/km²）'), s: t('资源'),
    one: t('潜在 {0} 人/km² · {1} · 淡水 {2}', fmt(f.density[i], 2), mode2, fmt(f.freshwater[i], 2)),
    full: caps.filter((c) => c[1] > 0.005).sort((a, b) => b[1] - a[1]).map((c) => bar(c[0], c[1], cmax, fmt(c[1], c[1] < 1 ? 2 : 1))).join('') +
      `<div class="muted">${t('潜在密度 {0} · 第2层生计：{1} · 河流 {2} m³/s', fmt(f.density[i], 2), mode2, fmt(f.discharge[i]))}</div>` });
  if (fr) {
    const d = fr.pop[i] / f.area_km2[i];
    const press = f.density[i] > 0 ? fr.pop[i] / (f.density[i] * f.area_km2[i]) : 0;
    const dm = t(w.modes[fr.f.dmode[i]]);
    secs.push({ key: ['人口(第3层)'], t: t('人口（第3层）'), s: t('人口'),
      one: t('{0} 人 · {1} 个家族 · {2}', fmtPop(fr.pop[i]), fr.f.fam[i], dm),
      full: bar(t('密度'), Math.log10(Math.max(d, 0.01)) + 2, 4.5, fmt(d, 2) + '/km²') + bar(t('人口压力'), press, 1.2, fmt(press * 100) + '%') +
        `<div class="muted">${t('{0} 人 · {1} 个家族 · 生计：{2} · 气候 {3}×', fmtPop(fr.pop[i]), fr.f.fam[i], dm, fmt(0.4 + fr.f.clim[i] / 255 * 1.2, 2))}</div>` });
    secs.push({ key: ['部落(第4层)'], t: t('部落（第4层）'), s: t('部落'),
      one: `${polName(fr.f.tribe[i], fr)} · ${t('层级')} ${fmt(fr.f.depth[i] / 20, 1)}`,
      full: `<div>${polName(fr.f.tribe[i], fr)}</div><div class="muted">${t('平均统治层级 {0} · 仇恨热度 {1}', fmt(fr.f.depth[i] / 20, 1), fmt(fr.f.hate[i] / 255 * 2, 2))}</div>` });
    const sh = fr.f.share[i] / 255;
    secs.push({ key: ['政治(第5层)', '综合'], t: t('政治（第5层）'), s: t('政治'),
      one: `${polName(fr.f.top1[i], fr)}${fr.f.top2[i] ? ' · ' + t('重叠') + ' ' + fmt(sh * 100) + '%' : ''}`,
      full: `<div>${polName(fr.f.top1[i], fr)} <span class="muted">${t('控制')} ${fmt(sh * 100)}%</span></div>` +
        (fr.f.top2[i] ? `<div class="muted">${t('与 {0} 争夺', polName(fr.f.top2[i], fr))}</div>` : '') +
        (fr.f.top1[i] && fr.f.top1[i] !== SCATTERED ? `<div class="muted">${t('{0} 个家族 · {1} 人 · {2} 格', fmt(fr.pols[fr.f.top1[i]].families), fmtPop(fr.pols[fr.f.top1[i]].pop), fr.pols[fr.f.top1[i]].cells)}</div>` : '') });
    if (fr.f.fam[i] > 0) {
      const trs = [['农耕', 't_farm'], ['放牧', 't_herd'], ['狩猎', 't_hunt'], ['好战', 't_aggr'], ['流动', 't_mob'], ['忠诚', 't_loyal']];
      secs.push({ key: ['家族属性'], t: t('家族属性（均值）'), s: t('家族属性'),
        one: trs.map(([n, k]) => `${t(n)} ${fmt(fr.f[k][i] / 255, 2)}`).slice(0, 4).join(' · '),
        full: trs.map(([n, k]) => bar(t(n), fr.f[k][i], 255, fmt(fr.f[k][i] / 255, 2))).join('') });
    }
  }
  secs.sort((a, b) => (b.key.includes(g) ? 1 : 0) - (a.key.includes(g) ? 1 : 0));
  secs.forEach((s, k) => {
    h += k === 0 ? `<div class="sec main"><div class="t">${s.t}</div>${s.full}</div>` : `<div class="sec"><div class="one">${s.s}${EN ? ': ' : '：'}${s.one}</div></div>`;
  });
  h += `<div class="sec muted">${t('点击查看家族明细与统治链')}</div>`;
  return h;
}

function showTooltip(i) {
  const tt = $('tooltip');
  if (i < 0 || !S.w.f.land[i] || S.w.f.area_km2[i] <= 0) { tt.hidden = true; return; }
  tt.innerHTML = tooltipHTML(i);
  tt.hidden = false;
  const r = $('center').getBoundingClientRect();
  let x = S.mouse[0] + 16, y = S.mouse[1] + 16;
  if (x + 300 > r.width) x = S.mouse[0] - 306;
  if (y + tt.offsetHeight > r.height) y = Math.max(4, r.height - tt.offsetHeight - 4);
  tt.style.left = x + 'px';
  tt.style.top = y + 'px';
}

// ---------------- 详情面板 ----------------
async function inspect(cell) {
  if (!S.run) return;
  S.inspectCell = cell;
  $('right').hidden = false;
  S.map.resize();
  redraw();
  const [lon, lat] = S.w.lonlat(cell);
  $('inspTitle').textContent = t('格子 {0}°N {1}°E · {2}', fmt(lat, 2), fmt(lon, 2), fmtYear(S.year));
  const back = (S.year + 1) % 200;
  $('inspBody').innerHTML = `<p class="muted">${t('从最近检查点重算到该年份（最多约 {0} 秒；同一年份再次点击会立即显示）…', Math.max(1, Math.round(back * 0.09)))}</p>`;
  let d;
  try { d = (await request({ type: 'inspect', run: S.run, year: S.year, cell })).data; } catch (e) {
    $('inspBody').innerHTML = `<p class="muted">${t('读取失败：')}${e.message}</p>`; return;
  }
  if (S.inspectCell !== cell) return;
  const tn = S.w.traitNames.map((n) => t(n));
  const otherName = (uid, name) => customName(uid) || (EN ? baseName(uid) : name || baseName(uid)); // 服务端名称是中文
  const pol = (p, label) => {
    if (!p) return '';
    const fp = S.frame?.pols.find((q) => q && q.uid === p.uid);
    const title = polityTitle(p.uid, fp || { families: p.families }, p.mode, isSuper(p.uid), customName(p.uid));
    return `<div class="card"><div style="font-weight:600"><i class="dot" style="background:rgb(${polityColor(p.uid)})"></i>${title} <span class="muted">${label}</span></div>
      <div class="kv"><span class="k">${t('家族')}</span><span>${fmt(p.families)}</span><span class="k">${t('人口')}</span><span>${fmtPop(p.pop)}</span>
      <span class="k">${t('建立')}</span><span>${p.born !== undefined ? fmtYear(p.born) : '—'}</span>
      <span class="k">${t('首府')}</span><span>${p.capital >= 0 ? `<a href="#" data-cell="${p.capital}">${S.w.lonlat(p.capital).map((v) => fmt(v, 1)).reverse().join('°N ')}°E</a>` : '—'}</span></div></div>`;
  };
  let h = '';
  h += `<h4>${t('政权')}</h4>` + (pol(d.polity, t('影响力第一')) || `<p class="muted">${t('无政权影响')}</p>`) + pol(d.polity2, t('重叠争夺'));
  if (d.hatred && d.hatred.length) {
    h += `<h4>${t('该政权的宿敌')}</h4><div class="card">` + d.hatred.map((x) => `<div><i class="dot" style="background:rgb(${polityColor(x.uid)})"></i>${otherName(x.uid, x.name)} <span class="muted">${t('仇恨')} ${fmt(x.v, 2)}</span></div>`).join('') + '</div>';
  }
  if (d.chain && d.chain.length) {
    h += `<h4>${t('统治链（本格最大家族 → 最高首领）')}</h4><ul class="chain">` + d.chain.map((c, k) =>
      `<li class="${k === d.chain.length - 1 ? 'root' : ''}" data-cell="${c.cell}">${k === d.chain.length - 1 ? t('首领') + ' ' : ''}${t('家族 #{0} · {1} 人', c.uid, fmtPop(c.pop))} <span class="muted">${S.w.lonlat(c.cell).map((v) => fmt(v, 1)).reverse().join('°N ')}°E</span></li>`).join('') + '</ul>';
  }
  h += `<h4>${t('家族（共 {0} 个，显示前 {1}）', d.familyCount, d.families?.length || 0)}</h4>`;
  for (const fam of d.families || []) {
    h += `<div class="fam"><div class="top"><span>#${fam.uid} · ${t('{0} 人', fmtPop(fam.pop))}</span><span class="m">${t('层级 {0} · 建于 {1} 年 · 存粮 {2}', fam.depth, fam.born, fmt(fam.store, 2))}</span></div>
      <div class="m muted" style="font-size:11px">${fam.parent ? t('上级 #{0}', fam.parent) + ' · ' : t('独立首领') + ' · '}${t('所属')} ${customName(fam.root) || baseName(fam.root)}${fam.grudges ? ' · ' + t('仇恨：') + fam.grudges.map((g) => otherName(g.target, g.name) + ' ' + fmt(g.v, 1)).join(EN ? ', ' : '，') : ''}</div>
      <div class="trs">${tn.map((n, k) => {
        const v = fam.traits[k], m = d.meanTraits[k];
        const sc = k === 7 ? (x) => (x - 0.5) : (x) => x;
        return `<div class="tr">${n} ${fmt(v, 2)}<div class="b"><i style="width:${sc(v) * 100}%"></i><u style="left:${sc(m) * 100}%"></u></div></div>`;
      }).join('')}</div></div>`;
  }
  h += `<p class="muted">${t('白色刻度 = 本格家族平均值')}</p>`;
  $('inspBody').innerHTML = h;
  $('inspBody').querySelectorAll('[data-cell]').forEach((el) => el.addEventListener('click', (e) => {
    e.preventDefault();
    const c = +el.dataset.cell;
    S.map.hover = c; redraw(); inspect(c);
  }));
}

// ---------------- 指标曲线与事件 ----------------
async function loadMetrics() {
  if (!S.run) return;
  try { S.metrics = await api(`/v1/runs/${S.run}/metrics`); } catch { S.metrics = []; }
  drawCharts();
}

function drawCharts() {
  const cv = $('charts');
  const r = cv.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  cv.width = r.width * dpr; cv.height = r.height * dpr;
  const c = cv.getContext('2d');
  c.scale(dpr, dpr);
  const W = r.width, H = r.height;
  c.clearRect(0, 0, W, H);
  const M = S.metrics;
  const total = Math.max(S.meta?.params?.years || 1, M.length);
  if (!M.length) return;
  const series = [
    [t('总人口'), (m) => m.population, '#e0a84a'],
    [t('政权数(≥10家族)'), (m) => m.polities10, '#5aa0d8'],
    [t('最大政权占比'), (m) => m.largest, '#d8584a'],
  ];
  const step = Math.max(1, Math.floor(M.length / W));
  series.forEach(([name, fn, col], k) => {
    let mx = 0;
    for (const m of M) mx = Math.max(mx, fn(m));
    c.beginPath();
    for (let j = 0; j < M.length; j += step) {
      const x = (M[j].year / total) * W;
      const y = H - 4 - (fn(M[j]) / (mx || 1)) * (H - 14);
      j === 0 ? c.moveTo(x, y) : c.lineTo(x, y);
    }
    c.strokeStyle = col; c.lineWidth = 1.2; c.stroke();
    const last = M.find((m) => m.year >= S.year) || M[M.length - 1];
    const v = fn(last);
    c.fillStyle = col;
    c.font = '11px system-ui';
    c.fillText(`${name} ${k === 0 ? fmtPop(v) : k === 2 ? fmt(v * 100, 1) + '%' : fmt(v)}`, 6 + k * (EN ? 200 : 170), 11);
  });
  const x = (S.year / total) * W;
  c.strokeStyle = 'rgba(255,255,255,0.6)'; c.beginPath(); c.moveTo(x, 0); c.lineTo(x, H); c.stroke();
}

let evTimer = null;
function loadEvents() {
  clearTimeout(evTimer);
  evTimer = setTimeout(async () => {
    if (!S.run) return;
    let ev = [];
    try { ev = await api(`/v1/runs/${S.run}/events?from=${Math.max(0, S.year - 60)}&to=${S.year}`); } catch { /* 忽略 */ }
    const nm = (uid) => {
      const p = S.frame?.pols.find((q) => q && q.uid === uid);
      return `<i class="dot" style="background:rgb(${polityColor(uid)})"></i>${p ? polityTitle(uid, p, S.polReg.get(uid)?.mode, isSuper(uid), customName(uid)) : baseName(uid)}`;
    };
    const big = (e) => {
      const a = S.polReg.get(e.a), b = e.b ? S.polReg.get(e.b) : null;
      return Math.max(a?.peakCells || 0, b?.peakCells || 0);
    };
    ev = (ev || []).filter((e) => e.type.startsWith('super') || big(e) >= 20).sort((a, b) => b.year - a.year || big(b) - big(a)).slice(0, 40);
    const txt = (e) => {
      if (e.type === 'annex') return t('{0} 吞并了 {1}', nm(e.a), nm(e.b));
      if (e.type === 'cede') return t('{0} 击败 {1}，夺取 {2} 个家族', nm(e.a), nm(e.b), e.n);
      if (e.type === 'repel') return t('{0} 击退了 {1} 的进攻', nm(e.a), nm(e.b));
      if (e.type === 'succession') return t('{0} 爆发继承危机，{1} 支分裂', nm(e.a), e.n);
      if (e.type === 'super_rise') return `<b style="color:#e0a84a">★ ${t('{0} 崛起为超级帝国（鼎盛约 {1} 年）', nm(e.a), e.n)}</b>`;
      if (e.type === 'super_fall') return `<b style="color:#d8584a">${e.n ? t('{0} 超级帝国瓦解，分裂出 {1} 支', nm(e.a), e.n) : t('{0} 超级帝国瓦解', nm(e.a))}</b>`;
      return e.type;
    };
    $('events').innerHTML = ev.length ? ev.map((e) => `<div class="ev"><span class="y">${e.year}</span>${txt(e)}</div>`).join('') : `<div class="muted">${t('近 60 年无重大政权事件')}</div>`;
  }, 150);
}

// ---------------- 演化列表与参数 ----------------
async function refreshRuns(select) {
  const [{ runs }, { worlds }] = await Promise.all([api('/v1/runs'), api('/v1/worlds')]);
  S.worldNames = new Map(worlds.map((w) => [w.id, tr(w.name)]));
  const sel = $('runSelect');
  sel.innerHTML = (runs || []).map((r) => `<option value="${r.id}">${S.worldNames.get(r.worldId) || r.worldId} · ${r.id} · ${t('{0}/{1} 年', r.computedUntil, r.params.years)}${r.done ? '' : (r.failed ? t('（失败）') : t('（未完成）'))}</option>`).join('');
  if (!runs || !runs.length) { $('loading').textContent = t('还没有演化记录：到控制台(/console.html)上传地图或在内置世界上开始演化'); return null; }
  const want = select || new URL(location.href).searchParams.get('run');
  const pick = (runs.find((r) => r.id === want) || runs.find((r) => r.done) || runs[0]);
  sel.value = pick.id;
  return pick;
}

async function loadRunData(id, keepYear = false) {
  S.run = id;
  S.meta = await api(`/v1/runs/${id}`);
  try {
    const reg = await api(`/v1/runs/${id}/polities`);
    S.polReg = new Map((reg || []).map((p) => [p.uid, p]));
  } catch { S.polReg = new Map(); }
  await loadMetrics();
  const last = S.metrics.length ? S.metrics[S.metrics.length - 1].year : 0;
  S.maxYear = Math.floor(last / 5) * 5;
  $('slider').max = S.maxYear;
  $('loading').style.display = 'none';
  const u = new URL(location.href);
  u.searchParams.set('run', id);
  history.replaceState(null, '', u.toString());
  await seek(keepYear ? S.year : 0);
}

// 切换到另一个世界的演化时，整页重新加载(格子数据不同)
function switchRun(id) {
  const u = new URL(location.href);
  u.searchParams.set('run', id);
  location.href = u.toString();
}

async function openParams() {
  const schema = await api('/v1/params');
  const cur = S.meta?.params || {};
  let g = '';
  let h = '';
  for (const p of schema) {
    if (p.group !== g) { g = p.group; h += `<div class="pg">${t(g)}</div>`; }
    const v = cur[p.id] ?? p.default;
    h += `<label><span>${p.id}<small>${t(p.desc)}</small></span><input data-id="${p.id}" data-int="${p.int}" type="number" step="${p.int ? 1 : 'any'}" min="${p.min}" max="${p.max}" value="${v}"></label>`;
  }
  $('paramForm').innerHTML = h;
  $('paramDlg').showModal();
}

async function newRun() {
  const params = {};
  $('paramForm').querySelectorAll('input').forEach((el) => {
    params[el.dataset.id] = el.dataset.int === 'true' ? parseInt(el.value, 10) : parseFloat(el.value);
  });
  try {
    const job = await api(`/v1/worlds/${S.meta.worldId}/jobs`, { method: 'POST', body: { simulate: { params } } });
    S.pendingJob = job.id;
    setStatus(t('已提交任务 {0}，等待开始…', job.id));
  } catch (e) { alert(t('启动失败：') + e.message); return; }
  $('paramDlg').close();
}

// 任务进度推送：自己刚提交的演化一开始就切过去实时观看
function onJob(j) {
  if (j.id !== S.pendingJob) return;
  setStatus(`${t('任务 {0}：', j.id)}${j.status} ${j.stage || ''} ${Math.round((j.progress || 0) * 100)}% ${tr(j.message || '')}`);
  if (j.result?.runId && j.result.runId !== S.run) {
    S.pendingJob = null;
    switchRun(j.result.runId);
  }
  if (j.status === 'failed') { S.pendingJob = null; alert(t('任务失败：') + tr(j.error)); }
}

async function stopRun() {
  const { jobs } = await api('/v1/jobs');
  const j = jobs.find((x) => x.result?.runId === S.run && (x.status === 'running' || x.status === 'queued'));
  if (!j) { alert(t('当前演化没有在运行')); return; }
  await api(`/v1/jobs/${j.id}`, { method: 'DELETE' });
}

// ---------------- 交互 ----------------
function bindMap() {
  const cv = $('map');
  let drag = null;
  cv.addEventListener('mousemove', (e) => {
    const r = cv.getBoundingClientRect();
    S.mouse = [e.clientX - r.left, e.clientY - r.top];
    if (drag) {
      S.map.panX += (e.clientX - drag[0]) * S.map.dpr;
      S.map.panY += (e.clientY - drag[1]) * S.map.dpr;
      drag = [e.clientX, e.clientY, true];
      redraw();
      return;
    }
    const i = S.map.cellAt(S.mouse[0], S.mouse[1]);
    if (i !== S.hover) {
      S.hover = i;
      S.map.hover = i;
      const L = S.layer;
      if (S.frame && L.borders) {
        const idx = S.frame.f[L.borders][i];
        S.map.highlightUid = idx && idx !== SCATTERED ? S.frame.pols[idx].uid : 0;
      }
      redraw();
    }
    showTooltip(i);
  });
  cv.addEventListener('mouseleave', () => { S.hover = -1; S.map.hover = -1; S.map.highlightUid = 0; $('tooltip').hidden = true; redraw(); });
  cv.addEventListener('mousedown', (e) => { drag = [e.clientX, e.clientY, false]; cv.classList.add('drag'); });
  window.addEventListener('mouseup', (e) => {
    if (drag && !drag[2] && e.target === cv) {
      const i = S.map.cellAt(S.mouse[0], S.mouse[1]);
      if (i >= 0 && S.w.f.land[i]) inspect(i);
    }
    drag = null; cv.classList.remove('drag');
  });
  cv.addEventListener('wheel', (e) => {
    e.preventDefault();
    S.map.zoomAt(S.mouse[0], S.mouse[1], e.deltaY < 0 ? 1.2 : 1 / 1.2);
    redraw();
  }, { passive: false });
  cv.addEventListener('dblclick', () => { S.map.fit(); redraw(); });
  window.addEventListener('resize', () => { S.map.resize(); redraw(); drawCharts(); });
}

function bindControls() {
  $('btnPlay').onclick = () => play(!S.playing);
  $('btnBack').onclick = () => { play(false); seek(S.year - 5); };
  $('btnFwd').onclick = () => { play(false); seek(S.year + 5); };
  $('slider').oninput = (e) => { play(false); seek(+e.target.value); };
  $('runSelect').onchange = (e) => { play(false); switchRun(e.target.value); };
  $('btnParams').onclick = openParams;
  $('btnNewRun').onclick = newRun;
  $('btnStop').onclick = () => stopRun().catch((e) => alert(e.message));
  $('inspClose').onclick = () => { $('right').hidden = true; S.inspectCell = -1; S.map.resize(); redraw(); };
  $('btnPlay').addEventListener('click', () => { if (!S.playing) warmLater(); });
  for (const id of ['optShade']) $(id).onchange = repaint;
  for (const id of ['optBorders', 'optRivers', 'optLabels']) $(id).onchange = redraw;
  $('charts').addEventListener('click', (e) => {
    const r = e.target.getBoundingClientRect();
    const total = Math.max(S.meta?.params?.years || 1, S.metrics.length);
    play(false);
    seek(((e.clientX - r.left) / r.width) * total);
  });
  window.addEventListener('keydown', (e) => {
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;
    if (e.code === 'Space') { e.preventDefault(); play(!S.playing); }
    if (e.code === 'ArrowRight') { play(false); seek(S.year + 5); }
    if (e.code === 'ArrowLeft') { play(false); seek(S.year - 5); }
    if (e.code === 'Escape') $('inspClose').click();
  });
}

async function main() {
  mountLang($('langSlot'));
  await ensureKey();
  const pick = await refreshRuns();
  if (!pick) return;
  S.w = await World.load(pick.worldId);
  S.map = new MapView($('map'), S.w);
  S.layers = buildLayers(S.w);
  renderLayerList();
  bindMap();
  bindControls();
  let saved = null;
  try { saved = localStorage.getItem('mapsim.layer'); } catch { /* 忽略 */ }
  S.layer = S.layers.find((l) => l.id === (saved || 'c_pol')) || S.layers[0];
  await connect();
  selectLayer(S.layer.id);
  await loadRunData(pick.id);
}

main().catch((e) => { $('loading').textContent = t('加载失败：') + e.message; console.error(e); });
