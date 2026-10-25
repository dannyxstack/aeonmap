import { api, ensureKey, getKey, clearKey, keyed } from './api.js';
import { t, tr, EN, LOCALE, mountLang, fmtTime } from './i18n.js';

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const L = (m) => Object.fromEntries(Object.entries(m).map(([k, v]) => [k, t(v)])); // 映射表的值逐个翻译
const STATUS = L({ uploaded: '已上传', prepping: '预处理中', ready: '就绪', failed: '失败', queued: '排队', running: '运行中', succeeded: '完成', cancelled: '已取消' });
const INPUT = L({ grayscale: '高程图', sketch: '草图', hypsometric: '分层设色' });
const STAGE = L({ prep: '预处理', simulate: '演化', export: '导出', done: '' });

const S = { me: null, worlds: [], jobs: new Map(), runs: [], simWorld: null, expRun: null };

async function loadAll() {
  const [me, { worlds }, { jobs }, { runs }] = await Promise.all([api('/v1/me'), api('/v1/worlds'), api('/v1/jobs'), api('/v1/runs')]);
  S.me = me; S.worlds = worlds; S.runs = runs;
  S.jobs = new Map(jobs.map((j) => [j.id, j]));
  renderAcct(); renderWorlds(); renderJobs(); renderAccount();
}

function renderAcct() {
  const p = S.me.plan;
  const lim = p.monthlyUnits < 0 ? t('不限') : p.monthlyUnits;
  const pct = p.monthlyUnits > 0 ? Math.min(100, S.me.usedThisMonth / p.monthlyUnits * 100) : 0;
  $('acct').innerHTML = `<span><b>${esc(tr(S.me.name))}</b> · ${t('套餐')} ${esc(p.name)}${p.noSimulate ? ` <span class="muted">(${t('仅环境分析')})</span>` : ''}</span>
    <span>${t('本月 {0} / {1} 点', S.me.usedThisMonth, lim)} <span class="meter"><i style="width:${pct}%"></i></span></span>
    <span>${t('引擎')} ${esc(S.me.engineVersion)}</span>`;
}

function worldName(id) { return tr(S.worlds.find((w) => w.id === id)?.name) || id; }

function renderWorlds() {
  $('worlds').innerHTML = S.worlds.map((w) => {
    const runs = S.runs.filter((r) => r.worldId === w.id);
    const done = runs.find((r) => r.done);
    const b = w.bounds ? `${w.bounds.west}°~${w.bounds.east}°E${EN ? ', ' : '，'}${w.bounds.south}°~${w.bounds.north}°N` : '';
    const thumb = w.status === 'ready' ? `style="background-image:url('${keyed(`/v1/worlds/${w.id}/preview/koppen.png`)}')"` : '';
    return `<div class="wcard"><div class="thumb" ${thumb}>${w.status === 'ready' ? '' : esc(STATUS[w.status] || w.status)}</div>
      <div class="body"><div class="title"><span>${esc(tr(w.name))}</span><span class="tag ${w.status}">${STATUS[w.status] || w.status}</span></div>
      <div class="meta">${INPUT[w.input] || w.input}${w.kind === 'builtin' ? ' · ' + t('内置') : ''} · ${esc(b)}${w.landCells ? ` · ${t('{0} 个陆地格', w.landCells)}` : ''}</div>
      <div class="meta">${t('{0} 次演化', runs.length)}${w.error ? ` · <span class="err">${esc(tr(w.error).slice(0, 160))}</span>` : ''}</div>
      <div class="acts">
        ${w.status === 'ready' ? `<a class="btnlink primary" href="/index.html?world=${w.id}">${t('查看气候与资源')}</a>` : ''}
        ${w.status === 'ready' && !S.me.plan.noSimulate ? `<button class="ghost" data-act="sim" data-id="${w.id}">${t('新建演化')}</button>` : ''}
        ${done ? `<a class="btnlink" href="/index.html?run=${done.id}">${t('回放最近演化')}</a>` : ''}
        ${w.status === 'ready' ? `<button class="ghost" data-act="expworld" data-id="${w.id}">${t('导出图层')}</button>` : ''}
        ${w.kind !== 'builtin' && w.tenant === S.me.id ? `<button class="ghost" data-act="del" data-id="${w.id}">${t('删除')}</button>` : ''}
      </div></div></div>`;
  }).join('') || `<p class="muted">${t('还没有世界')}</p>`;
}

