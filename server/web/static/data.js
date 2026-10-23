// 数据加载与解码：静态格子数据、动态帧、政权命名
import { api } from './api.js';

export class World {
  static async load(worldId) {
    const m = await api(`/v1/worlds/${worldId}/manifest`);
    const buf = await api(`/v1/worlds/${worldId}/cells.bin`);
    const w = new World();
    w.m = m;
    w.nx = m.grid.nx; w.ny = m.grid.ny; w.n = w.nx * w.ny;
    w.res = m.grid.res; w.west = m.grid.west; w.north = m.grid.north;
    w.f = {};
    w.desc = {};
    for (const fd of m.fields) {
      const arr = new Float32Array(w.n);
      if (fd.dtype === 'f32') {
        // 字段首尾相接存放：格子数不是 4 的倍数时，u8 字段之后的 f32 偏移不对齐，需要先拷出来
        const src = fd.offset % 4 === 0 ? new Float32Array(buf, fd.offset, w.n)
          : new Float32Array(buf.slice(fd.offset, fd.offset + 4 * w.n));
        arr.set(src);
      } else {
        const src = new Uint8Array(buf, fd.offset, w.n);
        for (let i = 0; i < w.n; i++) arr[i] = src[i];
      }
      w.f[fd.id] = arr;
      w.desc[fd.id] = fd;
    }
    w.landIdx = [];
    for (let i = 0; i < w.n; i++) if (w.f.land[i] > 0.5 && w.f.area_km2[i] > 0) w.landIdx.push(i);
    w.landIdx = Int32Array.from(w.landIdx);
    w.frameFields = m.frameFields;
    w.modes = m.modes;
    w.traitNames = m.traitNames;
    return w;
  }
  lonlat(i) {
    const r = Math.floor(i / this.nx), c = i % this.nx;
    return [this.west + (c + 0.5) * this.res, this.north - (r + 0.5) * this.res];
  }
}

// 解码帧：陆地格数组 → 全网格数组
export function decodeFrame(w, buf) {
  const dv = new DataView(buf);
  const magic = String.fromCharCode(dv.getUint8(0), dv.getUint8(1), dv.getUint8(2), dv.getUint8(3));
  if (magic !== 'MSF1') throw new Error('帧格式错误');
  const year = dv.getUint32(4, true);
  const nl = dv.getUint32(8, true);
  if (nl !== w.landIdx.length) throw new Error(`帧格数 ${nl} 与世界 ${w.landIdx.length} 不符`);
  const L = w.landIdx;
  let off = 12;
  const fr = { year, f: {} };
  for (const fd of w.frameFields) {
    let arr;
    if (fd.bytes === 2) {
      arr = new Uint16Array(w.n);
      for (let k = 0; k < nl; k++) arr[L[k]] = dv.getUint16(off + 2 * k, true);
    } else {
      arr = new Uint8Array(w.n);
      const src = new Uint8Array(buf, off, nl);
      for (let k = 0; k < nl; k++) arr[L[k]] = src[k];
    }
    off += fd.bytes * nl;
    fr.f[fd.id] = arr;
  }
  // 人口：对数量化还原
  const popQ = fr.f.pop;
  const pop = new Float32Array(w.n);
  for (let k = 0; k < nl; k++) { const i = L[k]; pop[i] = Math.pow(10, popQ[i] / 10000) - 1; }
  fr.pop = pop;
  const np = dv.getUint32(off, true); off += 4;
  fr.pols = [null];
  for (let k = 0; k < np; k++) {
    const uid = Number(dv.getBigUint64(off, true));
    fr.pols.push({
      uid,
      pop: dv.getFloat32(off + 8, true),
      families: dv.getUint32(off + 12, true),
      cells: dv.getUint32(off + 16, true),
      capital: dv.getInt32(off + 20, true),
      power: dv.getFloat32(off + 24, true),
    });
    off += 28;
  }
  return fr;
}

export const SCATTERED = 65535;

import { EN } from './i18n.js';

