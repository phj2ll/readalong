import { api, esc, fmtTime, isWails, toast } from '../api.js';
import { recstore } from '../recstore.js';
import { formatScore, scoreFor, scoreGrade } from '../score.js';

const MODES = [
  { key: 'loop', label: '单句循环' },
  { key: 'ab', label: 'A/B 对比' },
  { key: 'auto', label: '整篇连读' },
];

export function renderPlayer(root, courseId) {
  const state = {
    data: null,
    current: -1,
    mode: 'loop',
    rate: 1,
    done: new Set(),
    edits: {},
    mine: new Map(),
    mineUrl: null,
    stopAt: 0,
    onEnd: null,
    recording: false,
    recStarting: false, // 正在等麦克风授权（防连点开两个录音）
    recorder: null,
    chunks: [],
    stream: null,
    recIndex: -1,
    timer: null,
    scores: {},
    assessing: new Set(),
    iseReady: false,
    filter: null, // 任务视图筛选：null 看全部，或句号数组
    taskLabel: '', // 当前任务名（单词跟读 / 逐句跟读…），练习页标题用
    focusPos: 0, // 单词卡视图：当前词在任务里的位置
    recStart: 0, // 录音开始时间（圆环进度用）
    ringTimer: null,
    waveTimer: null,
    autoNextTimer: null,
    timers: new Set(), // 一次性定时器，路由切走时统一清掉
    vidTimer: null, // 视频课下载进度轮询
  };

  // 统一登记一次性定时器：路由切走后这些回调不该再操作已经销毁的页面
  function later(fn, ms) {
    const id = setTimeout(() => {
      state.timers.delete(id);
      fn();
    }, ms);
    state.timers.add(id);
    return id;
  }

  const keyDone = `ra.done.${courseId}`;
  const keyEdits = `ra.edits.${courseId}`;
  const keyScores = `ra.score.${courseId}`;
  const keyPos = `ra.pos.${courseId}`;
  try {
    state.done = new Set(JSON.parse(localStorage.getItem(keyDone) || '[]'));
  } catch (e) {
    state.done = new Set();
  }
  try {
    state.edits = JSON.parse(localStorage.getItem(keyEdits) || '{}');
  } catch (e) {
    state.edits = {};
  }
  try {
    state.scores = JSON.parse(localStorage.getItem(keyScores) || '{}');
  } catch (e) {
    state.scores = {};
  }
  try {
    state.pos = JSON.parse(localStorage.getItem(keyPos) || '{}') || {};
  } catch (e) {
    state.pos = {};
  }

  root.innerHTML = `
    <div class="player-head">
      <div class="row">
        <a class="back" href="#/" title="返回课程库">‹</a>
        <h1 id="title">加载中…</h1>
        <span class="sub" id="count"></span>
      </div>
      <div class="bar" style="margin-top:9px"><i id="doneBar"></i></div>
      <div class="toolbar">
        <div class="seg-control" id="modes">
          ${MODES.map((m) => `<button data-mode="${m.key}">${m.label}</button>`).join('')}
        </div>
        <div class="seg-control" id="rates">
          <button data-rate="1" class="on">1x</button>
          <button data-rate="0.75">0.75x</button>
        </div>
        <span class="chip" id="modeHint">点「跟读」：先听原音，然后自动录音，自动评分</span>
      </div>
    </div>
    <div class="wrap" id="list"></div>
    <audio id="audio" preload="auto"></audio>
  `;

  const $ = (s) => root.querySelector(s);
  const audio = $('#audio');
  const listEl = $('#list');
  // 手机端（Reader）：练习页用单词卡视图替代句子列表
  const isMobileView = () => document.body.classList.contains('mobile');

  // 单词卡左右滑动：一次性绑在列表容器上（重新渲染也不会丢），
  // pointer 事件同时覆盖触屏与鼠标拖拽
  let swipeX = 0;
  let swipeY = 0;
  let swipeOn = false;
  listEl.addEventListener('pointerdown', (e) => {
    if (!document.body.classList.contains('focus-mode')) return;
    if (e.target.closest('.focus-btn')) return; // 按钮上的拖动不算滑动
    swipeOn = true;
    swipeX = e.clientX;
    swipeY = e.clientY;
  });
  listEl.addEventListener('pointerup', (e) => {
    if (!swipeOn) return;
    swipeOn = false;
    const dx = e.clientX - swipeX;
    const dy = e.clientY - swipeY;
    if (Math.abs(dx) > 40 && Math.abs(dy) < 80) {
      // 手指从右往左滑（dx<0）= 下一个；从左往右滑 = 上一个
      focusGo(state.focusPos + (dx < 0 ? 1 : -1), true);
    }
  });
  listEl.addEventListener('pointercancel', () => {
    swipeOn = false;
  });

  function saveDone() {
    localStorage.setItem(keyDone, JSON.stringify([...state.done]));
  }
  function saveEdits() {
    localStorage.setItem(keyEdits, JSON.stringify(state.edits));
  }

  // ---------- 断点续读 ----------
  // state.pos 记「每个任务上次停在哪一句」（键是任务名，值是句子在整门课里的绝对序号）。
  // 存绝对序号而不是列表下标：筛选条件变了、重跑切句了，也不会跳错位置。
  function savePos(label, segIdx) {
    if (!label || segIdx == null || segIdx < 0) return;
    state.pos[label] = segIdx;
    try {
      localStorage.setItem(keyPos, JSON.stringify(state.pos));
    } catch (e) {
      /* 隐私模式写不了，忽略 */
    }
  }
  // 进任务时的落点：优先回上次位置；那一题已经练过就顺势落到后面第一个没练的
  function resumePosFor(t) {
    const saved = state.pos[t.label];
    if (saved == null) return { pos: 0, resumed: false };
    const k = t.indexes.indexOf(saved);
    if (k < 0) return { pos: 0, resumed: false };
    if (state.done.has(t.indexes[k])) {
      for (let j = k + 1; j < t.indexes.length; j++) {
        if (!state.done.has(t.indexes[j])) return { pos: j, resumed: true };
      }
    }
    return { pos: k, resumed: true };
  }

  function updateCount() {
    if (!state.data) return;
    const total = state.data.segments.length;
    // 练习页：头部显示「当前/总数」位置进度（参考图右侧的 3/8）
    if (document.body.classList.contains('focus-mode')) {
      const n = visibleIndexes().length || total;
      const p = Math.min(state.focusPos + 1, n);
      $('#count').textContent = `${p}/${n}`;
      $('#doneBar').style.width = n ? `${(p / n) * 100}%` : '0';
      return;
    }
    $('#count').textContent = `已练 ${state.done.size}/${total}`;
    $('#doneBar').style.width = total ? `${(state.done.size / total) * 100}%` : '0';
  }

  // ---------- 播放 ----------
  function courseAudioSrc() {
    return (state.data && state.data.audio) || `/courses/${courseId}/audio.wav`;
  }

  function stopAudio() {
    audio.pause();
    state.stopAt = 0;
    state.onEnd = null;
    // 播完自己的录音后 audio.src 还是 blob，这里兜底还原成课程音频
    if (state.mineUrl) {
      URL.revokeObjectURL(state.mineUrl);
      state.mineUrl = null;
      audio.src = courseAudioSrc();
    }
  }

  function playRange(start, end, onEnd) {
    stopAudio();
    state.stopAt = end + 0.06;
    state.onEnd = onEnd || null;
    try {
      audio.currentTime = Math.max(0, start - 0.04);
    } catch (e) {
      /* 尚未加载完 */
    }
    audio.play().catch(() => toast('音频还没加载好，稍后再试', true));
  }

  audio.addEventListener('timeupdate', () => {
    // 课文朗读（整篇连读）：按时间高亮当前句
    if (state.mode === 'auto' && !audio.paused && state.data) {
      const t = audio.currentTime;
      for (const i of visibleIndexes()) {
        const s = state.data.segments[i];
        if (t >= s.start - 0.05 && t <= s.end + 0.15) {
          if (state.current !== i) highlight(i);
          break;
        }
      }
    }
    if (!state.stopAt) return;
    if (audio.currentTime >= state.stopAt) {
      audio.pause();
      state.stopAt = 0;
      const cb = state.onEnd;
      state.onEnd = null;
      if (cb) cb();
    }
  });

  // 录音片段是自然播完的，走 ended 而不是 timeupdate
  audio.addEventListener('ended', () => {
    const cb = state.onEnd;
    state.onEnd = null;
    state.stopAt = 0;
    if (cb) cb();
  });

  function highlight(i) {
    state.current = i;
    savePos(state.taskLabel, i); // 跟着听到哪记到哪（没进任务时 taskLabel 为空，不记）
    const cards = listEl.querySelectorAll('.seg, .rd');
    cards.forEach((el) => el.classList.toggle('active', parseInt(el.dataset.i, 10) === i));
    const el = Array.from(cards).find((el) => parseInt(el.dataset.i, 10) === i);
    if (el) el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }

  function playSegment(i, after) {
    if (!state.data) return;
    const seg = state.data.segments[i];
    if (!seg) return;
    highlight(i);
    playRange(seg.start, seg.end, () => {
      if (after) {
        after();
        return;
      }
      if (state.mode === 'auto' && i + 1 < state.data.segments.length) {
        later(() => playSegment(i + 1), 420);
      }
    });
  }

  // 跟读主流程：播原音 → 播完自动开录 → 到时自动停（可手动提前停）
  function practice(i) {
    if (state.recording) {
      if (i === state.recIndex) {
        stopRec();
      } else {
        stopRec();
        later(() => practice(i), 120);
      }
      return;
    }
    stopAudio();
    highlight(i);
    playSegment(i, () => startRec(i));
  }

  async function playMine(i) {
    // state.mine 只是本次会话的缓存；录音本体在 IndexedDB 里，没缓存就去捞一次
    let blob = state.mine.get(i);
    if (!blob) {
      try {
        blob = await recstore.get(courseId, i);
      } catch (e) {
        blob = undefined;
      }
      if (blob) state.mine.set(i, blob);
    }
    if (!blob) {
      toast('这句还没录音', true);
      return;
    }
    highlight(i);
    stopAudio();
    const url = URL.createObjectURL(blob);
    state.mineUrl = url;
    audio.src = url;
    state.stopAt = 0;
    state.onEnd = () => {
      audio.src = courseAudioSrc();
      URL.revokeObjectURL(url);
      state.mineUrl = null;
    };
    audio.play().catch(() => toast('播放录音失败', true));
  }

  // ---------- 录音 ----------
  // 进课程时把存过的录音读回缓存，否则刷新后「我的录音」全是空的
  async function loadClips() {
    try {
      const saved = await recstore.all(courseId);
      for (const [idx, blob] of saved) state.mine.set(idx, blob);
    } catch (e) {
      /* 读不出来就当没录过，不影响练习 */
    }
  }

  async function startRec(i) {
    if (state.recording) {
      stopRec();
      return;
    }
    // 连点保护：getUserMedia 是异步的，等待期间 recording 仍是 false，
    // 不挡住会并发开两个录音（一个流被覆盖后麦克风常开，且录到错误的句子）
    if (state.recStarting) return;
    stopAudio(); // 录音时不能让原音还开着
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      toast('当前环境不支持录音', true);
      return;
    }
    state.recStarting = true;
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      });
    } catch (e) {
      state.recStarting = false;
      toast('麦克风不可用，检查系统权限后重试', true);
      return;
    }

    state.stream = stream;
    state.chunks = [];
    state.recIndex = i;
    state.recorder = new MediaRecorder(stream);
    state.recorder.ondataavailable = (ev) => {
      if (ev.data && ev.data.size) state.chunks.push(ev.data);
    };
    state.recorder.onstop = () => {
      const blob = new Blob(state.chunks, { type: 'audio/webm' });
      const idx = state.recIndex;
      state.mine.set(idx, blob);
      // 落盘（IndexedDB）：重录覆盖同一条，刷新后还能回放对比
      recstore.put(courseId, idx, blob).catch(() => toast('录音保存失败，本次仍可回放', true));
      state.done.add(idx);
      saveDone();
      savePos(state.taskLabel, idx); // 记住练到哪，下次进来直接续读
      updateCount();
      markDone(idx);
      if (state.iseReady) assess(idx, blob);
      if (state.mode === 'ab') {
        later(() => playMine(idx), 400); // A/B 对比：录完自动放你的录音
      }
    };

    state.recorder.start();
    state.recording = true;
    state.recStarting = false;
    const seg = state.data.segments[i];
    // 自动停：原句时长的 1.5 倍再留 2.5 秒，最短 4 秒
    const limit = Math.max(4000, (seg.end - seg.start) * 1000 * 1.5 + 2500);
    state.recStart = Date.now();
    if (isMobileView()) beginRing(limit);
    state.timer = setTimeout(() => stopRec(), limit);
    refreshRecButtons();
    toast('录音中…读完会自动结束');
  }

  function stopRec() {
    if (!state.recording) return;
    state.recording = false;
    clearTimeout(state.timer);
    if (isMobileView()) endRing();
    try {
      state.recorder.stop();
    } catch (e) {
      /* 忽略 */
    }
    if (state.stream) {
      state.stream.getTracks().forEach((t) => t.stop());
      state.stream = null;
    }
    refreshRecButtons();
  }

  function refreshRecButtons() {
    listEl.querySelectorAll('.seg').forEach((el) => {
      const b = el.querySelector('[data-practice]');
      if (!b) return;
      const i = parseInt(el.dataset.i, 10);
      const on = state.recording && i === state.recIndex;
      b.textContent = on ? '停止' : '跟读';
      b.classList.toggle('danger', on);
      el.classList.toggle('done', state.done.has(i));
    });
  }

  // 按 data-i 找句子：任务筛选视图下列表只渲染部分句子，用数组下标会标错行
  function segEl(i) {
    return listEl.querySelector(`.seg[data-i="${i}"]`);
  }

  function markDone(i) {
    const el = segEl(i);
    if (el) el.classList.add('done');
  }

  // ---------- 评分（讯飞 ISE） ----------
  // 任务行右侧的分数：100 分制，分母是全部题目（没评过的按 0 分算），
  // 所以「10 个词读了 2 个」显示 20 分，一眼能看出完成度。
  function taskScoreBox(t) {
    const s = scoreFor(state.scores, t.indexes);
    const tip = `已评 ${s.n}/${s.total}${t.cat === '单词' ? ' 个词' : ' 句'}，每项取历史最好成绩`;
    return (
      `<span class="task-score ${scoreGrade(s.score)}" title="${esc(tip)}">${formatScore(s.score)}</span>` +
      `<span class="task-score-max">/100</span>` +
      `<span class="task-go">›</span>`
    );
  }

  // 任务副标题（仿外研U学）：
  // 逐句跟读/单词/歌谣 按「句/个词」算——已经拆成一句一句，再报「几段」没用；
  // 课文朗读 按「部分」算——点进去就是「课文1 / 英文歌曲 / 课文2」的分部目录
  function taskSub(t) {
    if (t.sub) return t.sub;
    if (t.mode === 'auto') {
      return `共 ${Math.max(1, t.parts)} 部分`;
    }
    const unit = t.cat === '单词' ? '个词' : '句';
    const s = scoreFor(state.scores, t.indexes);
    const doneN = t.indexes.reduce((n, i) => n + (state.done.has(i) ? 1 : 0), 0);
    let text = `共 ${t.indexes.length} ${unit}`;
    if (doneN) text += ` · 已练 ${doneN}`;
    if (doneN || s.n) text += ` · 已评 ${s.n}`;
    return text;
  }

  // 有进度才显示「重置」，没练过的任务不啰嗦
  function hasProgress(t) {
    return t.indexes.some((i) => state.done.has(i)) || scoreFor(state.scores, t.indexes).n > 0;
  }

  function scoreChipText(i) {
    const s = state.scores[i];
    return s && s.total > 0 ? `${Math.round(s.total)}分` : '';
  }

  function updateScoreBadge(i) {
    const el = segEl(i);
    if (!el) return;
    const chip = el.querySelector('[data-score]');
    if (!chip) return;
    const s = state.scores[i];
    if (state.assessing.has(i)) {
      chip.className = 'score-chip assessing';
      chip.textContent = '评分中…';
      return;
    }
    if (!s || !(s.total > 0)) {
      chip.className = 'score-chip';
      chip.textContent = '';
      return;
    }
    chip.className = `score-chip ${scoreGrade(s.total)}`;
    chip.textContent = `${Math.round(s.total)}分`;
    chip.title = `总分 ${s.total} · 准确度 ${s.accuracy} · 流利度 ${s.fluency}${s.integrity ? ` · 完整度 ${s.integrity}` : ''}`;
  }

  async function assess(i, blob) {
    const seg = state.data && state.data.segments[i];
    if (!seg) return;
    state.assessing.add(i);
    updateScoreBadge(i);
    try {
      const text = state.edits[i] ?? seg.text;
      const buf = await blob.arrayBuffer();
      const res = await api.assess(text, state.data.lang || 'en', new Uint8Array(buf));
      // 只保留历史最好成绩
      const prev = state.scores[i];
      if (!prev || res.total >= prev.total) {
        state.scores[i] = res;
        localStorage.setItem(keyScores, JSON.stringify(state.scores));
      }
      toast(`第 ${i + 1} 句评分：${Math.round(res.total)} 分`);
    } catch (e) {
      const msg = String(e && e.message ? e.message : e);
      toast(`评分失败：${msg}`, true);
    } finally {
      state.assessing.delete(i);
      updateScoreBadge(i);
      if (isMobileView()) onFocusScore(i);
    }
  }

  // ---------- 渲染句子 ----------
  // 从分 P 标题里识别内容类型：单词 / 课文 / 歌谣 / 复习
  function detectCategory(section) {
    const t = (section || '').toLowerCase();
    if (/单词|词汇|word|vocabulary|字母|letter|alphabet/.test(t)) return ['单词', 'word'];
    if (/歌谣|歌曲|韵文|唱|chant|song|rhyme|sing/.test(t)) return ['歌谣', 'song'];
    if (/复习|测验|总结|review|test|quiz|summary/.test(t)) return ['复习', 'review'];
    if (/课文|文本|对话|阅读|故事|读|text|talk|dialog|story|read|listen|start|activity|练习/.test(t)) return ['课文', 'text'];
    return null;
  }

  function visibleIndexes() {
    if (!state.filter) return state.data.segments.map((_, i) => i);
    return state.filter;
  }

  // 任务视图：仿外研U学「Unit 内先选任务类型」
  const CAT_LABEL = { 单词: '单词跟读', 课文: '逐句跟读', 歌谣: '歌谣跟读', 复习: '同步练习' };
  const CAT_ICON = { 单词: '🔤', 课文: '🎧', 歌谣: '🎵', 复习: '✏️' };
  const CAT_CLS = { 单词: 'ico-word', 课文: 'ico-text', 歌谣: 'ico-song', 复习: 'ico-review' };

  function buildTasks() {
    const secs = [];
    let cur = null;
    state.data.segments.forEach((seg, i) => {
      if (!seg.section) return;
      if (!cur || cur.title !== seg.section) {
        cur = { title: seg.section, indexes: [] };
        secs.push(cur);
      }
      cur.indexes.push(i);
    });
    const groups = [];
    const byCat = {};
    for (const s of secs) {
      // 分类只看分 P 主名（「 · 」之前的部分），
      // 否则「课文跟读unit 2 · 单词朗读」会被误判成单词，混进单词跟读任务
      const base = s.title.split(' · ')[0];
      const d = detectCategory(base);
      const cat = d ? d[0] : '课文';
      if (!byCat[cat]) {
        byCat[cat] = { cat, indexes: [], parts: 0 };
        groups.push(byCat[cat]);
      }
      byCat[cat].indexes.push(...s.indexes);
      byCat[cat].parts++;
    }
    // 任务排序：单词 → 歌谣 → 课文 → 复习（仿外研U学：先单词后课文）
    const order = { 单词: 0, 歌谣: 1, 课文: 2, 复习: 3 };
    groups.sort((a, b) => (order[a.cat] ?? 9) - (order[b.cat] ?? 9));

    const tasks = [];
    for (const g of groups) {
      tasks.push({ label: CAT_LABEL[g.cat] || '逐句跟读', cat: g.cat, indexes: g.indexes, parts: g.parts, mode: 'loop' });
      if (g.cat === '课文') {
        tasks.push({ label: '课文朗读', cat: g.cat, indexes: g.indexes, parts: g.parts, mode: 'auto' });
      }
    }
    return tasks;
  }

  // ---------- 视频课：全屏播放层 ----------
  // 只在应用内播本地 mp4（离线可看），不提供「用浏览器打开」的入口。
  // 没下载到就不播：B 站嵌入播放器在应用内会触发登录墙（无 cookie 风控），
  // 而跳到系统浏览器会把孩子带出 App，体验和可控性都差。
  let videoEl = null; // 全屏层（挂在 body 上，跨 renderTasks 复用）
  let videoUrl = ''; // 当前 blob URL，关闭时必须 revoke
  let videoSeq = 0; // 加载序号：关闭后异步返回的结果要丢弃
  let videoPushed = false; // 是否为本层压了一条历史（Android 返回键关闭用）

  function ensureVideoEl() {
    if (videoEl) return videoEl;
    videoEl = document.createElement('div');
    videoEl.id = 'videoModal';
    videoEl.innerHTML = `
      <div class="video-bar">
        <button class="video-back" data-videoclose>‹ 返回</button>
        <span class="video-title" id="videoTitle"></span>
      </div>
      <div class="video-stage" id="videoBody"></div>`;
    document.body.appendChild(videoEl);
    videoEl.querySelector('[data-videoclose]').onclick = () => closeVideo(true);
    return videoEl;
  }

  // goBack=true 表示由「返回」按钮主动关闭：需要把压入的历史条目弹掉
  function closeVideo(goBack) {
    videoSeq++;
    const v = videoEl && videoEl.querySelector('video');
    if (v) {
      try {
        v.pause();
      } catch (e) {
        /* 忽略 */
      }
      v.removeAttribute('src');
    }
    if (videoUrl) {
      URL.revokeObjectURL(videoUrl);
      videoUrl = '';
    }
    if (!videoEl) return;
    videoEl.querySelector('#videoBody').innerHTML = '';
    videoEl.style.display = 'none';
    document.body.classList.remove('video-open');
    const pushed = videoPushed;
    videoPushed = false;
    if (goBack && pushed) {
      try {
        history.back();
      } catch (e) {
        /* 忽略 */
      }
    }
  }

  // Android 硬件返回键 / 浏览器后退：只关掉视频层，不退出课程
  function onVideoPop() {
    if (!videoPushed) return;
    closeVideo(false);
  }
  window.addEventListener('popstate', onVideoPop);

  async function openVideoModal(v) {
    const el = ensureVideoEl();
    const body = el.querySelector('#videoBody');

    el.querySelector('#videoTitle').textContent = v.title;

    // 先把全屏层亮出来并给个「加载中」，不要等文件取完才出现（原来的体验是毫无反馈）
    if (videoUrl) {
      URL.revokeObjectURL(videoUrl);
      videoUrl = '';
    }
    body.innerHTML = '<div class="video-loading">视频加载中…</div>';
    el.style.display = 'flex';
    document.body.classList.add('video-open');
    if (!videoPushed) {
      try {
        history.pushState({ videoLayer: 1 }, '');
        videoPushed = true;
      } catch (e) {
        /* 个别环境不支持，忽略 */
      }
    }
    const seq = ++videoSeq;
    const stale = () => seq !== videoSeq;

    // 内存数据可能是旧的（比如刚在别的入口补下载完）：先和后端对一次账
    if (!v.file) {
      try {
        const fresh = await api.course(courseId);
        if (stale()) return;
        const fv = (fresh.videos || []).find((x) => x.page === v.page);
        if (fv && fv.file) {
          state.data = fresh;
          v = fv;
          renderTasks();
        }
      } catch (e) {
        /* 对不上就用旧数据 */
      }
    }

    if (!v.file) {
      // 手机端（Reader）没有下载能力，视频只能随课程包带进来
      body.innerHTML = `<div class="video-fail">${
        isWails
          ? '这个视频课还没下载到本地<br>回任务列表点「补下载视频」'
          : '这个视频课没有随课程包导入<br>回课程库重新导入带视频的课程包'
      }</div>`;
      return;
    }
    try {
      videoUrl = await api.videoSrc(courseId, v.file);
    } catch (e) {
      if (!stale()) {
        body.innerHTML = '<div class="video-fail">视频读取失败：' + esc(String(e.message || e)) + '</div>';
      }
      return;
    }
    if (stale()) {
      URL.revokeObjectURL(videoUrl);
      videoUrl = '';
      return;
    }
    body.innerHTML = `<video controls autoplay playsinline webkit-playsinline src="${esc(videoUrl)}"></video>`;
    const vd = body.querySelector('video');
    vd.addEventListener('error', () => {
      body.innerHTML = `<div class="video-fail">视频文件损坏或丢失<br>${
        isWails ? '回任务列表点「补下载视频」' : '回课程库重新导入课程包'
      }</div>`;
    });
    // 自动播放被策略拦住时不影响播放器使用，点一下即可
    const p = vd.play();
    if (p && p.catch) p.catch(() => {});
  }

  // 补下载视频课（老课程没有本地文件时）
  async function reloadCourse() {
    try {
      state.data = await api.course(courseId);
    } catch (e) {
      /* 保留旧数据 */
    }
    renderTasks();
  }

  function bindVideoDownload() {
    const btn = listEl.querySelector('[data-viddl]');
    if (!btn) return;
    btn.onclick = async () => {
      btn.disabled = true;
      btn.textContent = '准备中…';
      let job;
      try {
        job = await api.downloadVideos(courseId);
      } catch (e) {
        // 后端说都下载过了 → 多半是页面数据是旧的，重拉一遍就好
        await reloadCourse();
        toast(String((e && e.message) || e), true);
        return;
      }
      let pollFails = 0;
      const timer = setInterval(async () => {
        try {
          const j = await api.job(job.id);
          pollFails = 0;
          if (j.status === 'done') {
            clearInterval(timer);
            await reloadCourse();
            toast('视频课已下载，离线可看');
          } else if (j.status === 'error') {
            clearInterval(timer);
            await reloadCourse();
            toast(j.error || '下载失败', true);
          } else {
            btn.textContent = j.step || '下载中…';
          }
        } catch (e) {
          // 查询失败不静默放弃：连续 3 次才提示并刷新
          if (++pollFails >= 3) {
            clearInterval(timer);
            await reloadCourse();
            toast('下载进度查询中断，列表已刷新，请确认视频状态', true);
          }
        }
      }, 1000);
      state.vidTimer = timer; // 路由切走时由 cleanup 停掉
    };
  }

  function renderTasks() {
    state.filter = null;
    state.taskLabel = ''; // 回到任务列表 = 不在任何任务里，别再往续读游标里写
    document.body.classList.remove('focus-mode');
    if (state.data) $('#title').textContent = state.data.title || '跟读练习';
    const tasks = buildTasks();
    const vids = state.data.videos || [];
    // 本课总分：分母是整门课的全部句子（没评过算 0 分）
    const courseSc = scoreFor(
      state.scores,
      state.data.segments.map((_, i) => i)
    );
    listEl.innerHTML = `
      <div class="card score-summary">
        <div class="ss-col">
          <div class="ss-label">本课得分</div>
          <div class="ss-num ${scoreGrade(courseSc.score)}">${formatScore(courseSc.score)}<small>/100</small></div>
        </div>
        <div class="ss-sep"></div>
        <div class="ss-col">
          <div class="ss-label">已评题数</div>
          <div class="ss-num">${courseSc.n}<small>/${courseSc.total}</small></div>
        </div>
      </div>
      <div class="card unit-card">
        <div class="unit-title">${esc(state.data.title)}</div>
        ${tasks
          .map(
            (t, k) => `
        <div class="task-row" data-task="${k}">
          <span class="task-ico ${CAT_CLS[t.cat] || 'ico-text'}">${CAT_ICON[t.cat] || '📖'}</span>
          <div class="task-info">
            <div class="task-name">${esc(t.label)}</div>
            <div class="task-sub">${esc(taskSub(t))}${
              t.mode !== 'auto' && hasProgress(t)
                ? `<button class="task-reset" data-reset="${k}" title="清空这个任务的已练记录和续读位置（分数保留）">重置</button>`
                : ''
            }</div>
          </div>
          <div class="task-score-box">${taskScoreBox(t)}</div>
        </div>`
          )
          .join('')}
        ${
          vids.length
            ? `<div class="video-divider">教学视频${
                vids.some((v) => !v.file)
                  ? ` <button class="btn small" data-viddl>补下载视频</button>`
                  : ''
              }</div>` +
              vids
                .map(
                  (v, k) => `
        <div class="task-row" data-video="${k}">
          <span class="task-ico ico-video">🎬</span>
          <div class="task-info">
            <div class="task-name">${esc(v.title)}</div>
            <div class="task-sub">${v.file ? '教学动画 · 已下载，离线可看' : '教学动画 · 未下载到本地'}</div>
          </div>
          <button class="btn small">观看</button>
        </div>`
                )
                .join('')
            : ''
        }
      </div>`;
    listEl.querySelectorAll('[data-task]').forEach((el) => {
      el.onclick = () => {
        const t = tasks[parseInt(el.dataset.task, 10)];
        state.filter = t.indexes;
        state.taskLabel = t.label;
        const { pos, resumed } = resumePosFor(t);
        state.focusPos = pos;
        state.readPart = null;
        setMode(t.mode);
        renderSegments();
        if (resumed) {
          toast(`已跳到上次位置 ${pos + 1}/${t.indexes.length}`);
          // 电脑端是整页列表，滚到那一句并闪一下
          const el2 = segEl(t.indexes[pos]);
          if (el2) {
            el2.scrollIntoView({ block: 'center' });
            el2.classList.add('flash');
            later(() => el2.classList.remove('flash'), 1400);
          }
        }
      };
    });
    // 重置本任务进度：清掉这部分的「已练」标记和游标，分数保留（取历史最好）
    listEl.querySelectorAll('[data-reset]').forEach((b) => {
      b.onclick = (e) => {
        e.stopPropagation();
        const t = tasks[parseInt(b.dataset.reset, 10)];
        if (!confirm(`清空「${t.label}」的练习进度？\n已记录的分数会保留。`)) return;
        for (const i of t.indexes) state.done.delete(i);
        saveDone();
        // 课文朗读和逐句跟读共用同一批句子，游标也是各记各的，一起清掉
        for (const tt of tasks) {
          if (tt.indexes === t.indexes) delete state.pos[tt.label];
        }
        try {
          localStorage.setItem(keyPos, JSON.stringify(state.pos));
        } catch (err) {
          /* 忽略 */
        }
        renderTasks();
        toast(`已重置「${t.label}」`);
      };
    });
    listEl.querySelectorAll('[data-video]').forEach((el) => {
      el.onclick = () => openVideoModal(vids[parseInt(el.dataset.video, 10)]);
    });
    bindVideoDownload();
  }

  function renderSegments() {
    if (state.mode === 'auto') {
      renderReading();
      return;
    }
    if (isMobileView()) {
      renderFocus(true); // 手机端：单词卡视图（进入自动播原声）
      return;
    }
    const idxs = visibleIndexes();
    let lastSection = '';
    const html = [];
    idxs.forEach((i) => {
      const seg = state.data.segments[i];
      if (seg.section && seg.section !== lastSection) {
        lastSection = seg.section;
        const cat = detectCategory(seg.section);
        const chip = cat ? `<span class="cat cat-${cat[1]}">${cat[0]}</span>` : '';
        html.push(`<div class="section-title">${esc(seg.section)}${chip}</div>`);
      }
      const text = state.edits[i] ?? seg.text;
      html.push(`
        <article class="card seg${state.done.has(i) ? ' done' : ''}" data-i="${i}">
          <div class="meta">
            <span class="idx">${i + 1}</span>
            <span>${seg.start.toFixed(1)}s – ${seg.end.toFixed(1)}s</span>
            <span>· ${(seg.end - seg.start).toFixed(1)}s</span>
          </div>
          <div class="en" contenteditable="true" spellcheck="false">${esc(text)}</div>
          <div class="acts">
            <button class="btn small primary" data-practice>跟读</button>
            <button class="btn small" data-play>原声</button>
            <button class="btn small" data-mine>我的录音</button>
            <span class="score-chip" data-score>${esc(scoreChipText(i))}</span>
          </div>
        </article>`);
    });

    listEl.innerHTML = `
      <div class="section-title">共 ${idxs.length} 句</div>
      ${html.join('')}`;

    listEl.querySelectorAll('.seg').forEach((el) => {
      const i = parseInt(el.dataset.i, 10);
      el.querySelector('[data-play]').onclick = () => playSegment(i);
      el.querySelector('[data-practice]').onclick = () => practice(i);
      el.querySelector('[data-mine]').onclick = () => playMine(i);

      const en = el.querySelector('.en');
      en.addEventListener('blur', () => {
        const v = en.textContent.trim();
        if (v && v !== state.data.segments[i].text) {
          state.edits[i] = v;
          saveEdits();
        }
      });
      en.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          en.blur();
        }
      });
    });
    updateCount();
  }

  // ---------- 手机端单词卡视图 ----------
  // 参考外研U学：一屏一词。左边喇叭听原声（进卡自动播），
  // 中间大话筒跟读（录音时外圈进度环、变 ❚❚ 可立即停），
  // 右边按钮听自己的录音；出分后自动/右滑到下一个词。
  const RING_C = 2 * Math.PI * 45;

  // 底部三颗按钮的图标（紫色喇叭 / 白色话筒 / 播放你的录音）
  const SVG_SPEAKER =
    '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M4 9.5h3.2L12 5.4v13.2L7.2 14.5H4z"/><path d="M15.4 8.4a4.6 4.6 0 010 7.2" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>';
  const SVG_MIC =
    '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 3.2a2.9 2.9 0 012.9 2.9v5.2a2.9 2.9 0 01-5.8 0V6.1A2.9 2.9 0 0112 3.2z"/><path d="M5.6 11.2a6.4 6.4 0 0012.8 0h-1.9a4.5 4.5 0 01-9 0z"/><path d="M11 17.9h2V21h-2z"/></svg>';
  const SVG_PLAY =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="3.2" y="4.6" width="17.6" height="13.2" rx="2.6"/><path d="M10.4 9.1l4.6 2.6-4.6 2.6z" fill="currentColor" stroke="none"/><path d="M8.4 21h7.2" stroke-linecap="round"/></svg>';

  // 未配置评测凭据时给个明确指引（APK 里凭据由用户手动填一次）
  const idleHint = () => (state.iseReady ? '点话筒跟读' : '先在书架页「评测设置」填讯飞凭据');

  function focusScoreHtml(i) {
    if (state.assessing.has(i)) return '<span class="score-wait">评分中…</span>';
    const s = state.scores[i];
    if (s && s.total > 0) {
      return (
        `<span class="score-num ${scoreGrade(s.total)}">${Math.round(s.total)}<small>分</small></span>` +
        `<span class="score-detail">准确 ${Math.round(s.accuracy || 0)} · 流利 ${Math.round(s.fluency || 0)}</span>`
      );
    }
    return '';
  }

  function renderFocus(autoplay) {
    const idxs = visibleIndexes();
    if (!idxs.length) return;
    if (state.focusPos >= idxs.length) state.focusPos = 0;
    const pos = state.focusPos;
    const i = idxs[pos];
    const seg = state.data.segments[i];
    const text = state.edits[i] ?? seg.text;

    // 练习页用紧凑头部：只留任务名 + 已练，别让紫色大块占屏幕
    document.body.classList.add('focus-mode');
    $('#title').textContent = state.taskLabel || '跟读练习';

    listEl.innerHTML = `
      <div class="focus" data-i="${i}">
        <div class="focus-card">
          <span class="ai-tag">${esc(state.taskLabel || '跟读')}</span>
          <div class="focus-text" contenteditable="${isMobileView() ? 'false' : 'true'}" spellcheck="false">${esc(text)}</div>
          <div class="focus-score" id="focusScore">${focusScoreHtml(i)}</div>
        </div>
        <div class="focus-btns">
          <button class="focus-btn side" id="fOrig" title="听原声">${SVG_SPEAKER}</button>
          <button class="focus-btn mic" id="fRec" title="跟读">
            <svg class="ring" viewBox="0 0 100 100">
              <circle class="rbg" cx="50" cy="50" r="45"></circle>
              <circle class="rfg" id="fRing" cx="50" cy="50" r="45"></circle>
            </svg>
            <span class="mic-ico" id="fMic">${SVG_MIC}</span>
          </button>
          <button class="focus-btn side" id="fMine" title="听我的录音">${SVG_PLAY}</button>
        </div>
        <div class="focus-hint" id="focusHint">${idleHint()}</div>
        <div class="focus-swipe-tip">左右滑动切换</div>
      </div>`;

    $('#fOrig').onclick = () => playSegment(i);
    $('#fMine').onclick = () => playMine(i);
    $('#fRec').onclick = () => {
      if (state.recording) {
        stopRec(); // 录音中点一下 = 立即结束并评分
      } else {
        startRec(i);
      }
    };


    const en = listEl.querySelector('.focus-text');
    en.addEventListener('blur', () => {
      const v = en.textContent.trim();
      if (v && v !== seg.text) {
        state.edits[i] = v;
        saveEdits();
      }
    });
    en.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        en.blur();
      }
    });

    updateCount();
    highlight(i);

    if (state.recording && state.recIndex === i) beginRing(0);
    else if (autoplay) later(() => playSegment(i), 150);
  }

  function focusGo(pos, autoplay) {
    const idxs = visibleIndexes();
    if (pos < 0 || pos >= idxs.length) {
      if (pos >= idxs.length) toast('全部练完啦 🎉');
      return;
    }
    stopAudio();
    state.focusPos = pos;
    savePos(state.taskLabel, idxs[pos]); // 手机端卡片翻到哪，也记到哪
    renderFocus(autoplay);
  }

  // 录音圆环：limitMs<=0 时按 10 秒兜底
  function beginRing(limitMs) {
    endRing();
    const mic = document.getElementById('fMic');
    if (mic) {
      // 录音中：中间先出一根竖线，两侧依次长出更短的竖线（声波动效）
      const hs = [0.3, 0.46, 0.64, 0.82, 1, 0.82, 0.64, 0.46, 0.3];
      mic.innerHTML = `<span class="wave">${hs
        .map((h, k) => `<i data-k="${k}" style="--h:${h};animation-delay:${(k % 4) * 0.09}s"></i>`)
        .join('')}</span>`;
      const bars = mic.querySelectorAll('.wave i');
      const order = [4, 3, 5, 2, 6, 1, 7, 0, 8]; // 中间 → 两侧
      if (bars[4]) bars[4].classList.add('on');
      let n = 1;
      state.waveTimer = setInterval(() => {
        if (n >= order.length) {
          clearInterval(state.waveTimer);
          state.waveTimer = null;
          return;
        }
        const b = bars[order[n]];
        if (b) b.classList.add('on');
        n++;
      }, 130);
    }
    const hint = document.getElementById('focusHint');
    if (hint) hint.textContent = '录音中… 点一下话筒立即结束';
    const ring = document.getElementById('fRing');
    if (!ring) return;
    const t0 = state.recStart || Date.now();
    const total = limitMs > 0 ? limitMs : 10000;
    ring.style.strokeDasharray = RING_C;
    state.ringTimer = setInterval(() => {
      const p = Math.min(1, (Date.now() - t0) / total);
      ring.style.strokeDashoffset = RING_C * (1 - p);
    }, 60);
  }

  function endRing() {
    if (state.ringTimer) {
      clearInterval(state.ringTimer);
      state.ringTimer = null;
    }
    if (state.waveTimer) {
      clearInterval(state.waveTimer);
      state.waveTimer = null;
    }
    const mic = document.getElementById('fMic');
    if (mic) mic.innerHTML = SVG_MIC;
    const hint = document.getElementById('focusHint');
    if (hint) hint.textContent = idleHint();
    const ring = document.getElementById('fRing');
    if (ring) ring.style.strokeDashoffset = RING_C;
  }

  // 评分回来：刷新分数，出了分自动到下一个词
  function onFocusScore(i) {
    const idxs = visibleIndexes();
    const pos = idxs.indexOf(i);
    const el = document.getElementById('focusScore');
    if (!el || pos !== state.focusPos) return;
    el.innerHTML = focusScoreHtml(i);
    const s = state.scores[i];
    if (s && s.total > 0) {
      clearTimeout(state.autoNextTimer);
      state.autoNextTimer = setTimeout(() => {
        if (state.focusPos === pos && pos < idxs.length - 1) focusGo(pos + 1, true);
      }, 1600);
    }
  }

  // 按 section 把句子索引分组（用于课文朗读的分部导航）
  function groupBySection(idxs) {
    const out = [];
    let cur = null;
    for (const i of idxs) {
      const sec = state.data.segments[i].section || '';
      if (!cur || cur.section !== sec) {
        cur = { section: sec, indexes: [] };
        out.push(cur);
      }
      cur.indexes.push(i);
    }
    return out;
  }

  // 手工标注的分部元信息（data.json 的 parts：标题 + 朗读播放区间，含前奏/间奏）
  function partMeta(section) {
    return (state.data.parts || []).find((p) => p.section === section);
  }

  function shortSection(sec) {
    const k = sec.lastIndexOf(' · ');
    return k >= 0 ? sec.slice(k + 3) : '';
  }

  // 课文朗读：先选部分（课文朗读 1/2/3…），进入后整篇连播 + 高亮
  function renderReading() {
    document.body.classList.remove('focus-mode');
    const idxs = visibleIndexes();
    if (!idxs.length) return;
    const subs = groupBySection(idxs);

    // 第一层：多个部分时先列目录
    if (!state.readPart && subs.length > 1) {
      const rows = subs
        .map((g, k) => {
          const meta = partMeta(g.section) || {};
          const s = state.data.segments[g.indexes[0]];
          const e = state.data.segments[g.indexes[g.indexes.length - 1]];
          const name = meta.title || shortSection(g.section) || `第${k + 1}部分`;
          const t0 = meta.start ?? s.start;
          const t1 = meta.end ?? e.end;
          return `
        <div class="task-row" data-part="${k}">
          <span class="task-ico ico-text">📖</span>
          <div class="task-info">
            <div class="task-name">课文朗读 ${k + 1} · ${esc(name)}</div>
            <div class="task-sub">${g.indexes.length} 句 · ${fmtTime(t0)}-${fmtTime(t1)}</div>
          </div>
          <button class="btn small primary">进入</button>
        </div>`;
        })
        .join('');
      listEl.innerHTML = `<div class="section-title">课文朗读 · 共 ${subs.length} 个部分</div><div class="card">${rows}</div>`;
      listEl.querySelectorAll('[data-part]').forEach((el) => {
        el.onclick = () => {
          state.readPart = subs[parseInt(el.dataset.part, 10)].section;
          renderReading();
        };
      });
      return;
    }

    // 第二层：单个部分的连读视图（顶部有分部切换）
    const active = state.readPart
      ? idxs.filter((i) => state.data.segments[i].section === state.readPart)
      : idxs;
    if (!active.length) {
      state.readPart = null;
      renderReading();
      return;
    }
    const meta = state.readPart ? partMeta(state.readPart) : null;
    const first = state.data.segments[active[0]];
    const cat = detectCategory(first.section || '');
    const chip = cat ? `<span class="cat cat-${cat[1]}">${cat[0]}</span>` : '';
    const nav =
      subs.length > 1
        ? `<div class="part-chips">` +
          subs
            .map((g, k) => {
              const name =
                (partMeta(g.section) || {}).title ||
                shortSection(g.section) ||
                `第${k + 1}部分`;
              return `<button class="chip${g.section === state.readPart ? ' on' : ''}" data-pchip="${k}">${esc(name)}</button>`;
            })
            .join('') +
          `</div>`
        : '';
    const spans = active
      .map((i) => {
        const text = state.edits[i] ?? state.data.segments[i].text;
        return `<span class="rd" data-i="${i}">${esc(text)}</span>`;
      })
      .join(' ');
    listEl.innerHTML = `
      <div class="section-title">${esc(meta?.title || shortSection(first.section) || first.section || '课文朗读')}${chip}</div>
      ${nav}
      <div class="card reading-bar">
        <button class="btn primary" id="readAll">▶ 播放整篇</button>
        <button class="btn" id="readStop">⏹ 停止</button>
        <span class="muted">播放中当前句会高亮；点任意句子可从那里开始连读。要跟读请回任务列表选「逐句跟读」</span>
      </div>
      <div class="card reading">${spans}</div>`;
    $('#readAll').onclick = () => playWhole();
    $('#readStop').onclick = () => {
      stopAudio();
      highlight(-1);
    };
    listEl.querySelectorAll('.rd').forEach((el) => {
      el.onclick = () => playWhole(parseInt(el.dataset.i, 10));
    });
    listEl.querySelectorAll('[data-pchip]').forEach((el) => {
      el.onclick = () => {
        stopAudio();
        state.readPart = subs[parseInt(el.dataset.pchip, 10)].section;
        renderReading();
      };
    });
    updateCount();
  }

  // 从某句（默认第一句）一直连播到本段最后一句；有手工标注时用标注的区间（含前奏/尾奏）
  function playWhole(fromIdx) {
    let idxs = visibleIndexes();
    if (!idxs.length) return;
    if (state.readPart) idxs = idxs.filter((i) => state.data.segments[i].section === state.readPart);
    if (!idxs.length) return;
    const first = fromIdx != null && idxs.includes(fromIdx) ? fromIdx : idxs[0];
    const meta = state.readPart ? partMeta(state.readPart) : null;
    const last = idxs[idxs.length - 1];
    const endT = meta && meta.end != null ? meta.end : state.data.segments[last].end;
    playRange(state.data.segments[first].start, endT, () => {
      highlight(-1);
    });
    highlight(first);
  }

  // ---------- 工具栏 ----------
  function setMode(mode) {
    state.mode = mode;
    $('#modes')
      .querySelectorAll('button')
      .forEach((b) => b.classList.toggle('on', b.dataset.mode === mode));
    $('#modeHint').textContent =
      mode === 'loop'
        ? '点「跟读」：先听原音，然后自动录音，自动评分'
        : mode === 'ab'
        ? '跟读后自动回放你的录音，方便和原声对比'
        : '整篇连读，一句接一句';
  }

  $('#modes').onclick = (e) => {
    const b = e.target.closest('button[data-mode]');
    if (b) {
      setMode(b.dataset.mode);
      renderSegments();
    }
  };

  $('#rates').onclick = (e) => {
    const b = e.target.closest('button[data-rate]');
    if (!b) return;
    $('#rates').querySelectorAll('button').forEach((x) => x.classList.toggle('on', x === b));
    state.rate = parseFloat(b.dataset.rate);
    audio.playbackRate = state.rate;
  };

  function onKey(e) {
    const tag = (e.target.tagName || '').toLowerCase();
    if (tag === 'input' || e.target.isContentEditable) return;
    // 视频层打开时只处理返回键，别让空格/方向键去操作背后的课文音频
    if (document.body.classList.contains('video-open')) {
      if (e.key === 'Escape') closeVideo(true);
      return;
    }
    if (e.code === 'Space') {
      e.preventDefault();
      if (state.current >= 0) playSegment(state.current, false);
      else if (state.data) playSegment(0, false);
    }
    if (e.key === 'r' || e.key === 'R') {
      if (state.recording) stopRec();
      else if (state.current >= 0) practice(state.current);
    }
    // 练习页：方向键也能切（电脑上调试方便）
    if (document.body.classList.contains('focus-mode')) {
      if (e.key === 'ArrowRight') focusGo(state.focusPos + 1, true);
      if (e.key === 'ArrowLeft') focusGo(state.focusPos - 1, true);
    }
  }
  window.addEventListener('keydown', onKey);

  // ---------- 载入 ----------
  api
    .settings()
    .then((s) => {
      state.iseReady = !!(s && s.appId && s.apiKey && s.apiSecret);
      // 配置是异步读的，读完刷新底部提示（否则首屏会误报“未配置”）
      const h = document.getElementById('focusHint');
      if (h && !state.recording) h.textContent = idleHint();
    })
    .catch(() => {});

  // 返回按钮：在任务里先回任务列表，在任务列表才是回课程库
  $('.back').addEventListener('click', (e) => {
    if (state.filter) {
      e.preventDefault();
      stopAudio();
      renderTasks();
    }
  });

  api
    .course(courseId)
    .then(async (data) => {
      state.data = data;
      $('#title').textContent = data.title || '跟读练习';
      document.title = data.title || 'ReadAlong';
      audio.src = data.audio || `/courses/${courseId}/audio.wav`;
      audio.playbackRate = state.rate;
      setMode('loop');
      await loadClips(); // 先把存过的录音读回来再渲染，免得状态和实际不一致
      if (state.data.segments.some((s) => s.section)) {
        renderTasks(); // 合并课程（多分段）：先看任务列表
      } else {
        renderSegments();
      }
    })
    .catch((e) => {
      listEl.innerHTML = `<div class="empty">课程加载失败：${esc(String(e && e.message ? e.message : e))}</div>`;
    });

  return () => {
    document.body.classList.remove('focus-mode');
    window.removeEventListener('keydown', onKey);
    window.removeEventListener('popstate', onVideoPop);
    closeVideo(false);
    if (videoEl) {
      videoEl.remove();
      videoEl = null;
    }
    clearTimeout(state.autoNextTimer);
    for (const id of state.timers) clearTimeout(id);
    state.timers.clear();
    if (state.vidTimer) clearInterval(state.vidTimer);
    if (state.ringTimer) clearInterval(state.ringTimer);
    if (state.waveTimer) clearInterval(state.waveTimer);
    stopRec();
    stopAudio();
    audio.src = '';
  };
}