function renderJobs() {
  const jobs = [...S.jobs.values()].sort((a, b) => (b.created > a.created ? 1 : -1)).slice(0, 50);
  $('jobs').querySelector('tbody').innerHTML = jobs.map((j) => {
    const req = typeof j.request === 'object' ? j.request : {};
    const what = [req.simulate ? t('演化 {0}年', req.simulate.years || '') : '', req.export ? t('导出') : '', !req.simulate && !req.export ? t('预处理') : ''].filter(Boolean).join(' + ');
    const r = j.result || {};
    const links = [
      r.runId ? `<a href="/index.html?run=${r.runId}">${t('回放')}</a>` : '',
      r.runId && j.status === 'succeeded' ? `<a href="#" data-act="exprun" data-run="${r.runId}" data-world="${j.world}">${t('导出…')}</a>` : '',
      r.exportUrl ? `<a href="${keyed(r.exportUrl)}">${t('下载导出包')}</a>` : '',
      r.runReused ? `<span class="muted">${t('复用已有结果')}</span>` : '',
    ].filter(Boolean).join(' · ');
    const prog = j.status === 'running' ? `<div class="bar"><i style="width:${Math.round((j.progress || 0) * 100)}%"></i></div>` : '';
    return `<tr><td>${esc(what)}<div class="muted">${j.id} · ${fmtTime(j.created)}</div></td><td>${esc(worldName(j.world))}</td>
      <td><span class="tag ${j.status}">${STATUS[j.status] || j.status}</span> ${esc(STAGE[j.stage] || '')} ${j.status === 'running' ? Math.round((j.progress || 0) * 100) + '%' : ''}
        <div class="muted">${esc(tr(j.message || ''))}</div>${prog}${j.error ? `<div class="err">${esc(tr(j.error))}</div>` : ''}</td>
      <td>${links}</td><td>${j.units || 0}${j.status !== 'succeeded' && j.estimate ? `<span class="muted"> / ${t('约 {0}', j.estimate)}</span>` : ''}</td>
      <td>${j.status === 'running' || j.status === 'queued' ? `<button class="ghost" data-act="cancel" data-id="${j.id}">${t('取消')}</button>` : ''}</td></tr>`;
  }).join('') || `<tr><td colspan="6" class="muted">${t('暂无任务')}</td></tr>`;
}

function renderAccount() {
  const k = getKey();
  $('account').innerHTML = `<div class="form">
    <label>${t('接口密钥')}</label><div><code class="key">${esc(k.slice(0, 10))}…${esc(k.slice(-4))}</code> <button class="ghost" id="copyKey">${t('复制')}</button></div>
    <label>${t('回调签名密钥')}</label><div><code class="key">${esc(S.me.webhookSecret.slice(0, 12))}…</code> <button class="ghost" id="copySec">${t('复制')}</button>
      <span class="muted">${t('回调头 X-Mapsim-Signature = sha256=HMAC(密钥, "时间戳." + 请求体)')}</span></div>
    <label>${t('快速开始')}</label><pre class="full" style="white-space:pre-wrap;background:var(--panel2);padding:10px;border-radius:6px;margin:0">curl -H "Authorization: Bearer $KEY" ${location.origin}/v1/worlds

curl -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \\
  -d '{"simulate":{"years":1000,"seed":1},"export":{"formats":["geojson","geotiff"]}}' \\
  ${location.origin}/v1/worlds/eurasia/jobs

# Python: sdk/python/mapsim_client.py</pre></div>`;
  $('copyKey').onclick = () => navigator.clipboard.writeText(k);
  $('copySec').onclick = () => navigator.clipboard.writeText(S.me.webhookSecret);
}

