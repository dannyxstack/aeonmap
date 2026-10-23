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
  const headers = { Authorization: `Bearer ${getKey()}`, ...(opts.headers || {}) };
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
  return `${path}${path.includes('?') ? '&' : '?'}key=${encodeURIComponent(getKey())}`;
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

export async function ensureKey() {
  let k = getKey();
  for (;;) {
    if (!k) k = await askKey();
    try {
      const me = await api('/v1/me');
      return me;
    } catch (e) {
      if (e.status === 401) { clearKey(); k = await askKey(t('密钥无效，请重新输入。')); continue; }
      throw e;
    }
  }
}
