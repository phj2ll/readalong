import { idb } from './idb.js';
import { assessWeb } from './ise.js';
import { recstore } from './recstore.js';

// 双模式：桌面（Wails，window.go 注入）走 Go 桥接；
// Reader（纯浏览器，无后端）走 IndexedDB + JS 评分客户端。
export const isWails = !!(window.go && window.go.main && window.go.main.App);

function bridge() {
  const b = isWails && window.go.main.App;
  if (!b) throw new Error('Wails 运行时未就绪');
  return b;
}

const SETTINGS_KEY = 'ra.reader.settings';

// Reader 模式下课程音频的 Blob URL 缓存：课程 id → object URL，避免重复创建泄漏
const audioUrls = new Map();

// u8ToB64 把录音字节编码成 base64（Wails 传参走 JSON，只能传字符串）。
function u8ToB64(u8) {
  let s = '';
  const chunk = 0x8000;
  for (let i = 0; i < u8.length; i += chunk) {
    s += String.fromCharCode(...u8.subarray(i, i + chunk));
  }
  return btoa(s);
}

function loadLocalSettings() {
  try {
    return JSON.parse(localStorage.getItem(SETTINGS_KEY) || '{}');
  } catch (e) {
    return {};
  }
}

function notInReader(name) {
  return Promise.reject(new Error(`「${name}」只在桌面版可用，请使用 ReadAlong Studio`));
}

const webApi = {
  version: () => Promise.resolve('reader'),
  status: () => notInReader('环境检查'),
  model: () => Promise.resolve({ ready: true, name: 'Reader 模式' }),
  downloadModel: () => notInReader('模型下载'),
  list: () =>
    idb.all().then((items) =>
      (items || [])
        .map((c) => ({
          id: c.id,
          title: c.title,
          source: c.source,
          uploader: c.uploader,
          duration: c.duration,
          count: c.count,
          created: c.created,
        }))
        .sort((a, b) => String(b.created).localeCompare(String(a.created)))
    ),
  course: (id) =>
    idb.get(id).then((c) => {
      if (!c) throw new Error('课程不存在，可能已被删除');
      const data = c.data || {};
      // 播放器通过 data.audio 拿音频地址，Reader 模式给 Blob URL。
      // 缓存起来：每进一次课程都 createObjectURL 会让整段 WAV 一直留在内存里
      let url = audioUrls.get(id);
      if (!url) {
        url = URL.createObjectURL(c.audioBlob);
        audioUrls.set(id, url);
      }
      return { ...data, audio: url };
    }),
  create: () => notInReader('课程生成'),
  job: () => Promise.resolve({ status: 'done' }),
  del: (id) => {
    const url = audioUrls.get(id);
    if (url) {
      URL.revokeObjectURL(url);
      audioUrls.delete(id);
    }
    // 录音另存一个库，删课程时一起清掉，别留孤儿数据
    return recstore
      .clear(id)
      .catch(() => {})
      .then(() => idb.del(id));
  },
  openDataDir: () => notInReader('数据目录'),
  openURL: (u) => {
    window.open(u, '_blank');
    return Promise.resolve();
  },
  settings: () => Promise.resolve(loadLocalSettings()),
  saveSettings: (s) => {
    if (!s.appId || !s.apiKey || !s.apiSecret) {
      return Promise.reject(new Error('APPID、APIKey、APISecret 都要填'));
    }
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(s));
    return Promise.resolve();
  },
  // audio 是 Uint8Array（webm 等原始录音），JS 评分客户端直接解码
  assess: (text, lang, audio) => assessWeb(text, lang, audio, loadLocalSettings()),
  exportBook: () => notInReader('导出课程包'),
  downloadVideos: (id) => notInReader('视频课下载'),
  // 书名/封面由课程包带着走，手机端只读
  books: () => Promise.resolve([]),
  renameBook: () => notInReader('修改书名'),
  pickCover: () => notInReader('上传封面'),
  exportBookInfo: () => notInReader('导出书信息'),
  // 视频课文件播放地址：从 IndexedDB 记录里取 Blob
  videoSrc: async (courseId, file) => {
    const c = await idb.get(courseId);
    const blob = c && c.videoBlobs && c.videoBlobs[file];
    if (!blob) throw new Error('视频文件不存在');
    return URL.createObjectURL(blob);
  },
};

const wailsApi = {
  version: () => bridge().Version(),
  status: () => bridge().Status(),
  model: () => bridge().ModelState(),
  downloadModel: () => bridge().DownloadModel(),
  list: () => bridge().ListCourses(),
  course: (id) => bridge().CourseData(id),
  create: (url, lang) => bridge().CreateCourse(url, lang),
  job: (id) => bridge().Job(id),
  del: async (id) => {
    // 录音不在课程目录里，删课前先把 IndexedDB 里这门课的录音清掉
    await recstore.clear(id).catch(() => {});
    return bridge().DeleteCourse(id);
  },
  openDataDir: () => bridge().OpenDataDir(),
  openURL: (u) => bridge().OpenURL(u),
  settings: () => bridge().GetSettings(),
  saveSettings: (s) => bridge().SaveSettings(s),
  assess: (text, lang, audio) => bridge().AssessRecording(text, lang, u8ToB64(audio)),
  exportBook: (id) => bridge().ExportBook(id),
  downloadVideos: (id) => bridge().DownloadVideos(id),
  // 书籍：改书名 / 换封面（封面图存数据目录，走 /books/<文件名> 访问）
  books: () => bridge().ListBooks(),
  renameBook: (id, title) => bridge().RenameBook(id, title),
  pickCover: (id) => bridge().PickBookCover(id),
  // 只含书名/封面的小包，改完信息后快速同步到手机
  exportBookInfo: (id) => bridge().ExportBookInfo(id),
  // 视频课文件播放地址：桌面版直接走内置资源路由
  videoSrc: (courseId, file) => {
    const clean = String(file).replace(/[^a-zA-Z0-9._-]/g, '');
    return Promise.resolve(`/courses/${encodeURIComponent(courseId)}/${clean}`);
  },
};

export const api = isWails ? wailsApi : webApi;

export function esc(s) {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

export function fmtTime(sec) {
  const s = Math.round(sec || 0);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

let toastTimer = null;
export function toast(msg, isError = false) {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.className = isError ? 'show error' : 'show';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.className = '';
  }, 2600);
}