// ---- 上传 ----
const HINTS = L({
  grayscale: '灰度越亮海拔越高。8/16 位 PNG 或 TIFF；浮点 GeoTIFF 选"像素值即米"。海平面以下识别为海洋。',
  sketch: '颜色约定：蓝=海或湖，青=河流，棕/红/黑色笔画=山脉，黄/橙=高地丘陵，其余(白纸、绿色)=平原。',
  hypsometric: '按色带把颜色换算成海拔，蓝色为水。可点"自动识别色带"生成色带(颜色→海拔)，核对后再上传。',
});
function updateUploadForm() {
  const typ = $('upInput').value;
  $('upHint').textContent = HINTS[typ];
  $('elevRow').hidden = typ !== 'grayscale';
  $('elevLabel').hidden = typ !== 'grayscale';
  document.querySelectorAll('.legendRow').forEach((e) => { e.hidden = typ !== 'hypsometric'; });
  document.querySelectorAll('.paletteRow').forEach((e) => { e.hidden = typ !== 'sketch'; });
  const bandOk = typ === 'hypsometric' && isRaster($('upFile').files[0]);
  document.querySelectorAll('.bandRow').forEach((e) => { e.hidden = !bandOk; });
  $('bandResult').hidden = !bandOk || !S.bands;
}

// ---- AI 识图预填 ----
const AI_INPUT = L({ sketch: '手绘草图', hypsometric: '分层设色地形图', grayscale: '灰度高程图', unknown: '无法判断' });
const AI_CLASS = L({ sea: '海/湖', river: '河流', mountain: '山脉', highland: '高地', land: '平原' });
const isRaster = (file) => file && /\.(png|jpe?g)$/i.test(file.name);

function onFileChange() {
  const file = $('upFile').files[0];
  S.ai = null;
  S.bands = null;
  $('bandResult').hidden = true;
  $('bandResult').innerHTML = '';
  $('bandMsg').textContent = S.me?.colorbands?.refdem
    ? t('按颜色邻接关系排序，用参考高程数据校准(需填写经纬度范围)，读取图例色块；不扣点')
    : t('按颜色邻接关系排序并读取图例色块；填写经纬度范围且服务端装有参考高程数据时会自动校准；不扣点');
  $('aiResult').hidden = true;
  $('aiResult').innerHTML = '';
  $('aiMsg').textContent = '';
  document.querySelectorAll('#upForm .aiFilled').forEach((e) => e.classList.remove('aiFilled'));
  const f = $('upForm');
  if (file && (!f.name.value || f.name.dataset.auto)) { f.name.value = file.name.replace(/\.[^.]+$/, ''); f.name.dataset.auto = '1'; }
  updateUploadForm();
  const ok = S.me?.ai?.enabled && isRaster(file);
  document.querySelectorAll('.aiRow').forEach((e) => { e.hidden = !ok; });
  if (ok) {
    const u = S.me.ai.unitsPerCall;
    $('aiMsg').textContent = t('判断地图类型、读出图例色带与经纬度范围，预填到下方') + (u > 0 ? t('(每次 {0} 点)', u) : '');
  }
}

async function analyze() {
  const file = $('upFile').files[0];
  if (!file) return;
  const btn = $('btnAnalyze');
  btn.disabled = true;
  $('aiMsg').textContent = t('识别中，通常需要十几秒到一分钟…');
  try {
    const fd = new FormData();
    fd.append('file', file);
    const r = await api('/v1/analyze', { method: 'POST', body: fd });
    if ($('upFile').files[0] !== file) return; // 等待期间换了文件
    S.ai = { file, result: r };
    prefill(r);
    await showAIResult(file, r);
    if (r.bands) { S.bands = { file, result: r.bands }; showBands(r.bands); updateUploadForm(); }
    $('aiMsg').textContent = t('已预填，蓝框标出的是 AI 填写的内容，请核对后再上传');
    api('/v1/me').then((me) => { S.me = me; renderAcct(); }).catch(() => {});
  } catch (e) {
    $('aiMsg').innerHTML = `<span class="err">${esc(e.message)}</span>`;
  } finally { btn.disabled = false; }
}

