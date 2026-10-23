// 色带与颜色工具

const STOPS = {
  viridis: ['#440154', '#482878', '#3e4a89', '#31688e', '#26828e', '#1f9e89', '#35b779', '#6ece58', '#b5de2b', '#fde725'],
  magma: ['#000004', '#180f3d', '#440f76', '#721f81', '#9e2f7f', '#cd4071', '#f1605d', '#fd9668', '#feca8d', '#fcfdbf'],
  inferno: ['#000004', '#1b0c41', '#4a0c6b', '#781c6d', '#a52c60', '#cf4446', '#ed6925', '#fb9b06', '#f7d13d', '#fcffa4'],
  plasma: ['#0d0887', '#46039f', '#7201a8', '#9c179e', '#bd3786', '#d8576b', '#ed7953', '#fb9f3a', '#fdca26', '#f0f921'],
  terrain: ['#333399', '#0294fa', '#00c66a', '#7ddd7e', '#fefe98', '#c5b77f', '#8c7062', '#a99692', '#d4cac8', '#ffffff'],
  RdBu_r: ['#053061', '#2166ac', '#4393c3', '#92c5de', '#d1e5f0', '#fddbc7', '#f4a582', '#d6604d', '#b2182b', '#67001f'],
  YlGnBu: ['#ffffd9', '#edf8b1', '#c7e9b4', '#7fcdbb', '#41b6c4', '#1d91c0', '#225ea8', '#253494', '#081d58'],
  PuOr: ['#2d004b', '#542788', '#8073ac', '#b2abd2', '#d8daeb', '#fee0b6', '#fdb863', '#e08214', '#b35806', '#7f3b08'],
  BrBG: ['#543005', '#8c510a', '#bf812d', '#dfc27d', '#f6e8c3', '#c7eae5', '#80cdc1', '#35978f', '#01665e', '#003c30'],
  Blues: ['#f7fbff', '#deebf7', '#c6dbef', '#9ecae1', '#6baed6', '#4292c6', '#2171b5', '#08519c', '#08306b'],
  YlGn: ['#ffffe5', '#f7fcb9', '#d9f0a3', '#addd8e', '#78c679', '#41ab5d', '#238443', '#006837', '#004529'],
  YlOrBr: ['#ffffe5', '#fff7bc', '#fee391', '#fec44f', '#fe9929', '#ec7014', '#cc4c02', '#993404', '#662506'],
  PuBu: ['#fff7fb', '#ece7f2', '#d0d1e6', '#a6bddb', '#74a9cf', '#3690c0', '#0570b0', '#045a8d', '#023858'],
  YlOrRd: ['#ffffcc', '#ffeda0', '#fed976', '#feb24c', '#fd8d3c', '#fc4e2a', '#e31a1c', '#bd0026', '#800026'],
  PuBuGn: ['#fff7fb', '#ece2f0', '#d0d1e6', '#a6bddb', '#67a9cf', '#3690c0', '#02818a', '#016c59', '#014636'],
  Greens: ['#f7fcf5', '#e5f5e0', '#c7e9c0', '#a1d99b', '#74c476', '#41ab5d', '#238b45', '#006d2c', '#00441b'],
  GnBu: ['#f7fcf0', '#e0f3db', '#ccebc5', '#a8ddb5', '#7bccc4', '#4eb3d3', '#2b8cbe', '#0868ac', '#084081'],
  Greys: ['#ffffff', '#f0f0f0', '#d9d9d9', '#bdbdbd', '#969696', '#737373', '#525252', '#252525', '#000000'],
  OrRd: ['#fff7ec', '#fee8c8', '#fdd49e', '#fdbb84', '#fc8d59', '#ef6548', '#d7301f', '#b30000', '#7f0000'],
  RdPu: ['#fff7f3', '#fde0dd', '#fcc5c0', '#fa9fb5', '#f768a1', '#dd3497', '#ae017e', '#7a0177', '#49006a'],
};

function hex(h) {
  const n = parseInt(h.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

const LUT_CACHE = {};
// 返回 256 级 RGB 查找表(Uint8Array 长度 768)
export function lut(name) {
  if (LUT_CACHE[name]) return LUT_CACHE[name];
  let rev = false;
  let base = name.replace(/^log_/, '');
  if (base.endsWith('_r') && !STOPS[base]) { rev = true; base = base.slice(0, -2); }
  const stops = (STOPS[base] || STOPS.viridis).map(hex);
  if (rev) stops.reverse();
  const out = new Uint8Array(768);
  for (let i = 0; i < 256; i++) {
    const t = i / 255 * (stops.length - 1);
    const k = Math.min(Math.floor(t), stops.length - 2);
    const f = t - k;
    for (let c = 0; c < 3; c++) out[i * 3 + c] = Math.round(stops[k][c] * (1 - f) + stops[k + 1][c] * f);
  }
  LUT_CACHE[name] = out;
  return out;
}

export function lutCss(name, t) {
  const l = lut(name);
  const i = Math.max(0, Math.min(255, Math.round(t * 255))) * 3;
  return `rgb(${l[i]},${l[i + 1]},${l[i + 2]})`;
}

export const hexToRgb = hex;

// 由政权 UID 确定性生成颜色(黄金角色相 + 明度/饱和度轮换)
const COLOR_CACHE = new Map();
export function polityColor(uid) {
  let c = COLOR_CACHE.get(uid);
  if (c) return c;
  const u = Number(BigInt.asUintN(32, BigInt(uid) * 2654435761n));
  const h = (u % 360);
  const s = 45 + ((u >> 9) % 4) * 10;
  const l = 42 + ((u >> 13) % 4) * 8;
  c = hslToRgb(h / 360, s / 100, l / 100);
  COLOR_CACHE.set(uid, c);
  return c;
}

export function hslToRgb(h, s, l) {
  const f = (n) => {
    const k = (n + h * 12) % 12;
    const a = s * Math.min(l, 1 - l);
    return Math.round(255 * (l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1))));
  };
  return [f(0), f(8), f(4)];
}

// 3x3 双变量色板(行：人口压力 低→高，列：承载力 低→高)
export const BIVAR = [
  ['#e8e8e8', '#b5c0da', '#6c83b5'],
  ['#b8d6be', '#90b2b3', '#567994'],
  ['#73ae80', '#5a9178', '#2a5a5b'],
].map((r) => r.map(hex));
