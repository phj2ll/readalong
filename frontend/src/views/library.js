import { api, esc, fmtTime, toast } from '../api.js';

export function renderLibrary(root) {
  let disposers = [];
  let modelTimer = null;
  let jobTimer = null;
  let busy = false;

  root.innerHTML = `
    <div class="topbar">
      <div class="brand"><span class="dot"></span>ReadAlong</div>
      <span class="chip" id="ver"></span>
      <div class="spacer"></div>
      <span class="chip" id="modelChip">语音模型检查中…</span>
      <button class="btn small" id="openData">数据目录</button>
    </div>
    <div class="wrap">
      <div class="card create">
        <h1>粘一个视频链接，生成跟读课程</h1>
        <p class="hint">支持 B 站等 yt-dlp 能解析的站点。生成后每一句都能单独循环、录音对比。</p>
        <div class="form-row">
          <input type="url" id="url" placeholder="https://www.bilibili.com/video/BVxxxxxxx/?p=1" spellcheck="false" autocomplete="off" />
          <select id="lang">
            <option value="en">英语</option>
            <option value="zh">中文</option>
            <option value="auto">自动识别</option>
          </select>
          <button class="btn primary" id="go">生成课程</button>
        </div>
        <div id="progress"></div>
      </div>

      <div class="section-title">我的课程</div>
      <div id="courses" class="courses"></div>
    </div>
  `;

  const $ = (s) => root.querySelector(s);
  const progressEl = $('#progress');
  const coursesEl = $('#courses');

  // -------- 顶栏状态 --------
  api
    .version()
    .then((v) => {
      $('#ver').textContent = v === 'dev' ? '开发版' : `v${v}`;
    })
    .catch(() => {});

  function renderModel(m) {
    const chip = $('#modelChip');
    if (!m) return;
    if (m.ready) {
      chip.className = 'chip ok';
      chip.textContent = `模型就绪 · ${m.name}`;
      progressEl.querySelector('#modelBox')?.remove();
      return;
    }
    chip.className = 'chip warn';
    chip.textContent = m.error ? '模型下载失败' : `模型 ${m.pct || 0}%`;
    let box = progressEl.querySelector('#modelBox');
    if (!box) {
      box = document.createElement('div');
      box.id = 'modelBox';
      box.className = 'progress-box';
      box.innerHTML = `
        <div class="progress-head">
          <span class="step">下载语音模型</span>
          <span class="pct" id="mPct"></span>
          <span class="spacer"></span>
          <button class="btn small" id="mRetry">重试</button>
        </div>
        <div class="bar" id="mBar"><i></i></div>
        <div class="log" id="mMsg"></div>`;
      progressEl.appendChild(box);
      box.querySelector('#mRetry').onclick = () => api.downloadModel().then(renderModel);
    }
    box.querySelector('#mPct').textContent = `${m.pct || 0}%`;
    box.querySelector('#mBar').firstElementChild.style.width = `${m.pct || 0}%`;
    box.querySelector('#mMsg').textContent = m.error || m.msg || '';
  }

  function pollModel() {
    api
      .model()
      .then(renderModel)
      .catch(() => {});
  }
  pollModel();
  modelTimer = setInterval(pollModel, 1500);
  disposers.push(() => clearInterval(modelTimer));

  $('#openData').onclick = () => api.openDataDir().catch((e) => toast(String(e), true));

  // -------- 课程列表 --------
  function renderCourses(list) {
    if (!list || !list.length) {
      coursesEl.innerHTML = `<div class="empty">还没有课程。上面粘一个链接试试，第一次会先下载语音模型。</div>`;
      return;
    }
    coursesEl.innerHTML = list
      .map(
        (c) => `
      <div class="card course">
        <div class="t">${esc(c.title || c.id)}</div>
        <div class="m">
          <span>${c.count} 句</span>
          <span>· ${fmtTime(c.duration)}</span>
          <span>· ${esc(c.created || '')}</span>
        </div>
        <div class="acts">
          <button class="btn small primary" data-open="${esc(c.id)}">打开</button>
          <button class="btn small danger" data-del="${esc(c.id)}">删除</button>
        </div>
      </div>`
      )
      .join('');

    coursesEl.querySelectorAll('[data-open]').forEach((b) => {
      b.onclick = () => {
        location.hash = `/play/${encodeURIComponent(b.dataset.open)}`;
      };
    });
    coursesEl.querySelectorAll('[data-del]').forEach((b) => {
      b.onclick = async () => {
        if (!confirm('删除这门课程？音频和数据都会被删掉。')) return;
        try {
          await api.del(b.dataset.del);
          refresh();
          toast('已删除');
        } catch (e) {
          toast(String(e), true);
        }
      };
    });
  }

  function refresh() {
    api
      .list()
      .then(renderCourses)
      .catch(() => renderCourses([]));
  }
  refresh();

  // -------- 生成任务 --------
  function renderJob(j) {
    let box = progressEl.querySelector('#jobBox');
    if (!box) {
      box = document.createElement('div');
      box.id = 'jobBox';
      box.className = 'progress-box';
      box.innerHTML = `
        <div class="progress-head">
          <span class="step" id="jStep"></span>
          <span class="pct" id="jPct"></span>
        </div>
        <div class="bar" id="jBar"><i></i></div>
        <div class="log" id="jLog"></div>`;
      progressEl.appendChild(box);
    }
    box.querySelector('#jStep').textContent = j.step || '';
    box.querySelector('#jPct').textContent = `${j.pct || 0}%`;
    const bar = box.querySelector('#jBar');
    bar.firstElementChild.style.width = `${j.pct || 0}%`;
    bar.classList.toggle('ok', j.status === 'done');
    box.querySelector('#jLog').innerHTML = (j.log || [])
      .map((l) => `<div class="${l.startsWith('失败') ? 'err' : ''}">${esc(l)}</div>`)
      .join('');
    box.querySelector('#jLog').scrollTop = 1e6;
  }

  function stopJobPoll() {
    clearInterval(jobTimer);
    jobTimer = null;
  }

  async function submit() {
    if (busy) return;
    const url = $('#url').value.trim();
    const lang = $('#lang').value;
    if (!url) {
      toast('先粘一个视频链接', true);
      return;
    }
    busy = true;
    $('#go').disabled = true;
    $('#go').textContent = '生成中…';
    progressEl.querySelector('#jobBox')?.remove();

    try {
      const job = await api.create(url, lang);
      renderJob(job);
      stopJobPoll();
      jobTimer = setInterval(async () => {
        try {
          const j = await api.job(job.id);
          renderJob(j);
          if (j.status === 'done') {
            stopJobPoll();
            busy = false;
            $('#go').disabled = false;
            $('#go').textContent = '生成课程';
            $('#url').value = '';
            refresh();
            toast('课程已生成');
            location.hash = `/play/${encodeURIComponent(j.courseId)}`;
          } else if (j.status === 'error') {
            stopJobPoll();
            busy = false;
            $('#go').disabled = false;
            $('#go').textContent = '生成课程';
            toast(j.error || '生成失败', true);
          }
        } catch (e) {
          stopJobPoll();
          busy = false;
          $('#go').disabled = false;
          $('#go').textContent = '生成课程';
        }
      }, 700);
      disposers.push(stopJobPoll);
    } catch (e) {
      busy = false;
      $('#go').disabled = false;
      $('#go').textContent = '生成课程';
      toast(String(e && e.message ? e.message : e), true);
    }
  }

  $('#go').onclick = submit;
  $('#url').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') submit();
  });

  return () => {
    disposers.forEach((fn) => fn());
    disposers = [];
  };
}