function mark(el, v) {
  el.value = v;
  el.classList.add('aiFilled');
}

function prefill(r) {
  const f = $('upForm');
  if (r.input && r.input !== 'unknown') { mark(f.input, r.input); updateUploadForm(); }
  if (r.bounds) for (const k of ['west', 'east', 'south', 'north']) mark(f[k], r.bounds[k]);
  const lines = (arr) => '[\n' + arr.map((x) => '  ' + JSON.stringify(x)).join(',\n') + '\n]';
  if (r.elevation?.legend) mark(f.legend, lines(r.elevation.legend));
  if (r.elevation?.water?.length) mark(f.water, JSON.stringify(r.elevation.water));
  if (r.input === 'sketch') {
    if (r.elevation?.palette) mark(f.palette, lines(r.elevation.palette));
    else f.palette.value = '';
  }
}

// 预览：缩略图上标出地图主体(绿)与图例(橙)，并给出是否裁剪的选项。
async function showAIResult(file, r) {
  const box = $('aiResult');
  const bmp = await createImageBitmap(file);
  const W = bmp.width, H = bmp.height;
  const sc = Math.min(1, 360 / W, 260 / H);
  const cv = document.createElement('canvas');
  cv.width = Math.round(W * sc); cv.height = Math.round(H * sc);
  const g = cv.getContext('2d');
  g.drawImage(bmp, 0, 0, cv.width, cv.height);
  const rect = (b, color, label) => {
    if (!b) return;
    g.strokeStyle = color; g.lineWidth = 2; g.setLineDash([6, 3]);
    g.strokeRect(b[0] * sc + 1, b[1] * sc + 1, (b[2] - b[0]) * sc - 2, (b[3] - b[1]) * sc - 2);
    g.setLineDash([]); g.fillStyle = color; g.font = '12px sans-serif';
    g.fillText(label, b[0] * sc + 4, Math.max(b[1] * sc + 14, 14));
  };
  rect(r.mapBox, '#4fd18b', t('地图主体'));
  rect(r.legendBox, '#f0a040', t('图例'));
  bmp.close?.();

  const mb = r.mapBox;
  const canCrop = mb && r.input !== 'grayscale' && ((mb[2] - mb[0]) < W * 0.97 || (mb[3] - mb[1]) < H * 0.97);
  S.ai.size = [W, H];
  const sw = (c) => `<i class="sw" style="background:${esc(c)}"></i>`;
  const legend = r.elevation?.legend?.map(([c, e]) => `${sw(c)}${Number(e).toLocaleString(LOCALE)} m`).join('　');
  const palette = r.elevation?.palette?.map(([c, k]) => `${sw(c)}${esc(AI_CLASS[k] || k)}`).join('　');
  box.innerHTML = '';
  box.appendChild(cv);
  const sum = document.createElement('div');
  sum.className = 'sum';
  sum.innerHTML = `
    <div><b>${esc(AI_INPUT[r.input] || r.input)}</b> <span class="muted">${t('把握')} ${Math.round((r.confidence || 0) * 100)}%</span></div>
    ${legend ? `<div>${t('图例色带：')}${legend}</div>` : ''}
    ${palette ? `<div>${t('草图配色：')}${palette}</div>` : ''}
    <div>${t('经纬度范围：')}${r.bounds ? t('西 {0}°，东 {1}°，南 {2}°，北 {3}°', r.bounds.west, r.bounds.east, r.bounds.south, r.bounds.north) : `<span class="muted">${t('未读出，请手动填写')}</span>`}</div>
    ${r.notes ? `<div class="muted">${esc(r.notes)}</div>` : ''}
    ${(r.warnings || []).filter((w) => !(canCrop && w.includes('mapBox'))).map((w) => `<div class="warn">· ${esc(tr(w))}</div>`).join('')}
    ${canCrop ? `<label style="display:block;margin-top:6px;color:var(--text)"><input type="checkbox" id="aiCrop" checked style="width:auto">
      ${t('上传前裁剪到地图主体({0}×{1} 像素，去掉边框与图例；经纬度范围对应的就是这块区域)', mb[2] - mb[0], mb[3] - mb[1])}</label>` : ''}`;
  box.appendChild(sum);
  box.hidden = false;
}

