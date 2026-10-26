// 对外页面(首页、价格、登录、条款)的公共部分：导航、登录状态、站点信息、价格卡片
import { t, tr, EN, mountLang, fmtNum } from './i18n.js';
import { getMe, logout } from './api.js';

export const $ = (id) => document.getElementById(id);
export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

let siteP = null;
export function site() {
  if (!siteP) siteP = fetch('/v1/site').then((r) => r.json());
  return siteP;
}

// 导航右侧：未登录 → 登录 / 免费开始；已登录 → 控制台 / 退出
export async function initSite() {
  mountLang($('langSlot'));
  let me = { guest: true };
  try { me = await getMe(); } catch { /* 忽略 */ }
  const a = $('authSlot');
  if (a) {
    a.innerHTML = me.guest
      ? `<a class="btnlink ghost" href="/login.html">${t('登录')}</a><a class="btnlink primary" href="/login.html">${t('免费开始')}</a>`
      : `<a class="btnlink ghost" href="#" id="navLogout">${t('退出')}</a><a class="btnlink primary" href="/console.html">${t('控制台')}</a>`;
    $('navLogout')?.addEventListener('click', (e) => { e.preventDefault(); logout(); });
  }
  document.querySelectorAll('[data-cta]').forEach((el) => {
    if (!me.guest) { el.textContent = t('进入控制台'); el.href = '/console.html'; }
  });
  const s = await site();
  document.querySelectorAll('[data-site]').forEach((el) => {
    const k = el.dataset.site;
    const v = k === 'operator' ? (s.operator || s.name) : k === 'contact' ? (s.contact || '') : s.name;
    if (el.tagName === 'A' && k === 'contact') {
      if (v) { el.href = 'mailto:' + v; el.textContent = el.textContent.trim() ? el.textContent : v; } else el.hidden = true;
    } else el.textContent = v;
  });
  return { me, site: s };
}

const SYMBOL = { CNY: '¥', USD: '$', EUR: '€', GBP: '£', JPY: '¥' };

export function money(v, cur) {
  const sym = SYMBOL[cur] ?? (cur + ' ');
  return sym + fmtNum(v, v % 1 ? 2 : 0);
}

// 价格卡片：按月费从低到高；注册套餐标为"免费开始"，付费套餐暂时通过邮件开通
export function renderPlans(el, s, me) {
  const plans = [...s.plans].sort((a, b) => a.priceMonthly - b.priceMonthly);
  const mid = plans.length > 2 ? plans[1].name : '';
  el.innerHTML = plans.map((p) => {
    const free = p.priceMonthly <= 0;
    const items = [
      p.monthlyUnits < 0 ? t('不限算力点') : t('每月 {0} 算力点', fmtNum(p.monthlyUnits)),
      t('气候、水文、资源分析'),
      [t('历史演化：单次最长 {0} 年', fmtNum(p.maxYears)), p.noSimulate],
      [t('最多 {0} 个家族', fmtNum(p.maxFamilies)), p.noSimulate],
      t('上传地图最大 {0} MB', fmtNum(p.maxUploadMB)),
      t('同时进行 {0} 个任务', p.maxActiveJobs),
      [t('GeoTIFF / GeoJSON / 游戏引擎导出'), false],
      [t('接口(API)调用'), false],
    ];
    const li = items.map((x) => (Array.isArray(x) ? (x[1] ? `<li class="no">${x[0]}</li>` : `<li>${x[0]}</li>`) : `<li>${x}</li>`)).join('');
    let cta;
    if (free) cta = me.guest ? `<a class="btnlink primary big" href="/login.html">${t('免费开始')}</a>` : `<a class="btnlink big" href="/console.html">${t('进入控制台')}</a>`;
    else if (s.contact) cta = `<a class="btnlink ${p.name === mid ? 'primary' : ''} big" href="mailto:${esc(s.contact)}?subject=${encodeURIComponent(`${s.name} ${tr(p.label)}`)}">${t('联系开通')}</a>`;
    else cta = `<span class="btnlink big" style="opacity:.6">${t('即将开放')}</span>`;
    return `<div class="plan ${p.name === mid ? 'hl' : ''}">
      <div class="pname">${esc(tr(p.label) || p.name)}</div>
      <div class="price">${free ? t('免费') : money(p.priceMonthly, s.currency)}${free ? '' : `<small> / ${t('月')}</small>`}</div>
      <ul>${li}</ul>${cta}</div>`;
  }).join('');
}

export { t, EN };
