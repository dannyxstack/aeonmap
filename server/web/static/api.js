// 接口密钥与带认证的请求(回放界面与控制台共用)
import { t, tr } from './i18n.js';

const KEY_STORE = 'mapsim.key';

export function getKey() {
  const u = new URL(location.href);
  const fromUrl = u.searchParams.get('key');
  if (fromUrl) {
    try { localStorage.setItem(KEY_STORE, fromUrl); } catch { /* 忽略 */ }
    u.searchParams.delete('key');
    history.replaceState(null, '', u.toString()); // 不把密钥留在地址栏和历史记录里
    return fromUrl;
  }
  try { return localStorage.getItem(KEY_STORE) || ''; } catch { return ''; }
}

export function setKey(k) {
  try { localStorage.setItem(KEY_STORE, k); } catch { /* 忽略 */ }
}

export function clearKey() {
  try { localStorage.removeItem(KEY_STORE); } catch { /* 忽略 */ }
}

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function api(path, opts = {}) {
  const k = getKey();
  // 网页登录后靠会话 Cookie 认证；只有填了接口密钥(开发者)时才带 Authorization
  const headers = { ...(k ? { Authorization: `Bearer ${k}` } : {}), ...(opts.headers || {}) };
  let body = opts.body;
  if (body && !(body instanceof FormData) && typeof body !== 'string') {
    body = JSON.stringify(body);
    headers['Content-Type'] = 'application/json';
  }
  const r = await fetch(path, { ...opts, headers, body });
  if (!r.ok) {
    let code = 'http_error';
    let msg = `HTTP ${r.status}`;
    try { const e = (await r.json()).error; code = e.code; msg = tr(e.message); } catch { /* 非 JSON */ }
    throw new ApiError(r.status, code, msg);
  }
  if (opts.raw) return r;
  const ct = r.headers.get('Content-Type') || '';
  return ct.includes('json') ? r.json() : r.arrayBuffer();
}

// 带密钥的下载链接(浏览器直接下载时无法加请求头)
export function keyed(path) {
  const k = getKey();
  return k ? `${path}${path.includes('?') ? '&' : '?'}key=${encodeURIComponent(k)}` : path;
}

// 没有密钥时弹出输入框
export function askKey(message) {
  return new Promise((resolve) => {
    const d = document.createElement('dialog');
    d.className = 'keydlg';
    d.innerHTML = `<form method="dialog"><h2>${t('输入接口密钥')}</h2><p class="hint">${message || t('本地服务首次启动时，密钥保存在 data/local.key。')}</p>
      <input name="k" placeholder="msk_..." style="width:100%" autofocus><div class="dlgBtns"><button>${t('确定')}</button></div></form>`;
    document.body.appendChild(d);
    d.addEventListener('close', () => { const k = d.querySelector('input').value.trim(); setKey(k); d.remove(); resolve(k); });
    d.showModal();
  });
}

// 当前身份：已登录返回账户信息；未登录返回 {guest: true}。存着的密钥失效时清掉，退回会话/访客。
export async function getMe() {
  try {
    return await api('/v1/me');
  } catch (e) {
    if (e.status === 401 && getKey()) { clearKey(); return api('/v1/me'); }
    throw e;
  }
}

// 需要登录的页面：未登录时跳到登录页，登录后回到当前页面
export async function ensureLogin() {
  const me = await getMe();
  if (me.guest) {
    location.href = '/login.html?next=' + encodeURIComponent(location.pathname + location.search);
    await new Promise(() => {});
  }
  return me;
}

// 兼容旧调用
export const ensureKey = ensureLogin;

export async function logout() {
  clearKey();
  try { await fetch('/v1/auth/logout', { method: 'POST' }); } catch { /* 忽略 */ }
  location.href = '/';
}

// 短任务(AI 识图、色带识别)：提交后轮询直到完成，返回结果
export async function runTask(path, body) {
  let tk = await api(path, { method: 'POST', body });
  while (tk.status === 'running') {
    await new Promise((r) => setTimeout(r, 1500));
    tk = await api(`/v1/tasks/${tk.id}`);
  }
  if (tk.status !== 'succeeded') throw new ApiError(502, tk.errorCode || 'task_failed', tr(tk.error || ''));
  return tk.result;
}
