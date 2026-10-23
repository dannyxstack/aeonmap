// 管理后台：独立的管理员令牌(data/admin.key)，与用户接口密钥分开保存
import { t, tr, EN, LOCALE, mountLang } from './i18n.js';

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmtT = (v) => v ? new Date(v).toLocaleString(LOCALE, { hour12: false }) : '—';
const fmtN = (n) => Number(n || 0).toLocaleString(LOCALE);
const fmtB = (b) => b > 1e9 ? (b / 1e9).toFixed(2) + ' GB' : b > 1e6 ? (b / 1e6).toFixed(1) + ' MB' : (b / 1e3).toFixed(0) + ' KB';
const L = (m) => Object.fromEntries(Object.entries(m).map(([k, v]) => [k, t(v)]));
const STATUS = L({ queued: '排队', running: '运行中', succeeded: '完成', failed: '失败', cancelled: '已取消' });
const planLabel = (p, k) => tr(p?.label || k); // 默认套餐名(免费版/专业版…)可翻译；管理员改过的显示原样
const TOKEN = 'mapsim.adminToken';

let token = '';
try { token = localStorage.getItem(TOKEN) || ''; } catch { /* 忽略 */ }
const S = { settings: null, tenants: [], tab: 'overview' };

async function api(path, opts = {}) {
  const headers = { Authorization: `Bearer ${token}` };
  let body = opts.body;
  if (body && typeof body !== 'string') { body = JSON.stringify(body); headers['Content-Type'] = 'application/json'; }
  const r = await fetch(path, { ...opts, headers, body });
  if (r.status === 401) { await login(t('令牌无效，请重新输入。')); return api(path, opts); }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(tr(data.error?.message) || `HTTP ${r.status}`);
  return data;
}

function login(msg) {
  return new Promise((resolve) => {
    const d = document.createElement('dialog');
    d.className = 'keydlg';
    d.innerHTML = `<form method="dialog"><h2>${t('管理员登录')}</h2><p class="hint">${msg || t('管理员令牌保存在服务器的 data/admin.key(也可用环境变量 MAPSIM_ADMIN_TOKEN 指定)。')}</p>
      <input type="password" placeholder="mad_..." style="width:100%" autofocus><div class="dlgBtns"><button>${t('登录')}</button></div></form>`;
    document.body.appendChild(d);
    d.addEventListener('close', () => {
      token = d.querySelector('input').value.trim();
      try { localStorage.setItem(TOKEN, token); } catch { /* 忽略 */ }
      d.remove();
      resolve();
    });
    d.showModal();
  });
}

