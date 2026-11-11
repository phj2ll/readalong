import { api, esc, fmtTime, toast } from '../api.js';

// 评测设置弹窗（桌面版和 Reader 共用）
export function openSettingsModal() {
  let s = { appId: '', apiKey: '', apiSecret: '' };
  const load = api
    .settings()
    .then((v) => {
      s = { ...s, ...v };
    })
    .catch(() => {});

  let el = document.getElementById('iseModal');
  if (!el) {
    el = document.createElement('div');
    el.id = 'iseModal';
    el.innerHTML = `
      <div class="modal-mask"></div>
      <div class="modal">
        <h2>讯飞语音评测设置</h2>
        <p class="hint">在 console.xfyun.cn 的「语音评测（流式版）」服务里获取。配置后，练习页录完音会自动打分。</p>
        <label>APPID<input type="text" id="iseAppId" spellcheck="false" autocomplete="off" /></label>
        <label>APIKey<input type="text" id="iseKey" spellcheck="false" autocomplete="off" /></label>
        <label>APISecret<input type="password" id="iseSecret" spellcheck="false" autocomplete="off" /></label>
        <div class="modal-acts">
          <button class="btn" id="iseCancel">取消</button>
          <button class="btn primary" id="iseSave">保存</button>
        </div>
      </div>`;
    document.body.appendChild(el);
  }
  const fill = () => {
    el.querySelector('#iseAppId').value = s.appId || '';
    el.querySelector('#iseKey').value = s.apiKey || '';
    el.querySelector('#iseSecret').value = s.apiSecret || '';
  };
  load.then(fill);
  el.style.display = 'block';
  fill();
  el.querySelector('.modal-mask').onclick = () => (el.style.display = 'none');
  el.querySelector('#iseCancel').onclick = () => (el.style.display = 'none');
  el.querySelector('#iseSave').onclick = async () => {
    try {
      await api.saveSettings({
        appId: el.querySelector('#iseAppId').value.trim(),
        apiKey: el.querySelector('#iseKey').value.trim(),
        apiSecret: el.querySelector('#iseSecret').value.trim(),
      });
      el.style.display = 'none';
      toast('评测设置已保存');
    } catch (e) {
      toast(String(e && e.message ? e.message : e), true);
    }
  };
}

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
      <button class="btn small" id="iseSet">评测设置</button>
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

  // -------- 评测设置（共用弹窗） --------
  $('#iseSet').onclick = openSettingsModal;

  // -------- 课程列表（按书分组） --------
  // 老课程没有 bookId 字段，按课程 ID 的 BV 前缀归组
  function bookOf(c) {
    const bid = c.bookId || (String(c.id).match(/^(BV[0-9A-Za-z]+)/) || [])[1] || c.id;
    let btitle = c.bookTitle || '';
    if (!btitle && c.title && c.title.includes('·')) {
      btitle = c.title.split('·').pop().trim();
    }
    let label = c.title || c.id;
    if (label.includes('·')) label = label.split('·')[0].trim();
    return { bid, btitle: btitle || bid, label };
  }

  // 用户自定义过的书籍信息（书名 / 封面），bid → { id, title, cover, mtime }
  let bookMeta = {};

  function bookTitleOf(b) {
    const m = bookMeta[b.bid];
    return (m && m.title) || b.btitle;
  }

  function coverUrlOf(bid) {
    const m = bookMeta[bid];
    if (!m || !m.cover) return '';
    // mtime 做版本号，换封面后立刻刷新缓存
    return `/books/${encodeURIComponent(m.cover)}?v=${m.mtime || 0}`;
  }

  // 改书名的小弹窗（Wails 的 WebView 里 window.prompt 不可靠，自己画一个）
  function askBookTitle(cur) {
    return new Promise((resolve) => {
      let el = document.getElementById('bookModal');
      if (!el) {
        el = document.createElement('div');
        el.id = 'bookModal';
        el.innerHTML = `
          <div class="modal-mask"></div>
          <div class="modal">
            <h2>修改书名</h2>
            <p class="hint">书名显示在课程库和手机端书架，导出课程包时也会带上。</p>
            <label>书名<input type="text" id="bookTitleInput" maxlength="60" spellcheck="false" autocomplete="off" /></label>
            <div class="modal-acts">
              <button class="btn" id="bookCancel">取消</button>
              <button class="btn primary" id="bookSave">保存</button>
            </div>
          </div>`;
        document.body.appendChild(el);
      }
      const input = el.querySelector('#bookTitleInput');
      input.value = cur || '';
      const done = (v) => {
        el.style.display = 'none';
        resolve(v);
      };
      el.querySelector('.modal-mask').onclick = () => done(null);
      el.querySelector('#bookCancel').onclick = () => done(null);
      el.querySelector('#bookSave').onclick = () => done(input.value.trim());
      input.onkeydown = (e) => {
        if (e.key === 'Enter') done(input.value.trim());
        if (e.key === 'Escape') done(null);
      };
      el.style.display = 'block';
      setTimeout(() => {
        input.focus();
        input.select();
      }, 30);
    });
  }

  function renderCourses(list) {
    if (!list || !list.length) {
      coursesEl.innerHTML = `<div class="empty">还没有课程。上面粘一个链接试试，第一次会先下载语音模型。</div>`;
      return;
    }
    const books = [];
    const byBook = {};
    for (const c of list) {
      const b = bookOf(c);
      if (!byBook[b.bid]) {
        byBook[b.bid] = { ...b, courses: [], sentences: 0, latest: c.created || '' };
        books.push(byBook[b.bid]);
      }
      byBook[b.bid].courses.push(c);
      byBook[b.bid].sentences += c.count || 0;
      if ((c.created || '') > byBook[b.bid].latest) byBook[b.bid].latest = c.created;
    }

    coursesEl.innerHTML = books
      .map((b) => {
        const cov = coverUrlOf(b.bid);
        return `
      <div class="card book">
        <div class="book-head">
          <div class="book-cover">${cov ? `<img src="${esc(cov)}" alt="" />` : '📘'}</div>
          <div class="info">
            <div class="t">${esc(bookTitleOf(b))}</div>
            <div class="m"><span>${b.courses.length} 课</span><span>· ${b.sentences} 句</span><span>· ${esc(b.latest)}</span></div>
          </div>
          <div class="acts">
            <button class="btn small" data-rename="${esc(b.bid)}">改书名</button>
            <button class="btn small" data-cover="${esc(b.bid)}">换封面</button>
            <button class="btn small" data-exp="${esc(b.bid)}" title="整本书（课程 + 音频 + 视频）导出，首次导入手机时用">导出整本书</button>
            <button class="btn small" data-expi="${esc(b.bid)}" title="只含书名和封面，几百 KB；改完书名/封面后用它同步到手机">导出书信息</button>
            <button class="btn small danger" data-delbook="${esc(b.bid)}">删除整本</button>
          </div>
        </div>
        <div class="units">
          ${b.courses
            .map(
              (c) => `
          <div class="unit">
            <span class="unit-label">${esc(bookOf(c).label)}</span>
            <span class="unit-meta">${c.count} 句</span>
            <button class="btn small primary" data-open="${esc(c.id)}">打开</button>
            <button class="btn small danger" data-del="${esc(c.id)}">删除</button>
          </div>`
            )
            .join('')}
        </div>
      </div>`;
      })
      .join('');

    coursesEl.querySelectorAll('[data-open]').forEach((b) => {
      b.onclick = () => {
        location.hash = `/play/${encodeURIComponent(b.dataset.open)}`;
      };
    });
    coursesEl.querySelectorAll('[data-rename]').forEach((b) => {
      b.onclick = async () => {
        const bid = b.dataset.rename;
        const book = books.find((x) => x.bid === bid);
        const cur = book ? bookTitleOf(book) : '';
        const title = await askBookTitle(cur);
        if (title == null || !title || title === cur) return;
        try {
          await api.renameBook(bid, title);
          refresh();
          toast('书名已更新');
        } catch (e) {
          toast(String(e && e.message ? e.message : e), true);
        }
      };
    });
    coursesEl.querySelectorAll('[data-cover]').forEach((b) => {
      b.onclick = async () => {
        try {
          const info = await api.pickCover(b.dataset.cover);
          if (info && info.cover) {
            refresh();
            toast('封面已更新');
          }
        } catch (e) {
          toast(String(e && e.message ? e.message : e), true);
        }
      };
    });
    coursesEl.querySelectorAll('[data-exp]').forEach((b) => {
      b.onclick = async () => {
        try {
          toast('正在导出整本书…');
          const path = await api.exportBook(b.dataset.exp);
          if (path) toast(`已导出：${path}`);
        } catch (e) {
          toast(String(e && e.message ? e.message : e), true);
        }
      };
    });
    coursesEl.querySelectorAll('[data-expi]').forEach((b) => {
      b.onclick = async () => {
        try {
          const path = await api.exportBookInfo(b.dataset.expi);
          if (path) toast(`已导出书信息：${path}（手机导入即可更新书名/封面）`);
        } catch (e) {
          toast(String(e && e.message ? e.message : e), true);
        }
      };
    });
    coursesEl.querySelectorAll('[data-delbook]').forEach((b) => {
      b.onclick = async () => {
        const book = books.find((x) => x.bid === b.dataset.delbook);
        const n = book ? book.courses.length : 0;
        const name = book ? bookTitleOf(book) : '这本书';
        if (!confirm(`删除「${name}」的全部 ${n} 门课？音频和数据都会被删掉，不可恢复。`)) return;
        try {
          for (const c of book.courses) {
            await api.del(c.id);
          }
          refresh();
          toast('整本书已删除');
        } catch (e) {
          toast(String(e && e.message ? e.message : e), true);
        }
      };
    });
    coursesEl.querySelectorAll('[data-del]').forEach((b) => {
      b.onclick = async () => {
        if (!confirm('删除这门课？音频和数据都会被删掉。')) return;
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
    Promise.all([api.list().catch(() => []), api.books().catch(() => [])])
      .then(([list, metas]) => {
        bookMeta = {};
        for (const m of metas || []) {
          if (m && m.id) bookMeta[m.id] = m;
        }
        renderCourses(list || []);
      })
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
            if (j.courseId) {
              toast('课程已生成');
              location.hash = `/play/${encodeURIComponent(j.courseId)}`;
            } else {
              toast('批量生成完成，课程列表见下方');
            }
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