// ---- 色带自动识别 ----
const BAND_MODE = L({ legend: '图例读数', dem: '参考高程校准', topology: '颜色邻接排序 + 分级模板' });
const BAND_SRC = L({ legend: '图例', dem: '参考高程', interp: '插值', template: '模板' });
const BAND_DIR = L({ coast: '贴海一端为低', legend: '按图例排列', dem: '按参考高程', color: '按配色习惯' });

function formBounds() {
  const f = $('upForm');
  const v = ['west', 'east', 'south', 'north'].map((k) => f[k].value);
  return v.every((x) => x !== '') ? { west: +v[0], east: +v[1], south: +v[2], north: +v[3] } : null;
}

async function detectBands() {
  const file = $('upFile').files[0];
  if (!isRaster(file)) return;
  const btn = $('btnBands');
  btn.disabled = true;
  $('bandMsg').textContent = t('识别中，通常需要几秒到半分钟…');
  try {
    const fd = new FormData();
    fd.append('file', file);
    const b = formBounds();
    if (b) fd.append('bounds', JSON.stringify(b));
    const ai = S.ai?.file === file ? S.ai.result : null; // 有 AI 识图结果时带上地图主体框、图例框与刻度
    if (ai?.mapBox) fd.append('mapBox', JSON.stringify(ai.mapBox));
    if (ai?.legendBox) fd.append('legendBox', JSON.stringify(ai.legendBox));
    if (ai?.legendStops?.length) fd.append('stops', JSON.stringify(ai.legendStops));
    const r = await api('/v1/colorbands', { method: 'POST', body: fd });
    if ($('upFile').files[0] !== file) return;
    S.bands = { file, result: r };
    showBands(r);
    fillLegend();
    $('bandMsg').textContent = t('已填入色带({0}s)，可在表格里改数值', r.elapsedSec);
    updateUploadForm();
  } catch (e) {
    $('bandMsg').innerHTML = `<span class="err">${esc(e.message)}</span>`;
  } finally { btn.disabled = false; }
}

