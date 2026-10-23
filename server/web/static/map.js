// 地图渲染：每格 4×4 像素的离屏画布 + 晕渲 + 边界线 + 首府标注；支持缩放平移
import { lut, polityColor, hexToRgb, BIVAR } from './colors.js';
import { SCATTERED, polityTitle } from './data.js';

const K = 4; // 每格像素
const WATER = [24, 44, 66];
const NODATA = [205, 200, 190];
const SCAT = [150, 146, 138];

export class MapView {
  constructor(canvas, world) {
    this.cv = canvas;
    this.ctx = canvas.getContext('2d');
    this.w = world;
    this.W = world.nx * K;
    this.H = world.ny * K;
    this.off = document.createElement('canvas');
    this.off.width = this.W;
    this.off.height = this.H;
    this.octx = this.off.getContext('2d');
    this.img = this.octx.createImageData(this.W, this.H);
    this.cellRGB = new Uint8ClampedArray(world.n * 3);
    this.cellStripe = new Int32Array(world.n).fill(-1); // 重叠条纹颜色索引(-1 无)
    this.stripeRGB = new Uint8ClampedArray(world.n * 3);
    this.zoom = 1; this.panX = 0; this.panY = 0;
    this.shadeOn = true;
    this.computeShade();
    this.fit();
    this.hover = -1;
    this.highlightUid = 0;
  }

