// 界面语言：英文(默认)与中文。原文(中文)即词条键，英文译文见 i18n_en.js。
//   t(s, ...args)  界面文本；{0} {1} 为占位符
//   tr(s)          服务端返回的文本(错误、进度、警告、字段说明)：先整句匹配，再按模式匹配，都没有则原样显示
//   translateDOM   翻译页面里的静态文字(文本节点与 placeholder / title / alt)
import { EN_DICT, EN_PATTERNS } from './i18n_en.js';

const KEY = 'mapsim.lang';

export const LANG = (() => {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'zh' || v === 'en') return v;
  } catch { /* 忽略 */ }
  return 'en';
})();
export const EN = LANG === 'en';
export const LOCALE = EN ? 'en-US' : 'zh-CN';

const fill = (s, a) => (a.length ? s.replace(/\{(\d+)\}/g, (_, i) => (a[+i] ?? '')) : s);

export function t(s, ...args) {
  return fill(EN ? (EN_DICT[s] ?? s) : s, args);
}

const PATTERNS = EN_PATTERNS.map(([re, en]) => [new RegExp('^' + re + '$', 's'), en]);

export function tr(s) {
  if (!EN || typeof s !== 'string' || !s) return s;
  const k = s.trim();
  if (EN_DICT[k] !== undefined) return EN_DICT[k];
  for (const [re, en] of PATTERNS) {
    const m = k.match(re);
    if (m) return en.replace(/\$(\d)/g, (_, i) => tr(m[+i] ?? ''));
  }
  return s;
}

const ATTRS = ['placeholder', 'title', 'alt'];
export function translateDOM(root = document.body) {
  if (!EN) return;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const raw = n.nodeValue;
    const k = raw.trim();
    if (k && EN_DICT[k] !== undefined) n.nodeValue = raw.replace(k, EN_DICT[k]);
  }
  const md = document.querySelector('meta[name="description"]');
  if (md && EN_DICT[md.content.trim()] !== undefined) md.content = EN_DICT[md.content.trim()];
  root.querySelectorAll('*').forEach((el) => {
    for (const a of ATTRS) {
      const v = el.getAttribute(a);
      if (v && EN_DICT[v.trim()] !== undefined) el.setAttribute(a, EN_DICT[v.trim()]);
    }
  });
}

// 页头的语言切换：选择后保存并刷新页面
export function mountLang(container) {
  document.documentElement.lang = EN ? 'en' : 'zh-CN';
  if (EN && EN_DICT[document.title]) document.title = EN_DICT[document.title];
  translateDOM(document.body);
  if (!container) return;
  const sel = document.createElement('select');
  sel.className = 'langsel';
  sel.title = EN ? 'Language' : '语言';
  sel.innerHTML = '<option value="en">English</option><option value="zh">中文</option>';
  sel.value = LANG;
  sel.onchange = () => {
    try { localStorage.setItem(KEY, sel.value); } catch { /* 忽略 */ }
    location.reload();
  };
  container.appendChild(sel);
}

// 数字与时间
export const fmtNum = (v, d = 0) => (v === undefined || v === null || Number.isNaN(v)) ? '—'
  : Number(v).toLocaleString(LOCALE, { maximumFractionDigits: d, minimumFractionDigits: d });
export const fmtTime = (s) => (s ? new Date(s).toLocaleString(LOCALE, { hour12: false }) : '');
// 人口：中文 万/亿，英文 k/M/B
export function fmtPop(p) {
  if (EN) return p >= 1e9 ? fmtNum(p / 1e9, 2) + 'B' : p >= 1e6 ? fmtNum(p / 1e6, 1) + 'M' : p >= 1e4 ? fmtNum(p / 1e3, 0) + 'k' : fmtNum(p);
  return p >= 1e8 ? fmtNum(p / 1e8, 2) + '亿' : p >= 1e4 ? fmtNum(p / 1e4, 1) + '万' : fmtNum(p);
}