function showBands(r) {
  const box = $('bandResult');
  const sw = (c) => `<i class="sw" style="background:${esc(c)}"></i>`;
  const land = r.layers.filter((l) => !l.water);
  const water = r.layers.filter((l) => l.water);
  const reg = r.registration;
  const nb = r.bounds;
  const rows = land.map((l, i) => `<tr><td>${sw(l.color)}<code>${esc(l.color)}</code></td>
    <td><input type="number" step="any" data-band="${i}" value="${l.elevation}"> m</td>
    <td class="muted">${l.range ? `${l.range[0]}~${l.range[1]}` : ''}</td>
    <td>${esc(BAND_SRC[l.source] || l.source)}${l.swatch ? ' · ' + t('图例色') : ''}</td>
    <td><span class="conf" title="${Math.round(l.confidence * 100)}%"><i style="width:${Math.round(l.confidence * 100)}%"></i></span></td>
    <td class="muted">${l.dem ? `${t('参考')} ${l.dem[0]}/${l.dem[1]}/${l.dem[2]}` : ''}</td></tr>`).reverse().join('');
  const pct = (v) => Math.round(v * 100) + '%';
  box.innerHTML = `<img src="${r.preview || ''}" alt="${t('分类预览')}" title="${t('每类涂成代表色，深色细线为层与层的交界，灰色为排除区域(边框、图例)')}">
    <div class="sum">
      <div><b>${esc(BAND_MODE[r.mode] || r.mode)}</b> <span class="muted">${t('整体把握 {0} · 链一致性 {1} · 方向：{2}', pct(r.confidence), pct(r.chainConsistency), esc(BAND_DIR[r.direction] || r.direction))}${r.exact ? ' · ' + t('精确色') : ''}</span></div>
      ${r.legendDetected ? `<div>${t('图例：找到 {0} 个色块({1})', r.legendDetected.swatches.length, r.legendDetected.axis === 'v' ? t('竖排') : t('横排'))}</div>` : ''}
      ${reg ? `<div>${t('参考高程：海陆吻合度 {0}', pct(reg.iou))}${reg.iouBefore != null && reg.refined ? t('(微调前 {0})', pct(reg.iouBefore)) : ''}</div>` : ''}
      ${nb ? `<div>${t('微调后的范围：')}${t('西 {0}°，东 {1}°，南 {2}°，北 {3}°', nb.west, nb.east, nb.south, nb.north)} <button type="button" class="ghost" id="bandUseBounds">${t('采用')}</button></div>` : ''}
      ${water.length ? `<div>${t('水体：')}${water.map((l) => sw(l.color) + `<code>${esc(l.color)}</code>`).join(' ')} <span class="muted">${r.water?.length ? t('非蓝色的水体已填入"水体颜色"') : t('按蓝色识别')}</span></div>` : ''}
      ${r.droppedColors?.length ? `<div class="muted">${t('已忽略的线条/文字色：')}${r.droppedColors.map(sw).join('')}</div>` : ''}
      ${(r.warnings || []).map((w) => `<div class="warn">· ${esc(tr(w))}</div>`).join('')}
      ${r.legendDetected && r.waterColors?.length ? `<label style="display:block;margin-top:4px;color:var(--text)"><input type="checkbox" id="bandMaskLegend" checked style="width:auto">
        ${t('上传前把图例区域涂成水体颜色(否则图例色块会被当成陆地)')}</label>` : ''}
      <table><thead><tr><th>${t('颜色(高→低)')}</th><th>${t('海拔')}</th><th>${t('区间')}</th><th>${t('来源')}</th><th>${t('把握')}</th><th></th></tr></thead><tbody>${rows}</tbody></table>
    </div>`;
  box.hidden = false;
  box.querySelectorAll('input[data-band]').forEach((el) => {
    el.oninput = () => { land[+el.dataset.band].elevation = +el.value; fillLegend(); };
  });
  const ub = $('bandUseBounds');
  if (ub) ub.onclick = () => { for (const k of ['west', 'east', 'south', 'north']) mark($('upForm')[k], nb[k]); };
}

function fillLegend() {
  const r = S.bands?.result;
  if (!r) return;
  const legend = r.layers.filter((l) => !l.water).map((l) => [l.color, l.elevation]).sort((a, b) => a[1] - b[1]);
  mark($('upForm').legend, '[\n' + legend.map((x) => '  ' + JSON.stringify(x)).join(',\n') + '\n]');
  const f = $('upForm');
  if (r.water?.length) mark(f.water, JSON.stringify(r.water));
  else if (f.water.classList.contains('aiFilled')) f.water.value = ''; // 上一次识别填的，清掉
}

// 把图例框涂成水体颜色(保持原尺寸，输出 PNG)
async function maskBox(file, b, color) {
  const bmp = await createImageBitmap(file);
  const cv = document.createElement('canvas');
  cv.width = bmp.width; cv.height = bmp.height;
  const g = cv.getContext('2d');
  g.drawImage(bmp, 0, 0);
  bmp.close?.();
  const pad = Math.round(Math.max(cv.width, cv.height) * 0.01);
  g.fillStyle = color;
  g.fillRect(b[0] - pad, b[1] - pad, b[2] - b[0] + 2 * pad, b[3] - b[1] + 2 * pad);
  const blob = await new Promise((res) => cv.toBlob(res, 'image/png'));
  return new File([blob], file.name.replace(/\.[^.]+$/, '') + '.png', { type: 'image/png' });
}