  computeShade() {
    const w = this.w, W = this.W, H = this.H;
    const e = w.f.elev;
    const land = w.f.land;
    const ev = (x, y) => {
      // 双线性插值的海拔(格中心对齐)
      const fx = Math.min(Math.max(x / K - 0.5, 0), w.nx - 1.001);
      const fy = Math.min(Math.max(y / K - 0.5, 0), w.ny - 1.001);
      const x0 = fx | 0, y0 = fy | 0, tx = fx - x0, ty = fy - y0;
      const i = y0 * w.nx + x0;
      const a = land[i] ? e[i] : 0, b = land[i + 1] ? e[i + 1] : 0;
      const c = land[i + w.nx] ? e[i + w.nx] : 0, d = land[i + w.nx + 1] ? e[i + w.nx + 1] : 0;
      return (a * (1 - tx) + b * tx) * (1 - ty) + (c * (1 - tx) + d * tx) * ty;
    };
    const el = new Float32Array(W * H);
    for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) el[y * W + x] = ev(x, y);
    this.shade = new Float32Array(W * H);
    const z = 0.0022; // 垂直夸张
    for (let y = 1; y < H - 1; y++) for (let x = 1; x < W - 1; x++) {
      const dx = (el[y * W + x + 1] - el[y * W + x - 1]) * z;
      const dy = (el[(y + 1) * W + x] - el[(y - 1) * W + x]) * z;
      // 光源西北
      const s = (-dx * -0.7 + -dy * -0.7 + 1) / Math.sqrt(dx * dx + dy * dy + 1);
      this.shade[y * W + x] = Math.max(0.55, Math.min(1.25, 0.35 + 0.75 * s));
    }
  }

  fit() {
    const r = this.cv.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    this.cv.width = Math.max(1, Math.round(r.width * dpr));
    this.cv.height = Math.max(1, Math.round(r.height * dpr));
    this.dpr = dpr;
    const s = Math.min(this.cv.width / this.W, this.cv.height / this.H);
    this.zoom = s;
    this.panX = (this.cv.width - this.W * s) / 2;
    this.panY = (this.cv.height - this.H * s) / 2;
  }

  resize() {
    const r = this.cv.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    const nw = Math.round(r.width * dpr), nh = Math.round(r.height * dpr);
    if (nw !== this.cv.width || nh !== this.cv.height) {
      const keepFit = this.zoom === Math.min(this.cv.width / this.W, this.cv.height / this.H);
      this.cv.width = nw; this.cv.height = nh;
      if (keepFit) this.fit();
    }
  }

  // 屏幕坐标(CSS 像素) → 格子下标
  cellAt(cx, cy) {
    const x = (cx * this.dpr - this.panX) / this.zoom;
    const y = (cy * this.dpr - this.panY) / this.zoom;
    if (x < 0 || y < 0 || x >= this.W || y >= this.H) return -1;
    return Math.floor(y / K) * this.w.nx + Math.floor(x / K);
  }

  zoomAt(cx, cy, factor) {
    const px = cx * this.dpr, py = cy * this.dpr;
    const minZ = Math.min(this.cv.width / this.W, this.cv.height / this.H) * 0.8;
    const nz = Math.max(minZ, Math.min(40, this.zoom * factor));
    this.panX = px - (px - this.panX) * nz / this.zoom;
    this.panY = py - (py - this.panY) * nz / this.zoom;
    this.zoom = nz;
  }

  // 根据图层计算每格颜色 → 离屏画布
  paint(layer, frame, ctx) {
    const w = this.w, n = w.n;
    const rgb = this.cellRGB;
    this.cellStripe.fill(-1);
    const has = new Uint8Array(n);
    for (const i of w.landIdx) {
      const c = layer.color(i, frame, ctx);
      if (!c) { rgb[i * 3] = NODATA[0]; rgb[i * 3 + 1] = NODATA[1]; rgb[i * 3 + 2] = NODATA[2]; continue; }
      rgb[i * 3] = c[0]; rgb[i * 3 + 1] = c[1]; rgb[i * 3 + 2] = c[2];
      has[i] = 1;
      if (layer.stripe) {
        const s = layer.stripe(i, frame, ctx);
        if (s) { this.cellStripe[i] = 1; this.stripeRGB[i * 3] = s[0]; this.stripeRGB[i * 3 + 1] = s[1]; this.stripeRGB[i * 3 + 2] = s[2]; }
      }
    }
    const d = this.img.data, W = this.W, H = this.H, nx = w.nx;
    const land = w.f.land;
    const shadeOn = this.shadeOn && layer.shade !== false;
    for (let y = 0; y < H; y++) {
      const cy = (y / K) | 0;
      for (let x = 0; x < W; x++) {
        const i = cy * nx + ((x / K) | 0);
        const p = (y * W + x) * 4;
        let r, g, b;
        if (!land[i] || w.f.area_km2[i] <= 0) {
          r = WATER[0]; g = WATER[1]; b = WATER[2];
        } else {
          const useStripe = this.cellStripe[i] >= 0 && ((x + y) % 6) < 2;
          const src = useStripe ? this.stripeRGB : rgb;
          r = src[i * 3]; g = src[i * 3 + 1]; b = src[i * 3 + 2];
          if (shadeOn) {
            const s = this.shade[y * W + x];
            r *= s; g *= s; b *= s;
          }
        }
        d[p] = r; d[p + 1] = g; d[p + 2] = b; d[p + 3] = 255;
      }
    }
    this.octx.putImageData(this.img, 0, 0);
  }

  draw(layer, frame, opts) {
    const c = this.ctx;
    c.setTransform(1, 0, 0, 1, 0, 0);
    c.fillStyle = '#101418';
    c.fillRect(0, 0, this.cv.width, this.cv.height);
    c.imageSmoothingEnabled = false;
    c.setTransform(this.zoom, 0, 0, this.zoom, this.panX, this.panY);
    c.drawImage(this.off, 0, 0);
    if (opts.rivers) this.drawRivers();
    const bkey = layer.borders;
    if (bkey && frame && opts.borders) this.drawBorders(frame.f[bkey], 'rgba(20,20,20,0.75)', 1);
    if (this.highlightUid && frame && bkey) this.drawHighlight(frame, bkey);
    c.setTransform(1, 0, 0, 1, 0, 0);
    if (opts.labels && frame && layer.labels) this.drawLabels(frame, opts);
    if (this.hover >= 0) {
      c.setTransform(this.zoom, 0, 0, this.zoom, this.panX, this.panY);
      const r = Math.floor(this.hover / this.w.nx), col = this.hover % this.w.nx;
      c.strokeStyle = '#fff';
      c.lineWidth = 2 / this.zoom;
      c.strokeRect(col * K, r * K, K, K);
      c.setTransform(1, 0, 0, 1, 0, 0);
    }
  }

  drawBorders(arr, color, width) {
    const w = this.w, c = this.ctx, nx = w.nx;
    c.beginPath();
    for (const i of w.landIdx) {
      const a = arr[i];
      const col = i % nx;
      if (col + 1 < nx) {
        const j = i + 1;
        if (w.f.land[j] && arr[j] !== a) { const x = (col + 1) * K, y = Math.floor(i / nx) * K; c.moveTo(x, y); c.lineTo(x, y + K); }
      }
      const j2 = i + nx;
      if (j2 < w.n && w.f.land[j2] && arr[j2] !== a) { const x = col * K, y = (Math.floor(i / nx) + 1) * K; c.moveTo(x, y); c.lineTo(x + K, y); }
    }
    c.strokeStyle = color;
    c.lineWidth = width / this.zoom * this.dpr;
    c.stroke();
  }

  drawHighlight(frame, key) {
    const w = this.w, c = this.ctx, nx = w.nx;
    const arr = frame.f[key];
    const tgt = frame.pols.findIndex((p) => p && p.uid === this.highlightUid);
    if (tgt <= 0) return;
    c.beginPath();
    for (const i of w.landIdx) {
      if (arr[i] !== tgt) continue;
      const col = i % nx, row = Math.floor(i / nx);
      const x = col * K, y = row * K;
      if (col + 1 >= nx || arr[i + 1] !== tgt) { c.moveTo(x + K, y); c.lineTo(x + K, y + K); }
      if (col === 0 || arr[i - 1] !== tgt) { c.moveTo(x, y); c.lineTo(x, y + K); }
      if (i + nx >= w.n || arr[i + nx] !== tgt) { c.moveTo(x, y + K); c.lineTo(x + K, y + K); }
      if (i - nx < 0 || arr[i - nx] !== tgt) { c.moveTo(x, y); c.lineTo(x + K, y); }
    }
    c.strokeStyle = '#ffffff';
    c.lineWidth = 2.2 / this.zoom * this.dpr;
    c.stroke();
  }

  drawRivers() {
    const w = this.w, c = this.ctx, q = w.f.discharge;
    c.fillStyle = 'rgba(70,140,215,0.85)';
    for (const i of w.landIdx) {
      if (q[i] < 400) continue;
      const s = Math.min(K, 1 + Math.log10(q[i] / 400) * 1.6);
      const col = i % w.nx, row = Math.floor(i / w.nx);
      c.fillRect(col * K + (K - s) / 2, row * K + (K - s) / 2, s, s);
    }
  }

  drawLabels(frame, opts) {
    const c = this.ctx;
    const pols = frame.pols.slice(1).filter((p) => p.capital >= 0 && p.cells >= 12);
    pols.sort((a, b) => b.cells - a.cells);
    const boxes = [];
    const dpr = this.dpr;
    c.textAlign = 'center';
    c.textBaseline = 'middle';
    for (const p of pols.slice(0, 60)) {
      const col = p.capital % this.w.nx, row = Math.floor(p.capital / this.w.nx);
      const x = this.panX + (col + 0.5) * K * this.zoom, y = this.panY + (row + 0.5) * K * this.zoom;
      if (x < 0 || y < 0 || x > this.cv.width || y > this.cv.height) continue;
      const fs = Math.round(Math.min(18, 9 + Math.sqrt(p.cells) / 3) * dpr);
      const sup = opts.isSuper?.(p.uid);
      const name = polityTitle(p.uid, p, opts.polMode?.(p.uid), sup, opts.customName?.(p.uid));
      c.font = `${p.cells > 300 ? 600 : 500} ${fs}px system-ui, sans-serif`;
      const tw = c.measureText(name).width;
      const box = [x - tw / 2 - 3, y - fs - 4, x + tw / 2 + 3, y + 2];
      if (boxes.some((b) => !(box[2] < b[0] || box[0] > b[2] || box[3] < b[1] || box[1] > b[3]))) continue;
      boxes.push(box);
      c.fillStyle = '#111';
      c.beginPath(); c.arc(x, y, 2.6 * dpr, 0, Math.PI * 2); c.fill();
      c.strokeStyle = 'rgba(255,255,255,0.85)'; c.lineWidth = 1.2 * dpr; c.stroke();
      c.lineWidth = 3 * dpr;
      c.strokeStyle = 'rgba(255,255,255,0.8)';
      c.strokeText(name, x, y - fs / 2 - 3);
      c.fillStyle = sup ? '#8a3a00' : '#16191d';
      c.fillText(name, x, y - fs / 2 - 3);
    }
  }
}