// ---- 政权命名(与 Go 端 sim/names.go 完全一致；英文界面用同一组音节的拼音) ----
const SYL = ['阿', '巴', '赤', '达', '额', '伏', '嘎', '赫', '伊', '嘉', '喀', '兰', '穆', '纳', '鄂', '帕', '齐', '柔',
  '萨', '塔', '乌', '瓦', '西', '雅', '扎', '昆', '图', '斡', '楚', '罗', '沙', '温', '铁', '白', '青', '玄',
  '金', '云', '河', '岚', '狼', '鹿', '鹰', '熊', '松', '桑', '羌', '夷', '孤', '丹', '苏', '莫', '宁', '安',
  '弥', '库', '勒', '诺', '珂', '琉', '曼', '黎', '焉', '疏', '龟', '姑', '高', '吐', '渤', '靺', '契', '室'];
const SYL_EN = ['a', 'ba', 'chi', 'da', 'e', 'fu', 'ga', 'he', 'yi', 'jia', 'ka', 'lan', 'mu', 'na', 'o', 'pa', 'qi', 'rou',
  'sa', 'ta', 'wu', 'wa', 'xi', 'ya', 'za', 'kun', 'tu', 'wo', 'chu', 'luo', 'sha', 'wen', 'tie', 'bai', 'qing', 'xuan',
  'jin', 'yun', 'hor', 'lam', 'lang', 'lu', 'ying', 'xiong', 'song', 'sang', 'qiang', 'yi', 'gu', 'dan', 'su', 'mo', 'ning', 'an',
  'mi', 'ku', 'le', 'nuo', 'ke', 'liu', 'man', 'li', 'yan', 'shu', 'qiu', 'gu', 'gao', 'tu', 'bo', 'mo', 'qi', 'shi'];
const M64 = (x) => BigInt.asUintN(64, x);
function mix(a, b) {
  let x = M64(a ^ M64(b + 0x9e3779b97f4a7c15n + M64(a << 6n) + (a >> 2n)));
  x ^= x >> 33n; x = M64(x * 0xff51afd7ed558ccdn);
  x ^= x >> 33n; x = M64(x * 0xc4ceb9fe1a85ec53n);
  x ^= x >> 33n;
  return x;
}
const NAME_CACHE = new Map();
export function baseName(uid) {
  let s = NAME_CACHE.get(uid);
  if (s) return s;
  const h = mix(BigInt(uid), 0xabcdefn);
  const n = 2 + Number(h % 3n) / 2 | 0;
  s = '';
  for (let k = 0; k < n; k++) {
    const i = Number((h >> BigInt(8 * (k + 1))) % BigInt(SYL.length));
    if (!EN) { s += SYL[i]; continue; }
    const y = SYL_EN[i];
    s += (s && /[aeiou]$/.test(s) && /^[aeiou]/.test(y) ? "'" : '') + y; // 拼音隔音符：Ba'e
  }
  if (EN) s = s[0].toUpperCase() + s.slice(1);
  NAME_CACHE.set(uid, s);
  return s;
}

// 后缀按规模与生计：部 / 部落联盟 / 汗国 / 王国 / 帝国
export function polityTitle(uid, p, modeSkill, isSuper = false, customName = '') {
  if (customName) return (isSuper ? '★' : '') + customName; // 用户约束指定的政权保留原名
  const b = baseName(uid);
  if (EN) {
    if (!p) return b + ' Tribe';
    const herd = modeSkill === 1;
    if (isSuper) return '★Great ' + b + (herd ? ' Khanate' : ' Empire');
    if (p.families >= 3000) return herd ? 'Great ' + b + ' Khanate' : b + ' Empire';
    if (p.families >= 600) return herd ? b + ' Khanate' : 'Kingdom of ' + b;
    if (p.families >= 120) return herd ? b + ' Confederacy' : 'Duchy of ' + b;
    return b + ' Tribe';
  }
  if (!p) return b + '部';
  const herd = modeSkill === 1;
  if (isSuper) return '★' + b + (herd ? '大汗国' : '大帝国');
  if (p.families >= 3000) return b + (herd ? '大汗国' : '帝国');
  if (p.families >= 600) return b + (herd ? '汗国' : '王国');
  if (p.families >= 120) return b + (herd ? '部落联盟' : '公国');
  return b + '部';
}
