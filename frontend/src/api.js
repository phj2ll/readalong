/* Wails 把 App 的公开方法注入到 window.go.main.App */
function bridge() {
  const b = window.go && window.go.main && window.go.main.App;
  if (!b) throw new Error('Wails 运行时未就绪');
  return b;
}

export const api = {
  version: () => bridge().Version(),
  status: () => bridge().Status(),
  model: () => bridge().ModelState(),
  downloadModel: () => bridge().DownloadModel(),
  list: () => bridge().ListCourses(),
  course: (id) => bridge().CourseData(id),
  create: (url, lang) => bridge().CreateCourse(url, lang),
  job: (id) => bridge().Job(id),
  del: (id) => bridge().DeleteCourse(id),
  openDataDir: () => bridge().OpenDataDir(),
  openURL: (u) => bridge().OpenURL(u),
};

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