// ---------------- 图层定义 ----------------

function contColor(cmap, lo, hi, log) {
  const l = lut(cmap);
  return (v) => {
    if (v === null || v === undefined || Number.isNaN(v)) return null;
    let t;
    if (log) t = (Math.log10(Math.max(v, lo)) - Math.log10(lo)) / (Math.log10(hi) - Math.log10(lo));
    else t = (v - lo) / (hi - lo);
    const k = Math.max(0, Math.min(255, Math.round(t * 255))) * 3;
    return [l[k], l[k + 1], l[k + 2]];
  };
}

function polColorFromIdx(frame, idx) {
  if (idx === 0) return null;
  if (idx === SCATTERED) return SCAT;
  const p = frame.pols[idx];
  return p ? polityColor(p.uid) : null;
}

export function buildLayers(w) {
  const L = [];
  const D = w.desc;
  const add = (o) => { L.push(o); return o; };

  // ---- 静态(第1、2层) ----
  for (const fd of w.m.fields) {
    if (['land', 'area_km2', 'land_frac', 'coast'].includes(fd.id)) continue;
    const arr = w.f[fd.id];
    let group = fd.group;
    if (group === '承载') group = '承载(第2层)';
    if (fd.style === 'categorical') {
      const cols = fd.colors.map(hexToRgb);
      add({ id: 's_' + fd.id, name: fd.name, group, field: fd.id, desc: fd.desc,
        color: (i) => cols[arr[i]] || null,
        legend: { type: 'cat', items: fd.labels.map((l, k) => ({ label: l || '—', color: fd.colors[k], k })), field: fd.id } });
    } else {
      const log = fd.colormap.startsWith('log_') || fd.id === 'discharge' || fd.id === 'density';
      const lo = log ? Math.max(fd.range[0], fd.id === 'discharge' ? 10 : 0.01) : fd.range[0];
      const f = contColor(fd.colormap.replace('log_', ''), lo, fd.range[1], log);
      add({ id: 's_' + fd.id, name: fd.name, group, field: fd.id, desc: fd.desc,
        color: (i) => f(arr[i]), legend: { type: 'cont', cmap: fd.colormap.replace('log_', ''), lo, hi: fd.range[1], log, unit: fd.unit } });
    }
  }
  // 地形用更好看的分层设色
  const terr = L.find((l) => l.id === 's_elev');
  if (terr) { terr.name = '地形'; terr.group = '基础'; }

  // ---- 动态：人口(第3层) ----
  const area = w.f.area_km2;
  const dens = w.f.density;
  const fDens = contColor('magma', 0.01, 300, true);
  add({ id: 'd_dens', name: '人口密度', group: '人口(第3层)', dyn: true, desc: '演化中的实际人口密度',
    color: (i, fr) => fr.pop[i] > 0 ? fDens(fr.pop[i] / area[i]) : null,
    legend: { type: 'cont', cmap: 'magma', lo: 0.01, hi: 300, log: true, unit: '人/km²' } });
  const fPress = contColor('RdPu', 0, 1.2, false);
  add({ id: 'd_press', name: '人口压力', group: '人口(第3层)', dyn: true, desc: '实际人口 ÷ 第2层潜在承载人口',
    color: (i, fr) => fr.pop[i] > 0 && dens[i] > 0 ? fPress(fr.pop[i] / (dens[i] * area[i])) : null,
    legend: { type: 'cont', cmap: 'RdPu', lo: 0, hi: 1.2, unit: '' } });
  const fFam = contColor('viridis', 1, 60, true);
  add({ id: 'd_fam', name: '家族数', group: '人口(第3层)', dyn: true, desc: '格内家族数',
    color: (i, fr) => fr.f.fam[i] > 0 ? fFam(fr.f.fam[i]) : null,
    legend: { type: 'cont', cmap: 'viridis', lo: 1, hi: 60, log: true, unit: '个' } });
  const modeFd = D.mode;
  const modeCols = modeFd.colors.map(hexToRgb);
  add({ id: 'd_mode', name: '生计方式(演化)', group: '人口(第3层)', dyn: true, desc: '家族技能加权后的主导生计方式',
    color: (i, fr) => fr.f.fam[i] > 0 ? modeCols[fr.f.dmode[i]] : null,
    legend: { type: 'cat', items: modeFd.labels.map((l, k) => ({ label: l, color: modeFd.colors[k], k })) } });
  const fClim = contColor('BrBG', 0.6, 1.4, false);
  add({ id: 'd_clim', name: '气候波动', group: '人口(第3层)', dyn: true, desc: '当年产出乘数(干旱/丰年)',
    color: (i, fr) => fClim(0.4 + fr.f.clim[i] / 255 * 1.2),
    legend: { type: 'cont', cmap: 'BrBG', lo: 0.6, hi: 1.4, unit: '×' } });
  const traitIds = [['t_farm', '农耕技能'], ['t_herd', '放牧技能'], ['t_hunt', '狩猎技能'], ['t_aggr', '好战'], ['t_mob', '流动性'], ['t_loyal', '忠诚']];
  const fTr = contColor('viridis', 0, 1, false);
  for (const [id, nm] of traitIds) {
    add({ id: 'd_' + id, name: nm, group: '家族属性', dyn: true, desc: '格内家族属性的人口加权平均(每25年刷新)',
      color: (i, fr) => fr.f.fam[i] > 0 ? fTr(fr.f[id][i] / 255) : null,
      legend: { type: 'cont', cmap: 'viridis', lo: 0, hi: 1, unit: '' } });
  }
  // ---- 社会(第4层) ----
  add({ id: 'd_tribe', name: '部落', group: '部落(第4层)', dyn: true, borders: 'tribe', labels: false,
    desc: '格内人口最多的最高首领所统领的部落；灰色为零散小族',
    color: (i, fr) => polColorFromIdx(fr, fr.f.tribe[i]), legend: { type: 'polity' } });
  const fDepth = contColor('YlOrBr', 0, 5, false);
  add({ id: 'd_depth', name: '统治层级', group: '部落(第4层)', dyn: true, desc: '家族到最高首领的平均层级',
    color: (i, fr) => fr.f.fam[i] > 0 ? fDepth(fr.f.depth[i] / 20) : null,
    legend: { type: 'cont', cmap: 'YlOrBr', lo: 0, hi: 5, unit: '级' } });
  const fHate = contColor('OrRd', 0, 1.5, false);
  add({ id: 'd_hate', name: '仇恨热度', group: '部落(第4层)', dyn: true, desc: '格内家族仇恨值均值',
    color: (i, fr) => fr.f.fam[i] > 0 ? fHate(fr.f.hate[i] / 255 * 2) : null,
    legend: { type: 'cont', cmap: 'OrRd', lo: 0, hi: 1.5, unit: '' } });
  // ---- 政治(第5层) ----
  add({ id: 'd_top1', name: '政治地图', group: '政治(第5层)', dyn: true, borders: 'top1', labels: true,
    desc: '每格影响力第一的政权；条纹 = 与第二政权重叠争夺',
    color: (i, fr) => polColorFromIdx(fr, fr.f.top1[i]),
    stripe: (i, fr) => fr.f.top2[i] && fr.f.share[i] < 145 ? polColorFromIdx(fr, fr.f.top2[i]) : null,
    legend: { type: 'polity' } });
  add({ id: 'd_top2', name: '重叠政权', group: '政治(第5层)', dyn: true, borders: 'top1',
    desc: '影响力第二的政权(势力重叠区)',
    color: (i, fr) => fr.f.top2[i] ? polColorFromIdx(fr, fr.f.top2[i]) : null, legend: { type: 'polity' } });
  const fShare = contColor('plasma', 0.5, 1, false);
  add({ id: 'd_share', name: '控制强度', group: '政治(第5层)', dyn: true, borders: 'top1',
    desc: '第一政权影响力占前两名之和的比例；越低越处于争夺中',
    color: (i, fr) => fr.f.top1[i] ? fShare(fr.f.share[i] / 255) : null,
    legend: { type: 'cont', cmap: 'plasma', lo: 0.5, hi: 1, unit: '' } });

  // ---- 综合 ----
  add({ id: 'c_pol', name: '政治综合', group: '综合', dyn: true, borders: 'top1', labels: true,
    desc: '政权着色 × 人口明暗 + 边界 + 首府；条纹为重叠争夺区',
    color: (i, fr) => {
      const c = polColorFromIdx(fr, fr.f.top1[i]);
      if (!c) return null;
      const d = fr.pop[i] / area[i];
      const t = Math.max(0, Math.min(1, (Math.log10(Math.max(d, 0.01)) + 2) / 4.5));
      const k = 0.62 + 0.5 * t;
      return [c[0] * k, c[1] * k, c[2] * k];
    },
    stripe: (i, fr) => fr.f.top2[i] && fr.f.share[i] < 140 ? polColorFromIdx(fr, fr.f.top2[i]) : null,
    legend: { type: 'polity' } });
  add({ id: 'c_tribe', name: '部落综合', group: '综合', dyn: true, borders: 'tribe',
    desc: '部落着色 × 人口明暗 + 部落边界',
    color: (i, fr) => {
      const c = polColorFromIdx(fr, fr.f.tribe[i]);
      if (!c) return null;
      const d = fr.pop[i] / area[i];
      const k = 0.62 + 0.5 * Math.max(0, Math.min(1, (Math.log10(Math.max(d, 0.01)) + 2) / 4.5));
      return [c[0] * k, c[1] * k, c[2] * k];
    }, legend: { type: 'polity' } });
  add({ id: 'c_bivar', name: '资源 × 人口', group: '综合', dyn: true,
    desc: '列：承载力(低/中/高)；行：人口压力(低/中/高)。左下浅色=资源少人少；右上深色=资源多已饱和',
    color: (i, fr) => {
      const cap = dens[i];
      const cx = cap < 1 ? 0 : cap < 15 ? 1 : 2;
      const pr = dens[i] > 0 ? fr.pop[i] / (dens[i] * area[i]) : 0;
      const ry = pr < 0.3 ? 0 : pr < 0.7 ? 1 : 2;
      return BIVAR[ry][cx];
    }, legend: { type: 'bivar' } });
  add({ id: 'c_mode', name: '生计与政治边界', group: '综合', dyn: true, borders: 'top1', labels: true,
    desc: '演化后的生计方式着色，叠加政权边界：观察游牧-农耕分界与政治边界的关系',
    color: (i, fr) => fr.f.fam[i] > 0 ? modeCols[fr.f.dmode[i]] : null,
    legend: { type: 'cat', items: modeFd.labels.map((l, k) => ({ label: l, color: modeFd.colors[k], k })) } });
  return L;
}

export const GROUP_ORDER = ['综合', '政治(第5层)', '部落(第4层)', '人口(第3层)', '家族属性', '承载(第2层)', '资源', '水', '气候', '基础'];