// ---------------- 概览 ----------------
function stackedBars(series, keys, colors) {
  const W = 760, H = 200, pad = 28;
  const max = Math.max(1, ...series.map((d) => keys.reduce((s, k) => s + d[k], 0)));
  const bw = (W - pad) / series.length;
  let g = '';
  series.forEach((d, i) => {
    let y = H - 18;
    keys.forEach((k, j) => {
      const h = d[k] / max * (H - 30);
      if (h > 0) g += `<rect x="${pad + i * bw + 1}" y="${y - h}" width="${Math.max(bw - 2, 1)}" height="${h}" fill="${colors[j]}"><title>${d.date} ${k} ${d[k]}</title></rect>`;
      y -= h;
    });
    if (i % Math.ceil(series.length / 8) === 0) g += `<text x="${pad + i * bw}" y="${H - 4}">${d.date.slice(5)}</text>`;
  });
  g += `<text x="0" y="12">${fmtN(max)}</text><line x1="${pad}" x2="${W}" y1="${H - 18}" y2="${H - 18}" stroke="#2e3540"/>`;
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${g}</svg>`;
}

async function renderOverview() {
  const o = await api('/admin/v1/overview?days=30');
  $('gen').textContent = t('更新于 {0}', fmtT(o.generated));
  const tn = o.tenants;
  const plans = S.settings?.plans || {};
  const byPlan = Object.entries(tn.byPlan).map(([k, v]) => `${esc(planLabel(plans[k], k))} ${v}`).join(' · ');
  const kind = o.usage.byKind;
  $('tab-overview').innerHTML = `
    <div class="kpis">
      <div class="kpi"><div class="v">${fmtN(tn.total)}</div><div class="l">${t('注册用户')}</div><div class="s">${t('本月新增 {0} · 停用 {1}', tn.newThisMonth, tn.suspended)}</div></div>
      <div class="kpi"><div class="v">${fmtN(tn.paid)}</div><div class="l">${t('付费用户')}</div><div class="s">${byPlan || '—'}</div></div>
      <div class="kpi"><div class="v">${o.revenue.currency === 'CNY' ? '¥' : ''}${fmtN(o.revenue.mrr)}</div><div class="l">${t('预估月收入(按套餐月费)')}</div><div class="s">${t('尚未接入支付，仅按当前套餐估算')}</div></div>
      <div class="kpi"><div class="v">${fmtN(tn.active30d)}</div><div class="l">${t('30 天活跃用户')}</div><div class="s">${t('有调用接口或提交任务')}</div></div>
      <div class="kpi"><div class="v">${fmtN(o.usage.units)}</div><div class="l">${t('本月算力点')}</div><div class="s">${t('预处理 {0} · 演化 {1} · 导出 {2}', kind.prep || 0, kind.simulate || 0, kind.export || 0)}${kind.ai ? ' · ' + t('AI 识图 {0}', kind.ai) : ''}</div></div>
      <div class="kpi"><div class="v">${fmtN(o.jobs.last24h)}</div><div class="l">${t('24 小时任务')}</div><div class="s">${t('失败 {0} · 运行中 {1} · 排队 {2}', o.jobs.failed24h, o.running?.length || 0, o.jobs.byStatus.queued || 0)}</div></div>
      <div class="kpi"><div class="v">${fmtN(o.runs)}</div><div class="l">${t('演化总数')}</div><div class="s">${t('用户上传世界 {0}', o.worlds.uploads)}</div></div>
      <div class="kpi"><div class="v">${fmtB(o.storage.dataBytes + o.storage.cacheBytes)}</div><div class="l">${t('存储占用')}</div><div class="s">${t('数据 {0} · 缓存 {1}', fmtB(o.storage.dataBytes), fmtB(o.storage.cacheBytes))}</div></div>
    </div>
    <div class="cols2" style="margin-top:14px">
      <div class="panel"><h2 style="margin-top:0">${t('近 30 天算力点')}</h2>
        <div class="legend-inline"><span><i style="background:#5aa0d8"></i>${t('演化')}</span><span><i style="background:#e0a84a"></i>${t('预处理')}</span><span><i style="background:#7fd6a0"></i>${t('导出')}</span></div>
        ${stackedBars(o.series, ['simulate', 'prep', 'export'], ['#5aa0d8', '#e0a84a', '#7fd6a0'])}
        <h2>${t('近 30 天任务与注册')}</h2>
        <div class="legend-inline"><span><i style="background:#8b95a3"></i>${t('任务')}</span><span><i style="background:#d8584a"></i>${t('失败')}</span><span><i style="background:#c08ad8"></i>${t('新用户')}</span></div>
        ${stackedBars(o.series.map((d) => ({ ...d, ok: d.jobs - d.failed })), ['ok', 'failed', 'signups'], ['#8b95a3', '#d8584a', '#c08ad8'])}
      </div>
      <div class="panel"><h2 style="margin-top:0">${t('本月用量前 10')}</h2>
        <table class="jobs"><tbody>${(o.usage.top || []).map((x) => `<tr class="click" data-tenant="${x.id}"><td>${esc(tr(x.name))}<div class="muted">${esc(planLabel(plans[x.plan], x.plan))}</div></td><td style="text-align:right">${t('{0} 点', fmtN(x.units))}</td></tr>`).join('') || `<tr><td class="muted">${t('本月暂无用量')}</td></tr>`}</tbody></table>
        <h2>${t('正在运行的演化')}</h2>
        ${(o.running || []).map((r) => `<div class="muted">${esc(r.run)}${EN ? ': ' : '：'}${t('第 {0} / {1} 年', r.year, r.years)}</div>`).join('') || `<div class="muted">${t('无')}</div>`}
      </div>
    </div>`;
}

// ---------------- 用户 ----------------
async function renderTenants() {
  S.tenants = await api('/admin/v1/tenants');
  const plans = S.settings.plans;
  const sec = $('tab-tenants');
  if (!sec.dataset.init) {
    sec.dataset.init = 1;
    sec.innerHTML = `<div class="toolbar">
      <input id="tq" placeholder="${t('搜索名称 / 邮箱 / ID')}" style="min-width:240px">
      <select id="tplan"><option value="">${t('全部套餐')}</option>${Object.values(plans).map((p) => `<option value="${p.name}">${esc(planLabel(p, p.name))}</option>`).join('')}</select>
      <select id="tflag"><option value="">${t('全部')}</option><option value="paid">${t('付费')}</option><option value="free">${t('未付费')}</option><option value="suspended">${t('已停用')}</option></select>
      <div class="spacer"></div><button id="btnNewTenant">${t('新建用户')}</button></div>
      <div class="panel"><table class="jobs" id="ttable"><thead><tr><th>${t('用户')}</th><th>${t('套餐')}</th><th>${t('本月用量 / 额度')}</th><th>${t('世界 / 演化 / 任务')}</th><th>${t('最近活动')}</th><th>${t('注册')}</th></tr></thead><tbody></tbody></table></div>`;
    ['tq', 'tplan', 'tflag'].forEach((id) => $(id).addEventListener('input', drawTenants));
    $('btnNewTenant').onclick = openNewTenant;
  }
  drawTenants();
}

function drawTenants() {
  const q = $('tq').value.trim().toLowerCase();
  const plan = $('tplan').value;
  const flag = $('tflag').value;
  const rows = S.tenants.filter((x) => x.id !== 'public')
    .filter((x) => !q || [x.name, x.email, x.id].some((v) => (v || '').toLowerCase().includes(q)))
    .filter((x) => !plan || x.plan === plan)
    .filter((x) => !flag || (flag === 'paid' && x.paid && !x.suspended) || (flag === 'free' && !x.paid) || (flag === 'suspended' && x.suspended));
  $('ttable').querySelector('tbody').innerHTML = rows.map((tn) => {
    const q2 = tn.quota < 0 ? t('不限') : fmtN(tn.quota);
    const pct = tn.quota > 0 ? Math.min(100, tn.usedMonth / tn.quota * 100) : 0;
    const last = [tn.lastSeen, tn.lastJob].filter(Boolean).sort().pop();
    const src = { cli: '命令行', admin: '后台', signup: '自助注册' }[tn.source];
    return `<tr class="click" data-tenant="${tn.id}"><td>${esc(tr(tn.name))} ${tn.suspended ? `<span class="tag failed">${t('停用')}</span>` : ''}<div class="muted">${esc(tn.email || '')} ${tn.id}</div></td>
      <td><span class="pill ${tn.paid ? 'paid' : ''}">${esc(tr(tn.planLabel || tn.plan))}</span>${tn.paid ? `<div class="muted">${t('¥{0}/月', fmtN(tn.price))}</div>` : ''}</td>
      <td>${fmtN(tn.usedMonth)} / ${q2}${tn.quotaCustom ? ` <span class="muted">${t('(单独)')}</span>` : ''}<div class="bar" style="width:120px;height:5px;background:var(--panel2);border-radius:3px;margin-top:4px"><i style="display:block;height:100%;width:${pct}%;background:${pct > 90 ? '#d8584a' : 'var(--accent)'};border-radius:3px"></i></div></td>
      <td>${tn.worlds} / ${tn.runs} / ${tn.jobs}${tn.failedJobs ? ` <span class="err">${t('({0} 失败)', tn.failedJobs)}</span>` : ''}</td>
      <td>${fmtT(last)}</td><td>${fmtT(tn.created)}<div class="muted">${esc(src ? t(src) : '')}</div></td></tr>`;
  }).join('') || `<tr><td colspan="6" class="muted">${t('没有匹配的用户')}</td></tr>`;
}

async function openTenant(id) {
  const d = await api(`/admin/v1/tenants/${id}`);
  const tn = d.tenant;
  const plans = S.settings.plans;
  const months = Object.entries(d.usageByMonth).sort().slice(-12);
  const maxM = Math.max(1, ...months.map((m) => m[1]));
  $('tenantBody').innerHTML = `
    <div class="rhead" style="margin:-16px -16px 12px"><span>${esc(tr(tn.name))} <span class="muted">${tn.id}</span></span><button class="ghost" id="tdClose">✕</button></div>
    <div id="newKeyBox"></div>
    <div class="cols2">
      <form class="form" id="tdForm">
        <label>${t('名称')}</label><input name="name" value="${esc(tn.name)}">
        <label>${t('邮箱')}</label><input name="email" value="${esc(tn.email || '')}">
        <label>${t('套餐')}</label><select name="plan">${Object.values(plans).map((p) => `<option value="${p.name}" ${p.name === tn.plan ? 'selected' : ''}>${esc(planLabel(p, p.name))}${p.priceMonthly ? ` · ${t('¥{0}/月', p.priceMonthly)}` : ''}</option>`).join('')}</select>
        <label>${t('单独额度')}</label><input name="quota" type="number" placeholder="${t('留空 = 按套餐；-1 = 不限')}" value="${tn.quotaCustom ? tn.quota : ''}">
        <label>${t('备注')}</label><input name="note" value="${esc(tn.note || '')}">
        <label>${t('状态')}</label><label style="color:var(--text)"><input type="checkbox" name="suspended" style="width:auto" ${tn.suspended ? 'checked' : ''}> ${t('停用(所有密钥立即失效)')}</label>
        <div class="full dlgBtns"><button type="button" class="ghost" id="tdDelete" style="color:#e88;border-color:#5a3030">${t('删除用户')}</button><button type="submit">${t('保存')}</button></div>
      </form>
      <div>
        <h2 style="margin-top:0">${t('本月 {0} / {1} 点', fmtN(tn.usedMonth), tn.quota < 0 ? t('不限') : fmtN(tn.quota))}</h2>
        <div class="bars" style="margin-bottom:22px">${months.map(([m, v]) => `<div style="height:${v / maxM * 100}%" title="${m} ${t('{0} 点', v)}"><span>${m.slice(2)}</span></div>`).join('') || `<span class="muted">${t('暂无用量')}</span>`}</div>
        <h2>${t('接口密钥')} <button class="ghost" id="tdAddKey" style="font-size:12px">${t('新增密钥')}</button></h2>
        ${d.keys.map((k) => `<div>${esc(k.prefix)}… <span class="muted">${fmtT(k.created)}</span> ${k.revoked ? `<span class="tag failed">${t('已吊销')}</span>` : `<button class="ghost" data-revoke="${esc(k.prefix)}" style="font-size:11px;padding:2px 8px">${t('吊销')}</button>`}</div>`).join('') || `<div class="muted">${t('无')}</div>`}
      </div>
    </div>
    <h2>${t('最近任务')}</h2>
    <table class="jobs"><tbody>${d.jobs.map((j) => `<tr><td>${j.id}<div class="muted">${fmtT(j.created)}</div></td><td><span class="tag ${j.status}">${STATUS[j.status] || j.status}</span>${j.error ? `<div class="err">${esc(tr(j.error).slice(0, 200))}</div>` : ''}</td><td>${t('{0} 点', j.units)}</td><td>${j.result?.runId ? `<span class="muted">${j.result.runId}</span>` : ''}</td></tr>`).join('') || `<tr><td class="muted">${t('无')}</td></tr>`}</tbody></table>
    <h2>${t('世界({0}) · 演化({1})', d.worlds.length, d.runs.length)}</h2>
    <div class="muted">${d.worlds.map((w) => `${esc(tr(w.name))}(${esc(w.input)}${EN ? ', ' : '，'}${esc(w.status)})`).join(EN ? ', ' : '、') || t('无')}</div>`;
  $('tdClose').onclick = () => $('tenantDlg').close();
  $('tdForm').onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target;
    const patch = { name: f.name.value, email: f.email.value, plan: f.plan.value, note: f.note.value, suspended: f.suspended.checked,
      quotaOverride: f.quota.value === '' ? -2 : parseInt(f.quota.value, 10) };
    try { await api(`/admin/v1/tenants/${id}`, { method: 'PATCH', body: patch }); $('tenantDlg').close(); refresh(); } catch (err) { alert(err.message); }
  };
  $('tdDelete').onclick = async () => {
    const typed = prompt(t('删除用户「{0}」及其全部世界、演化与任务，不可恢复。\n请输入用户名称确认：', tn.name));
    if (typed !== tn.name) return;
    try { await api(`/admin/v1/tenants/${id}`, { method: 'DELETE' }); $('tenantDlg').close(); refresh(); } catch (err) { alert(err.message); }
  };
  $('tdAddKey').onclick = async () => {
    if (!confirm(t('为该用户新增一把接口密钥？'))) return;
    const r = await api(`/admin/v1/tenants/${id}/keys`, { method: 'POST' });
    await openTenant(id);
    $('newKeyBox').innerHTML = `<div class="newkey">${t('新密钥(只显示这一次，请转交用户)：')}<br><code>${esc(r.key)}</code></div>`;
  };
  $('tenantBody').querySelectorAll('[data-revoke]').forEach((b) => b.onclick = async () => {
    if (!confirm(t('吊销密钥 {0}…？使用它的程序会立即失败。', b.dataset.revoke))) return;
    await api(`/admin/v1/tenants/${id}/keys/${b.dataset.revoke}`, { method: 'DELETE' });
    openTenant(id);
  });
  if (!$('tenantDlg').open) $('tenantDlg').showModal();
}

function openNewTenant() {
  const f = $('newTenantForm');
  f.plan.innerHTML = Object.values(S.settings.plans).map((p) => `<option value="${p.name}">${esc(planLabel(p, p.name))}</option>`).join('');
  f.plan.value = S.settings.signupPlan;
  $('newTenantDlg').showModal();
}

async function createTenant() {
  const f = $('newTenantForm');
  try {
    const r = await api('/admin/v1/tenants', { method: 'POST', body: { name: f.name.value, email: f.email.value, plan: f.plan.value, note: f.note.value } });
    f.reset();
    await refresh();
    await openTenant(r.tenant.id);
    $('newKeyBox').innerHTML = `<div class="newkey">${t('用户已创建。接口密钥(只显示这一次，请转交用户)：')}<br><code>${esc(r.key)}</code></div>`;
  } catch (e) { alert(e.message); }
}

// ---------------- 任务 ----------------
async function renderJobs() {
  const sec = $('tab-jobs');
  if (!sec.dataset.init) {
    sec.dataset.init = 1;
    sec.innerHTML = `<div class="toolbar"><select id="jstatus"><option value="">${t('全部状态')}</option>${Object.entries(STATUS).map(([k, v]) => `<option value="${k}">${v}</option>`).join('')}</select>
      <select id="jtenant"><option value="">${t('全部用户')}</option></select></div>
      <div class="panel"><table class="jobs" id="jtable"><thead><tr><th>${t('任务')}</th><th>${t('用户')}</th><th>${t('世界')}</th><th>${t('状态')}</th><th>${t('算力点')}</th><th></th></tr></thead><tbody></tbody></table></div>`;
    $('jstatus').onchange = renderJobs;
    $('jtenant').onchange = renderJobs;
  }
  const sel = $('jtenant');
  const cur = sel.value;
  sel.innerHTML = `<option value="">${t('全部用户')}</option>` + S.tenants.map((x) => `<option value="${x.id}">${esc(tr(x.name))}</option>`).join('');
  sel.value = cur;
  const q = new URLSearchParams({ status: $('jstatus').value, tenant: sel.value, limit: 300 });
  const jobs = await api('/admin/v1/jobs?' + q);
  $('jtable').querySelector('tbody').innerHTML = jobs.map((j) => {
    const req = j.request || {};
    const what = [req.simulate ? t('演化 {0}年', req.simulate.years || '') : '', req.export ? t('导出') : '', !req.simulate && !req.export ? t('预处理') : ''].filter(Boolean).join(' + ');
    return `<tr><td>${esc(what)}<div class="muted">${j.id} · ${fmtT(j.created)}</div></td><td>${esc(tr(j.tenantName))}</td><td class="muted">${esc(j.world)}</td>
      <td><span class="tag ${j.status}">${STATUS[j.status] || j.status}</span> ${j.status === 'running' ? Math.round(j.progress * 100) + '% ' + esc(tr(j.message || '')) : ''}${j.error ? `<div class="err">${esc(tr(j.error).slice(0, 160))}</div>` : ''}</td>
      <td>${j.units}${j.estimate && j.status !== 'succeeded' ? `<span class="muted"> / ${t('约 {0}', j.estimate)}</span>` : ''}</td>
      <td>${j.status === 'running' || j.status === 'queued' ? `<button class="ghost" data-cancel="${j.id}">${t('取消')}</button>` : ''}</td></tr>`;
  }).join('') || `<tr><td colspan="6" class="muted">${t('没有任务')}</td></tr>`;
  $('jtable').querySelectorAll('[data-cancel]').forEach((b) => b.onclick = async () => {
    try { await api(`/admin/v1/jobs/${b.dataset.cancel}`, { method: 'DELETE' }); setTimeout(renderJobs, 800); } catch (e) { alert(e.message); }
  });
}

// ---------------- 配置 ----------------
function renderSettings() {
  const st = S.settings;
  const rows = Object.values(st.plans).sort((a, b) => a.priceMonthly - b.priceMonthly);
  const field = (p, k, type = 'number') => `<input data-plan="${p.name}" data-k="${k}" type="${type}" value="${esc(p[k])}" ${p.name === 'internal' && k === 'name' ? 'readonly' : ''}>`;
  $('tab-settings').innerHTML = `
    <div class="panel"><h2 style="margin-top:0">${t('套餐')} <span class="muted">${t('每月额度 -1 表示不限；月费 > 0 的套餐计为付费用户')}</span></h2>
      <table class="jobs plans"><thead><tr><th>${t('标识')}</th><th>${t('显示名')}</th><th>${t('月费')}</th><th>${t('每月算力点')}</th><th>${t('单次最长年数')}</th><th>${t('家族上限')}</th><th>${t('上传上限 MB')}</th><th></th></tr></thead>
      <tbody>${rows.map((p) => `<tr><td>${field(p, 'name', 'text')}</td><td>${field(p, 'label', 'text')}</td><td>${field(p, 'priceMonthly')}</td><td>${field(p, 'monthlyUnits')}</td><td>${field(p, 'maxYears')}</td><td>${field(p, 'maxFamilies')}</td><td>${field(p, 'maxUploadMB')}</td>
        <td>${p.name !== 'internal' ? `<button class="ghost" data-delplan="${p.name}" style="font-size:11px">${t('删除')}</button>` : ''}</td></tr>`).join('')}</tbody></table>
      <button class="ghost" id="addPlan" style="margin-top:8px">${t('新增套餐')}</button>
    </div>
    <div class="panel" style="margin-top:14px"><h2 style="margin-top:0">${t('计价与接口')}</h2>
      <div class="form">
        <label>${t('预处理计价')}</label><div>${t('每 {0} 个陆地格 = 1 点', `<input id="pPrep" type="number" value="${st.pricing.prepCellsPerUnit}" style="width:120px">`)}</div>
        <label>${t('演化计价')}</label><div>${t('每 {0} 陆地格×年 = 1 点', `<input id="pSim" type="number" value="${st.pricing.simCellYearsPerUnit}" style="width:140px">`)} <span class="muted">${t('(欧亚 2.9 万陆地格 × 1000 年约 {0} 点)', Math.ceil(28969 * 1000 / st.pricing.simCellYearsPerUnit))}</span></div>
        <label>${t('导出计价')}</label><div><input id="pExp" type="number" value="${st.pricing.exportUnits}" style="width:80px"> ${t('点/次')}</div>
        <label>${t('货币')}</label><input id="pCur" value="${esc(st.currency)}" style="width:100px">
        <label>${t('自助注册')}</label><div><label style="color:var(--text)"><input type="checkbox" id="pSignup" ${st.allowSignup ? 'checked' : ''} style="width:auto"> ${t('开放 POST /v1/signup')}</label>
          ${t('新用户套餐')} <select id="pSignupPlan">${Object.values(st.plans).map((p) => `<option value="${p.name}" ${p.name === st.signupPlan ? 'selected' : ''}>${esc(planLabel(p, p.name))}</option>`).join('')}</select>
          <span class="muted">${t('每个 IP 每小时最多 5 次')}</span></div>
        <label>${t('内网回调')}</label><label style="color:var(--text)"><input type="checkbox" id="pPriv" ${st.allowPrivateWebhooks ? 'checked' : ''} style="width:auto"> ${t('允许回调到内网/本机地址(仅本地开发，生产环境请关闭以防 SSRF)')}</label>
      </div>
    </div>
    <div style="margin-top:14px;display:flex;gap:10px;align-items:center"><button id="saveSettings">${t('保存配置')}</button><span id="saveMsg" class="muted"></span></div>`;
  $('addPlan').onclick = () => {
    const name = prompt(t('新套餐标识(英文，如 team)'));
    if (!name) return;
    st.plans[name] = { name, label: name, monthlyUnits: 5000, maxYears: 10000, maxFamilies: 100000, maxUploadMB: 200, priceMonthly: 299 };
    renderSettings();
  };
  $('tab-settings').querySelectorAll('[data-delplan]').forEach((b) => b.onclick = () => { delete st.plans[b.dataset.delplan]; renderSettings(); });
  $('saveSettings').onclick = saveSettings;
}

async function saveSettings() {
  const st = structuredClone(S.settings);
  const plans = {};
  const rename = {};
  document.querySelectorAll('#tab-settings [data-plan]').forEach((el) => {
    const old = el.dataset.plan;
    const p = plans[old] || { ...st.plans[old] };
    p[el.dataset.k] = el.type === 'number' ? Number(el.value) : el.value.trim();
    plans[old] = p;
  });
  for (const [old, p] of Object.entries(plans)) { rename[old] = p.name; }
  st.plans = Object.fromEntries(Object.values(plans).map((p) => [p.name, p]));
  st.pricing = { prepCellsPerUnit: Number($('pPrep').value), simCellYearsPerUnit: Number($('pSim').value), exportUnits: Number($('pExp').value) };
  st.currency = $('pCur').value.trim() || 'CNY';
  st.allowSignup = $('pSignup').checked;
  st.signupPlan = rename[$('pSignupPlan').value] || $('pSignupPlan').value;
  st.allowPrivateWebhooks = $('pPriv').checked;
  try {
    S.settings = await api('/admin/v1/settings', { method: 'PUT', body: st });
    renderSettings();
    $('saveMsg').textContent = t('已保存，立即生效');
  } catch (e) { $('saveMsg').innerHTML = `<span class="err">${esc(e.message)}</span>`; }
}

// ---------------- AI 识图 ----------------
const INPUTS = L({ sketch: '手绘草图', hypsometric: '分层设色地形图', grayscale: '灰度高程图', unknown: '无法判断' });

async function renderAI() {
  const { config: c, catalog } = await api('/admin/v1/ai');
  S.ai = { c, catalog };
  const opt = (p) => `<option value="${p.id}" ${p.id === c.active ? 'selected' : ''}>${esc(tr(p.label))}</option>`;
  const card = (p) => {
    const v = c.providers[p.id] || {};
    return `<div class="panel aiprov" data-prov="${p.id}" style="margin-top:10px">
      <h2 style="margin-top:0">${esc(tr(p.label))} ${v.hasKey ? `<span class="pill">${t('已填密钥')}</span>` : ''} ${p.id === c.active ? `<span class="pill paid">${t('当前使用')}</span>` : ''}
        ${p.keyURL ? `<a class="muted" href="${esc(p.keyURL)}" target="_blank" rel="noopener" style="font-size:12px;font-weight:normal">${t('申请密钥 ↗')}</a>` : ''}</h2>
      <div class="form">
        <label>${t('接口地址')}</label><input data-k="baseURL" value="${esc(v.baseURL)}" placeholder="${esc(p.baseURL || 'https://…/v1')}">
        <label>${t('模型')}</label><div><input data-k="model" value="${esc(v.model)}" placeholder="${esc(p.models[0] || t('模型名'))}" list="models-${p.id}">
          <datalist id="models-${p.id}">${p.models.map((m) => `<option value="${esc(m)}">`).join('')}</datalist></div>
        <label>API Key</label><div style="display:flex;gap:8px;align-items:center"><input data-k="apiKey" type="password" autocomplete="off" placeholder="${v.hasKey ? t('已保存 {0}，留空则不修改', esc(v.keyMasked)) : t('未设置')}">
          ${v.hasKey ? `<label style="white-space:nowrap;color:var(--text)"><input type="checkbox" data-k="clear" style="width:auto"> ${t('清除')}</label>` : ''}</div>
        ${p.note ? `<p class="hint">${esc(tr(p.note))}</p>` : ''}
        <label></label><div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
          <button class="ghost" data-test="${p.id}">${t('测试(示例草图)')}</button>
          <label class="btnlink ghost" style="cursor:pointer">${t('用自己的图测试')}<input type="file" accept=".png,.jpg,.jpeg" data-testfile="${p.id}" hidden></label>
          <span class="muted" data-testmsg="${p.id}"></span></div>
      </div>
      <div data-testout="${p.id}"></div>
    </div>`;
  };
  $('tab-ai').innerHTML = `
    <div class="panel"><h2 style="margin-top:0">${t('AI 识图')} <span class="muted">${t('用户上传地图后，由多模态大模型判断地图类型、读出图例色带与经纬度范围，作为建议参数供用户确认')}</span></h2>
      <div class="form">
        <label>${t('启用')}</label><label style="color:var(--text)"><input type="checkbox" id="aiEnabled" ${c.enabled ? 'checked' : ''} style="width:auto"> ${t('开放 POST /v1/analyze')}</label>
        <label>${t('当前服务商')}</label><select id="aiActive"><option value="">${t('(未选择)')}</option>${catalog.map(opt).join('')}</select>
        <label>${t('请求超时')}</label><div><input id="aiTimeout" type="number" value="${c.timeoutSec}" style="width:100px"> ${t('秒')}</div>
        <label>${t('图片长边上限')}</label><div><input id="aiMaxPx" type="number" value="${c.maxImagePx}" style="width:100px"> ${t('像素')} <span class="muted">${t('发送前缩小以控制费用；取色仍按原图')}</span></div>
        <label>${t('每次扣点')}</label><div><input id="aiUnits" type="number" value="${c.unitsPerCall}" style="width:100px"> ${t('算力点/次')}</div>
      </div>
    </div>
    ${catalog.map(card).join('')}
    <div style="margin-top:14px;display:flex;gap:10px;align-items:center;position:sticky;bottom:0;background:var(--bg);padding:10px 0"><button id="saveAI">${t('保存 AI 配置')}</button><span id="aiMsg" class="muted"></span></div>`;
  $('saveAI').onclick = saveAI;
  $('tab-ai').querySelectorAll('[data-test]').forEach((b) => b.onclick = () => testAI(b.dataset.test, null));
  $('tab-ai').querySelectorAll('[data-testfile]').forEach((f) => f.onchange = () => { if (f.files[0]) testAI(f.dataset.testfile, f.files[0]); f.value = ''; });
}

function provForm(id) {
  const el = document.querySelector(`#tab-ai [data-prov="${id}"]`);
  const get = (k) => el.querySelector(`[data-k="${k}"]`);
  return { baseURL: get('baseURL').value.trim(), model: get('model').value.trim(), apiKey: get('apiKey').value.trim(), clear: !!get('clear')?.checked };
}

