// ReadAlong Reader：纯前端的课程阅读端。
// 桌面 Studio「导出整本书」→ 这里导入 zip → 选书 → 选单元（Unit1/P3…）→ 练习（单词/课文/朗读）。
// 复用 Studio 的练习页组件（renderPlayer），评分走 JS 直连讯飞。

import './style.css';
import './mobile.css';
import { unzipSync } from 'fflate';
import { api, esc, fmtTime, toast } from './api.js';
import { idb } from './idb.js';
import { recstore } from './recstore.js';
import { BACK_SVG } from './icons.js';
import { renderPlayer } from './views/player.js';
import { openSettingsModal } from './views/library.js';

const root = document.getElementById('app');
let cleanup = null;

// ---------- 路由 ----------
function route() {
  if (typeof cleanup === 'function') {
    try {
      cleanup();
    } catch (e) {
      /* 忽略 */
    }
    cleanup = null;
  }
  const hash = location.hash.replace(/^#/, '');
  let m = hash.match(/^\/play\/([^/]+)$/);
  if (m) {
    cleanup = renderPlayer(root, decodeURIComponent(m[1]));
    return;
  }
  m = hash.match(/^\/book\/([^/]+)$/);
  if (m) {
    cleanup = renderBook(decodeURIComponent(m[1]));
    return;
  }
  cleanup = renderShelf();
}
window.addEventListener('hashchange', route);

// 回书架：改 hash 会触发 hashchange→route()，再手动 route() 会渲染两遍（书架闪一下，
// 封面 Blob URL 也会白白创建两次）
function goHome() {
  if (location.hash === '' || location.hash === '#' || location.hash === '#/') route();
  else location.hash = '#/';
}

// ---------- 导入课程包（整本书 / 单个课程 / 只含书名封面的小包） ----------

// 封面在包里的文件名：优先 book.json 声明的，其次按 cover.<ext> 找
function pickCoverFile(names, manifest) {
  if (manifest.cover && names.includes(manifest.cover)) return manifest.cover;
  return names.find((x) => /^cover\.(jpg|jpeg|png|webp|gif|bmp)$/i.test(x)) || '';
}

function imageTypeOf(name) {
  if (/\.png$/i.test(name)) return 'image/png';
  if (/\.webp$/i.test(name)) return 'image/webp';
  if (/\.gif$/i.test(name)) return 'image/gif';
  if (/\.bmp$/i.test(name)) return 'image/bmp';
  return 'image/jpeg';
}

async function importPackage(file) {
  if (!/\.zip$/i.test(file.name)) {
    toast('请选择 .zip 课程包（桌面 Studio 的「导出整本书」生成）', true);
    return;
  }
  let raw;
  try {
    raw = unzipSync(new Uint8Array(await file.arrayBuffer()));
  } catch (e) {
    toast('课程包解压失败：' + (e.message || e), true);
    return;
  }
  // 有些打包工具（Windows 资源管理器「压缩为 zip」）用反斜杠做条目分隔符，
  // 不符合 ZIP 规范；不统一成正斜杠的话 <课程ID>/data.json 一个都匹配不上
  const names = [];
  const files = {};
  for (const n of Object.keys(raw)) {
    const k = n.replace(/\\/g, '/');
    names.push(k);
    files[k] = raw[n];
  }
  const text = (n) => new TextDecoder().decode(files[n]);

  // 课程包里附带了评测凭据（PC 导出时写入 app-settings.json）：自动配置，免手填
  let credsMsg = '课程包未含评测凭据，可在「评测设置」里手动填一次';
  if (names.includes('app-settings.json')) {
    try {
      const s = JSON.parse(text('app-settings.json'));
      if (s && s.appId && s.apiKey && s.apiSecret) {
        await api.saveSettings({ appId: s.appId, apiKey: s.apiKey, apiSecret: s.apiSecret });
        credsMsg = '已自动配置评测凭据';
      }
    } catch (e) {
      credsMsg = '评测凭据写入失败：' + (e.message || e);
    }
  }
  // 收集这门课的视频课文件（video-*.mp4）
  const videosOf = (courseId) => {
    const out = {};
    for (const n of names) {
      if (n.startsWith(courseId + '/video-') && n.endsWith('.mp4')) {
        const name = n.slice(courseId.length + 1);
        out[name] = new Blob([files[n]], { type: 'video/mp4' });
      }
    }
    return out;
  };
  const put = async (data, blob, bookId, bookTitle) => {
    await idb.put({
      id: data.id,
      title: data.title || data.id,
      source: data.source || '',
      uploader: data.uploader || '',
      duration: data.duration || 0,
      count: (data.segments || []).length,
      created: data.created || '',
      bookId: data.bookId || bookId || '',
      bookTitle: data.bookTitle || bookTitle || '',
      data,
      audioBlob: blob,
      videoBlobs: videosOf(data.id),
    });
  };

  // 新格式：book.json + <课程ID>/data.json + <课程ID>/audio.wav
  if (names.includes('book.json')) {
    let manifest;
    try {
      manifest = JSON.parse(text('book.json'));
    } catch (e) {
      toast('book.json 解析失败', true);
      return;
    }
    // 小包（infoOnly）：只带书名和封面，改完信息后用它同步，不动课程
    if (manifest.infoOnly) {
      const mine = (await allCourses()).filter((c) => bookOf(c).bid === manifest.id);
      if (!mine.length) {
        toast('这本书还没导入课程，请先导入完整课程包', true);
        return;
      }
      try {
        const coverFile = pickCoverFile(names, manifest);
        const prev = await idb.getBook(manifest.id);
        await idb.putBook({
          id: manifest.id,
          title: manifest.title || (prev && prev.title) || '',
          coverBlob: coverFile
            ? new Blob([files[coverFile]], { type: imageTypeOf(coverFile) })
            : prev
              ? prev.coverBlob
              : null,
        });
      } catch (e) {
        toast('书名/封面写入失败：' + (e.message || e), true);
        return;
      }
      toast(`已更新「${manifest.title}」的书名和封面`);
      goHome();
      return;
    }

    let n = 0;
    for (const c of manifest.courses || []) {
      const d = `${c.id}/data.json`;
      const a = `${c.id}/audio.wav`;
      if (!names.includes(d) || !names.includes(a)) continue;
      try {
        const data = JSON.parse(text(d));
        await put(data, new Blob([files[a]], { type: 'audio/wav' }), manifest.id, manifest.title);
        n++;
      } catch (e) {
        /* 单门课坏了不影响整本 */
      }
    }
    if (!n) {
      toast('课程包里没有可导入的课程', true);
      return;
    }
    // 书名/封面：新版课程包会带 cover.jpg + book.json 里的 title
    try {
      const coverFile = pickCoverFile(names, manifest);
      const prev = await idb.getBook(manifest.id);
      let coverBlob = prev ? prev.coverBlob : null;
      if (coverFile) {
        coverBlob = new Blob([files[coverFile]], { type: imageTypeOf(coverFile) });
      }
      await idb.putBook({ id: manifest.id, title: manifest.title || '', coverBlob });
    } catch (e) {
      /* 封面存不进去不影响课程本身 */
    }
    toast(`已导入「${manifest.title}」（${n} 课）`);
    setTimeout(() => toast(credsMsg, credsMsg.includes('失败') || credsMsg.includes('未含')), 1200);
    goHome();
    return;
  }

  // 旧格式：单个课程（data.json + audio.wav 在根目录）
  const dataFile = names.find((n2) => n2.endsWith('data.json'));
  const audioFile = names.find((n2) => n2.endsWith('audio.wav'));
  if (!dataFile || !audioFile) {
    toast('课程包缺少 data.json / audio.wav', true);
    return;
  }
  try {
    const data = JSON.parse(text(dataFile));
    await put(data, new Blob([files[audioFile]], { type: 'audio/wav' }));
    toast(`已导入「${data.title || data.id}」`);
    goHome();
  } catch (e) {
    toast('data.json 解析失败', true);
  }
}

// ---------- 分组辅助（与课程库同一规则） ----------
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

async function allCourses() {
  return (await idb.all()).sort((a, b) => String(b.created).localeCompare(String(a.created)));
}

// ---------- 书架 ----------
function renderShelf() {
  root.innerHTML = `
    <div class="topbar">
      <div class="brand"><span class="dot"></span>一起读</div>
      <div class="spacer"></div>
      <button class="btn small" id="iseSet">评测设置</button>
      <button class="btn small primary" id="import">导入课程包</button>
      <input type="file" id="file" accept=".zip" hidden />
    </div>
    <div class="wrap">
      <div class="section-title">我的书架</div>
      <div id="books" class="courses"></div>
      <p class="rec-hint" style="text-align:center">
        课程包由电脑版「一起读」的「导出整本书」生成；导入后离线跟读，评分需联网。
      </p>
    </div>
  `;

  const $ = (s) => root.querySelector(s);
  const booksEl = $('#books');
  const fileInput = $('#file');

  $('#import').onclick = () => fileInput.click();
  fileInput.onchange = async () => {
    const f = fileInput.files[0];
    fileInput.value = '';
    if (f) await importPackage(f);
  };
  $('#iseSet').onclick = openSettingsModal;

  // 书名/封面：导入课程包时存在 books 存储里（PC 端改过的书名和上传的封面）
  const coverUrls = [];
  Promise.all([allCourses(), idb.allBooks().catch(() => [])])
    .then(([courses, bookRecords]) => {
      if (!courses.length) {
        booksEl.innerHTML = `<div class="empty">书架空空的。点右上角「导入课程包」选择 zip 文件。</div>`;
        return;
      }
      const metas = {};
      for (const m of bookRecords || []) {
        if (m && m.id) metas[m.id] = m;
      }
      const books = [];
      const byBook = {};
      for (const c of courses) {
        const b = bookOf(c);
        if (!byBook[b.bid]) {
          byBook[b.bid] = { ...b, n: 0, sentences: 0 };
          books.push(byBook[b.bid]);
        }
        byBook[b.bid].n++;
        byBook[b.bid].sentences += c.count || 0;
      }
      booksEl.innerHTML = books
        .map((b) => {
          const m = metas[b.bid];
          const title = (m && m.title) || b.btitle;
          let cov = '';
          if (m && m.coverBlob) {
            cov = URL.createObjectURL(m.coverBlob);
            coverUrls.push(cov);
          }
          return `
        <div class="card course">
          <span class="course-ico${cov ? ' has-cover' : ''}">${cov ? `<img src="${esc(cov)}" alt="" />` : '📚'}</span>
          <div class="course-info">
            <div class="course-name">${esc(title)}</div>
            <div class="course-meta">${b.n} 课 · ${b.sentences} 句</div>
          </div>
          <div class="course-acts">
            <button class="btn small primary" data-book="${esc(b.bid)}">打开</button>
            <button class="btn small ghost" data-delbook="${esc(b.bid)}">删除</button>
          </div>
        </div>`;
        })
        .join('');

      booksEl.querySelectorAll('[data-book]').forEach((el) => {
        el.onclick = () => {
          location.hash = `/book/${encodeURIComponent(el.dataset.book)}`;
        };
      });
      booksEl.querySelectorAll('[data-delbook]').forEach((el) => {
        el.onclick = async () => {
          if (!confirm('删除这本书的全部课程？')) return;
          const bid = el.dataset.delbook;
          for (const c of await allCourses()) {
            if (bookOf(c).bid === bid) {
              await recstore.clear(c.id).catch(() => {});
              await idb.del(c.id);
            }
          }
          await idb.delBook(bid).catch(() => {});
          route();
          toast('已删除');
        };
      });
    })
    .catch((e) => {
      booksEl.innerHTML = `<div class="empty">读取失败：${esc(e.message || e)}</div>`;
    });

  return () => {
    for (const u of coverUrls) URL.revokeObjectURL(u);
  };
}

// ---------- 书内页：单元列表 ----------
function renderBook(bookID) {
  root.innerHTML = `
    <div class="topbar">
      <a class="back" href="#/" title="返回书架">${BACK_SVG}</a>
      <div class="brand" id="bt">…</div>
      <div class="spacer"></div>
      <button class="btn small" id="iseSet">评测设置</button>
    </div>
    <div class="wrap">
      <div class="section-title">本书的课</div>
      <div id="units" class="courses"></div>
    </div>
  `;
  const $ = (s) => root.querySelector(s);
  $('#iseSet').onclick = openSettingsModal;

  allCourses()
    .then((courses) => {
      const mine = courses.filter((c) => bookOf(c).bid === bookID);
      if (!mine.length) {
        $('#units').innerHTML = `<div class="empty">这本书没有课程。</div>`;
        return;
      }
      $('#bt').textContent = bookOf(mine[0]).btitle;
      // 课程包带了书名就优先用（PC 端改过的书名）
      idb
        .getBook(bookID)
        .then((m) => {
          if (m && m.title) $('#bt').textContent = m.title;
        })
        .catch(() => {});
      $('#units').innerHTML = mine
        .map(
          (c) => `
        <div class="card course">
          <span class="course-ico">📖</span>
          <div class="course-info">
            <div class="course-name">${esc(bookOf(c).label)}</div>
            <div class="course-meta">${c.count} 句 · ${fmtTime(c.duration)}</div>
          </div>
          <div class="course-acts">
            <button class="btn small primary" data-open="${esc(c.id)}">开始</button>
            <button class="btn small ghost" data-del="${esc(c.id)}">删除</button>
          </div>
        </div>`
        )
        .join('');

      $('#units').querySelectorAll('[data-open]').forEach((el) => {
        el.onclick = () => {
          location.hash = `/play/${encodeURIComponent(el.dataset.open)}`;
        };
      });
      $('#units').querySelectorAll('[data-del]').forEach((el) => {
        el.onclick = async () => {
          if (!confirm('删除这门课？')) return;
          await recstore.clear(el.dataset.del).catch(() => {});
          await idb.del(el.dataset.del);
          route();
          toast('已删除');
        };
      });
    })
    .catch((e) => {
      $('#units').innerHTML = `<div class="empty">读取失败：${esc(e.message || e)}</div>`;
    });

  return () => {};
}

// 样式预览（开发用）：index.html?preview → 塞一门假课程，方便看任务页样式
if (new URLSearchParams(location.search).has('preview')) {
  const pv = (s, e, t, sec) => ({ start: s, end: e, text: t, section: sec });
  const pvData = {
    id: 'BVTEST_u3',
    title: '课文跟读unit 3 · 外研新交际英语一年级上册',
    duration: 100,
    segments: [
      pv(0, 1, 'Dad', '单词跟读unit 3'),
      pv(1.5, 2.5, 'Mum', '单词跟读unit 3'),
      pv(3, 4, 'Sister', '单词跟读unit 3'),
      pv(13, 17, 'Hello, hello', '课文跟读unit 3 · 英文歌曲'),
      pv(18, 22, 'Meet my family', '课文跟读unit 3 · 英文歌曲'),
      pv(23, 27, 'Hello, hello', '课文跟读unit 3 · 英文歌曲'),
      pv(40, 44, 'This is my mum.', '课文跟读unit 3 · 课文'),
      pv(45, 49, 'I love my mum.', '课文跟读unit 3 · 课文'),
      pv(50, 54, 'This is my dad.', '课文跟读unit 3 · 课文'),
    ],
    parts: [
      { title: '英文歌曲', section: '课文跟读unit 3 · 英文歌曲', start: 12, end: 30 },
      { title: '课文', section: '课文跟读unit 3 · 课文', start: 39, end: 55 },
    ],
  };
  idb.put({
    id: pvData.id,
    title: pvData.title,
    source: '',
    uploader: '',
    duration: 100,
    count: pvData.segments.length,
    created: '2026-11-11 12:00',
    bookId: 'BVTEST',
    bookTitle: '外研新交际英语一年级上册',
    data: pvData,
    audioBlob: new Blob([new ArrayBuffer(32044)], { type: 'audio/wav' }),
    videoBlobs: {},
  }).then(() => {
    location.hash = '/play/BVTEST_u3';
    route();
  });
}

route();