async function cropToMapBox(file, b) {
  const bmp = await createImageBitmap(file, b[0], b[1], b[2] - b[0], b[3] - b[1]);
  const cv = document.createElement('canvas');
  cv.width = bmp.width; cv.height = bmp.height;
  cv.getContext('2d').drawImage(bmp, 0, 0);
  bmp.close?.();
  const blob = await new Promise((res) => cv.toBlob(res, 'image/png'));
  return new File([blob], file.name.replace(/\.[^.]+$/, '') + '.png', { type: 'image/png' });
}

async function upload(ev) {
  ev.preventDefault();
  const f = ev.target;
  const fd = new FormData();
  let file = f.file.files[0];
  const bands = S.bands?.file === file ? S.bands.result : null;
  if (f.input.value === 'hypsometric' && bands?.legendDetected && bands.waterColors?.length && $('bandMaskLegend')?.checked) {
    $('upMsg').textContent = t('涂掉图例…');
    file = await maskBox(file, bands.legendDetected.box, bands.waterColors[0]);
  }
  if (S.ai?.file === f.file.files[0] && $('aiCrop')?.checked) {
    $('upMsg').textContent = t('裁剪中…');
    file = await cropToMapBox(file, S.ai.result.mapBox);
  }
  fd.append('file', file);
  fd.append('input', f.input.value);
  if (f.name.value) fd.append('name', f.name.value);
  const vals = ['west', 'east', 'south', 'north'].map((k) => f[k].value);
  if (vals.every((v) => v !== '')) {
    fd.append('bounds', JSON.stringify({ west: +vals[0], east: +vals[1], south: +vals[2], north: +vals[3] }));
  }
  const elev = {};
  if (f.input.value === 'grayscale') {
    if (f.enc.value === 'meters') elev.encoding = 'meters';
    else { elev.min = +f.emin.value; elev.max = +f.emax.value; }
    elev.sea_level = +f.sea.value || 0;
  }
  if (f.input.value === 'hypsometric') {
    try { elev.legend = JSON.parse(f.legend.value); } catch { $('upMsg').textContent = t('色带不是合法 JSON'); return; }
    if (f.water.value.trim()) {
      try { elev.water = JSON.parse(f.water.value); } catch { $('upMsg').textContent = t('水体颜色不是合法 JSON'); return; }
    }
  }
  if (f.input.value === 'sketch' && f.palette.value.trim()) {
    try { elev.palette = JSON.parse(f.palette.value); } catch { $('upMsg').textContent = t('草图配色不是合法 JSON'); return; }
  }
  fd.append('elevation', JSON.stringify(elev));
  $('upMsg').textContent = t('上传中…');
  try {
    const r = await api('/v1/worlds', { method: 'POST', body: fd });
    $('upMsg').textContent = t('已创建「{0}」，正在预处理；完成后点"查看气候与资源"', r.world.name);
    S.worlds.unshift(r.world); S.jobs.set(r.job.id, r.job);
    renderWorlds(); renderJobs();
    f.reset(); onFileChange(); updateUploadForm();
  } catch (e) { $('upMsg').innerHTML = `<span class="err">${esc(e.message)}</span>`; }
}

// ---- 新建演化 / 导出 ----
function openSim(worldId) {
  S.simWorld = worldId;
  $('simWorld').textContent = worldName(worldId);
  $('simErr').textContent = '';
  $('simDlg').showModal();
}