async function saveAI() {
  const providers = {};
  const clearKeys = [];
  for (const p of S.ai.catalog) {
    const f = provForm(p.id);
    const had = S.ai.c.providers[p.id];
    if (f.baseURL || f.model || f.apiKey || had) providers[p.id] = { baseURL: f.baseURL, model: f.model, apiKey: f.apiKey };
    if (f.clear) clearKeys.push(p.id);
  }
  const body = { enabled: $('aiEnabled').checked, active: $('aiActive').value, providers, clearKeys,
    timeoutSec: Number($('aiTimeout').value), maxImagePx: Number($('aiMaxPx').value), unitsPerCall: Number($('aiUnits').value) };
  try {
    const r = await api('/admin/v1/ai', { method: 'PUT', body });
    await renderAI();
    $('aiMsg').innerHTML = r.warning ? `<span class="err">${esc(tr(r.warning))}</span>` : t('已保存，立即生效');
  } catch (e) { $('aiMsg').innerHTML = `<span class="err">${esc(e.message)}</span>`; }
}

async function testAI(id, file) {
  const f = provForm(id);
  const fd = new FormData();
  fd.append('provider', id);
  fd.append('baseURL', f.baseURL);
  fd.append('model', f.model);
  fd.append('apiKey', f.apiKey);
  if (file) fd.append('file', file);
  const msg = document.querySelector(`[data-testmsg="${id}"]`);
  const out = document.querySelector(`[data-testout="${id}"]`);
  msg.textContent = t('识别中：{0}…(可能需要几十秒)', file ? file.name : t('示例草图'));
  out.innerHTML = '';
  try {
    const r = await fetch('/admin/v1/ai/test', { method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: fd });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(tr(d.error?.message) || `HTTP ${r.status}`);
    msg.innerHTML = `<span style="color:#8c8">${t('连接正常')}</span> · ${esc(d.model)} · ${t('{0} 秒 · {1} 次调用，输入 {2} / 输出 {3} tokens', d.elapsedSec, d.usage.calls, fmtN(d.usage.inputTokens), fmtN(d.usage.outputTokens))}`;
    const sw = (c) => `<span style="display:inline-block;width:12px;height:12px;border-radius:2px;vertical-align:middle;background:${esc(c)};border:1px solid var(--line)"></span>`;
    const legend = d.elevation?.legend?.map(([c, e]) => `${sw(c)} ${esc(c)} → ${fmtN(e)} m`).join('<br>');
    const palette = d.elevation?.palette?.map(([c, k]) => `${sw(c)} ${esc(c)} → ${esc(k)}`).join('<br>');
    out.innerHTML = `<div class="form" style="margin-top:10px">
      <label>${t('判断类型')}</label><div>${esc(INPUTS[d.input] || d.input)} <span class="muted">${t('把握')} ${Math.round((d.confidence || 0) * 100)}%</span></div>
      ${legend ? `<label>${t('图例色带')}</label><div>${legend}</div>` : ''}
      ${palette ? `<label>${t('草图配色')}</label><div>${palette}</div>` : ''}
      <label>${t('经纬度范围')}</label><div>${d.bounds ? t('西 {0} · 东 {1} · 南 {2} · 北 {3}', d.bounds.west, d.bounds.east, d.bounds.south, d.bounds.north) : `<span class="muted">${t('未读出')}</span>`}</div>
      ${d.mapBox ? `<label>${t('地图主体(像素)')}</label><div>${d.mapBox.join(', ')}${d.legendBox ? ` <span class="muted">${t('图例')} ${d.legendBox.join(', ')}</span>` : ''}</div>` : ''}
      ${d.notes ? `<label>${t('模型备注')}</label><div>${esc(d.notes)}</div>` : ''}
      ${d.warnings.length ? `<label>${t('提示')}</label><div class="muted">${d.warnings.map((w) => esc(tr(w))).join('<br>')}</div>` : ''}
      <label>${t('原始输出')}</label><details><summary class="muted">${t('展开')}</summary><pre style="white-space:pre-wrap;font-size:11px">${esc(JSON.stringify(d.raw, null, 1))}</pre></details>
    </div>`;
  } catch (e) { msg.innerHTML = `<span class="err">${esc(e.message)}</span>`; }
}

