import { api, esc, fmtTime, toast } from '../api.js';

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
    recorder: null,
    chunks: [],
    stream: null,
    recIndex: -1,
    timer: null,
  };

  const keyDone = `ra.done.${courseId}`;
  const keyEdits = `ra.edits.${courseId}`;
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
        <span class="chip" id="modeHint">点「原声」听一遍，再点「录音」跟读</span>
      </div>
    </div>
    <div class="wrap" id="list"></div>
    <audio id="audio" preload="auto"></audio>
  `;

  const $ = (s) => root.querySelector(s);
  const audio = $('#audio');
  const listEl = $('#list');

  function saveDone() {
    localStorage.setItem(keyDone, JSON.stringify([...state.done]));
  }
  function saveEdits() {
    localStorage.setItem(keyEdits, JSON.stringify(state.edits));
  }

  function updateCount() {
    const total = state.data ? state.data.segments.length : 0;
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
    listEl.querySelectorAll('.seg').forEach((el, k) => {
      el.classList.toggle('active', k === i);
    });
    const el = listEl.querySelectorAll('.seg')[i];
    if (el) el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }

  function playSegment(i, thenMine) {
    if (!state.data) return;
    const seg = state.data.segments[i];
    if (!seg) return;
    highlight(i);
    playRange(seg.start, seg.end, () => {
      if (thenMine && state.mine.has(i)) {
        setTimeout(() => playMine(i), 350);
      } else if (state.mode === 'auto' && i + 1 < state.data.segments.length) {
        setTimeout(() => playSegment(i + 1, true), 420);
      }
    });
  }

  function playMine(i) {
    const blob = state.mine.get(i);
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
  async function startRec(i) {
    if (state.recording) {
      stopRec();
      return;
    }
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      toast('当前环境不支持录音', true);
      return;
    }
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      });
    } catch (e) {
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
      state.done.add(idx);
      saveDone();
      updateCount();
      markDone(idx);
      if (state.mode === 'ab' || state.mode === 'loop') {
        setTimeout(() => playSegment(idx, true), 260);
      }
    };

    state.recorder.start();
    state.recording = true;
    const seg = state.data.segments[i];
    const limit = Math.max(3000, (seg.end - seg.start) * 1000 * 1.5 + 1500);
    state.timer = setTimeout(() => stopRec(), limit);
    refreshRecButtons();
    toast('录音中，读完点「停止」');
  }

  function stopRec() {
    if (!state.recording) return;
    state.recording = false;
    clearTimeout(state.timer);
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
    listEl.querySelectorAll('.seg').forEach((el, k) => {
      const b = el.querySelector('[data-rec]');
      if (!b) return;
      const on = state.recording && k === state.recIndex;
      b.textContent = on ? '停止' : '录音';
      b.classList.toggle('danger', on);
      el.classList.toggle('done', state.done.has(k));
    });
  }

  function markDone(i) {
    const el = listEl.querySelectorAll('.seg')[i];
    if (el) el.classList.add('done');
  }

  // ---------- 渲染句子 ----------
  function renderSegments() {
    const segs = state.data.segments;
    let lastSection = '';
    const html = [];
    segs.forEach((seg, i) => {
      if (seg.section && seg.section !== lastSection) {
        lastSection = seg.section;
        html.push(`<div class="section-title">${esc(seg.section)}</div>`);
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
            <button class="btn small" data-play>原声</button>
            <button class="btn small" data-rec>录音</button>
            <button class="btn small" data-mine>我的录音</button>
          </div>
        </article>`);
    });

    listEl.innerHTML = `
      <div class="section-title">${segs.length} 句 · 起点 ${fmtTime(state.data.segments[0]?.start || 0)}</div>
      ${html.join('')}`;

    listEl.querySelectorAll('.seg').forEach((el, i) => {
      el.querySelector('[data-play]').onclick = () => playSegment(i, false);
      el.querySelector('[data-rec]').onclick = () => startRec(i);
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

  // ---------- 工具栏 ----------
  function setMode(mode) {
    state.mode = mode;
    $('#modes')
      .querySelectorAll('button')
      .forEach((b) => b.classList.toggle('on', b.dataset.mode === mode));
    $('#modeHint').textContent =
      mode === 'loop'
        ? '点「原声」听一遍，再点「录音」跟读'
        : mode === 'ab'
        ? '录完自动先播原声、再播你的录音'
        : '一句接一句，中间插播你的录音';
  }

  $('#modes').onclick = (e) => {
    const b = e.target.closest('button[data-mode]');
    if (b) setMode(b.dataset.mode);
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
    if (e.code === 'Space') {
      e.preventDefault();
      if (state.current >= 0) playSegment(state.current, false);
      else if (state.data) playSegment(0, false);
    }
    if (e.key === 'r' || e.key === 'R') {
      if (state.recording) stopRec();
      else if (state.current >= 0) startRec(state.current);
    }
  }
  window.addEventListener('keydown', onKey);

  // ---------- 载入 ----------
  api
    .course(courseId)
    .then((data) => {
      state.data = data;
      $('#title').textContent = data.title || '跟读练习';
      document.title = data.title || 'ReadAlong';
      audio.src = data.audio || `/courses/${courseId}/audio.wav`;
      audio.playbackRate = state.rate;
      setMode('loop');
      renderSegments();
    })
    .catch((e) => {
      listEl.innerHTML = `<div class="empty">课程加载失败：${esc(String(e && e.message ? e.message : e))}</div>`;
    });

  return () => {
    window.removeEventListener('keydown', onKey);
    stopRec();
    stopAudio();
    audio.src = '';
  };
}