async function submitSim() {
  const f = $('simForm');
  const sim = { years: +f.years.value, seed: +f.seed.value, params: { superRate: +f.superRate.value } };
  if (f.constraints.value.trim()) {
    try { sim.constraints = JSON.parse(f.constraints.value); } catch { $('simErr').textContent = t('约束不是合法 JSON'); return false; }
  }
  const body = { simulate: sim };
  if (f.exp.checked) body.export = { formats: ['geotiff', 'geojson', 'csv', 'engine'] };
  if (f.webhook.value.trim()) body.webhook = f.webhook.value.trim();
  try {
    const j = await api(`/v1/worlds/${S.simWorld}/jobs`, { method: 'POST', body });
    S.jobs.set(j.id, j); renderJobs();
    return true;
  } catch (e) { $('simErr').textContent = e.message; return false; }
}

function openExport(runId, worldId) {
  S.expRun = { runId, worldId };
  $('expRun').textContent = runId || worldName(worldId) + t('(仅世界图层)');
  $('expDlg').showModal();
}

async function submitExport() {
  const f = $('expDlg').querySelector('form');
  const formats = [...$('expFmts').querySelectorAll('input:checked')].map((i) => i.value);
  const exp = { formats, engineSize: +f.size.value };
  if (S.expRun.runId) exp.run = S.expRun.runId;
  if (f.year.value !== '') exp.year = +f.year.value;
  try {
    const j = await api(`/v1/worlds/${S.expRun.worldId}/jobs`, { method: 'POST', body: { export: exp } });
    S.jobs.set(j.id, j); renderJobs();
  } catch (e) { alert(e.message); }
}

// ---- 实时推送 ----
function connectWS() {
  const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws?key=${encodeURIComponent(getKey())}`);
  ws.onmessage = async (ev) => {
    if (typeof ev.data !== 'string') return;
    const m = JSON.parse(ev.data);
    if (m.type !== 'job') return;
    const prev = S.jobs.get(m.job.id);
    S.jobs.set(m.job.id, m.job);
    renderJobs();
    if (prev?.status !== m.job.status && (m.job.status === 'succeeded' || m.job.status === 'failed')) {
      await loadAll(); // 世界状态、用量、演化列表随之更新
    }
  };
  ws.onclose = () => setTimeout(connectWS, 3000);
}

function bind() {
  $('upInput').onchange = updateUploadForm;
  $('upForm').onsubmit = upload;
  $('upFile').onchange = onFileChange;
  $('btnAnalyze').onclick = analyze;
  $('btnBands').onclick = detectBands;
  $('upForm').addEventListener('input', (e) => {
    e.target.classList.remove('aiFilled');
    if (e.target.name === 'name') delete e.target.dataset.auto;
  });
  $('btnLogout').onclick = () => { clearKey(); location.reload(); };
  document.body.addEventListener('click', async (e) => {
    const el = e.target.closest('[data-act]');
    if (!el) return;
    e.preventDefault();
    const act = el.dataset.act;
    if (act === 'sim') openSim(el.dataset.id);
    if (act === 'expworld') openExport(null, el.dataset.id);
    if (act === 'exprun') openExport(el.dataset.run, el.dataset.world);
    if (act === 'cancel') { try { await api(`/v1/jobs/${el.dataset.id}`, { method: 'DELETE' }); } catch (err) { alert(err.message); } }
    if (act === 'del' && confirm(t('删除这个世界及其所有演化？此操作不可恢复。'))) {
      try { await api(`/v1/worlds/${el.dataset.id}`, { method: 'DELETE' }); await loadAll(); } catch (err) { alert(err.message); }
    }
  });
  $('simDlg').addEventListener('close', async () => {
    if ($('simDlg').returnValue === 'ok' && !(await submitSim())) $('simDlg').showModal();
  });
  $('expDlg').addEventListener('close', () => { if ($('expDlg').returnValue === 'ok') submitExport(); });
}

async function main() {
  mountLang($('langSlot'));
  await ensureKey();
  bind();
  updateUploadForm();
  await loadAll();
  connectWS();
}

main().catch((e) => { document.querySelector('main').innerHTML = `<p class="err">${t('加载失败：')}${esc(e.message)}</p>`; });