// ---------------- 审计 ----------------
const ACTIONS = L({ 'tenant.create': '新建用户', 'tenant.update': '修改用户', 'tenant.signup': '自助注册', 'key.add': '新增密钥', 'key.revoke': '吊销密钥', 'settings.update': '修改配置', 'job.cancel': '取消任务', 'tenant.delete': '删除用户', 'ai.update': '修改 AI 配置', 'ai.test': '测试 AI 识图' });
async function renderAudit() {
  const rows = await api('/admin/v1/audit');
  $('tab-audit').innerHTML = `<div class="panel"><table class="jobs"><thead><tr><th>${t('时间')}</th><th>${t('操作')}</th><th>${t('对象')}</th><th>${t('详情')}</th><th>IP</th></tr></thead><tbody>
    ${rows.map((a) => `<tr><td>${fmtT(a.time)}</td><td>${esc(ACTIONS[a.action] || a.action)}</td><td>${esc(a.target || '')}</td>
      <td class="muted" style="max-width:520px;word-break:break-all">${esc(a.action === 'settings.update' ? t('套餐/计价/接口配置变更') : JSON.stringify(a.detail || {}))}</td><td>${esc(a.ip || '')}</td></tr>`).join('') || `<tr><td colspan="5" class="muted">${t('暂无记录')}</td></tr>`}</tbody></table></div>`;
}

// ---------------- 框架 ----------------
async function showTab(tab) {
  S.tab = tab;
  document.querySelectorAll('#tabs button').forEach((b) => b.classList.toggle('on', b.dataset.tab === tab));
  document.querySelectorAll('main > section').forEach((s) => { s.hidden = s.id !== 'tab-' + tab; });
  await refresh();
}

async function refresh() {
  try {
    S.settings = await api('/admin/v1/settings');
    if (S.tab === 'overview') await renderOverview();
    if (S.tab === 'tenants' || S.tab === 'jobs') await renderTenants();
    if (S.tab === 'jobs') await renderJobs();
    if (S.tab === 'settings') renderSettings();
    if (S.tab === 'ai') await renderAI();
    if (S.tab === 'audit') await renderAudit();
  } catch (e) { $('gen').innerHTML = `<span class="err">${esc(e.message)}</span>`; }
}

async function main() {
  mountLang($('langSlot'));
  if (!token) await login();
  document.querySelectorAll('#tabs button').forEach((b) => b.onclick = () => showTab(b.dataset.tab));
  $('btnRefresh').onclick = refresh;
  $('btnLogout').onclick = () => { try { localStorage.removeItem(TOKEN); } catch { /* 忽略 */ } location.reload(); };
  document.body.addEventListener('click', (e) => {
    const tr = e.target.closest('[data-tenant]');
    if (tr && !e.target.closest('dialog')) openTenant(tr.dataset.tenant).catch((err) => alert(err.message));
  });
  $('newTenantDlg').addEventListener('close', () => { if ($('newTenantDlg').returnValue === 'ok') createTenant(); });
  await showTab('overview');
  setInterval(() => { if (S.tab === 'overview' || S.tab === 'jobs') refresh(); }, 15000);
}

main();
