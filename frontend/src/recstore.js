// 跟读录音的本地仓库：桌面版和 Reader 都把「我的录音」存进浏览器 IndexedDB，
// 刷新、切课程、关掉重开之后还能回放自己对的原声。
// clips: 一条记录 = { key: '<courseId>#<句号>', courseId, idx, blob, at }
// 每句只留最新一条（重录覆盖旧的），所以占用上限 = 句数 × 单条录音，不会无限涨。

const DB_NAME = 'readalong-clips';
const CLIPS = 'clips';
const DB_VERSION = 1;
const COURSE_IDX = 'courseId';

function open() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(CLIPS)) {
        const store = db.createObjectStore(CLIPS, { keyPath: 'key' });
        // 按课程取/清：删除课程、覆盖生成时都要一次抹掉整门课的录音
        store.createIndex(COURSE_IDX, 'courseId', { unique: false });
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

// fn 拿到的第二个参数是事务本身，批量删除时要在事务里跑完
function run(mode, fn) {
  return open().then(
    (db) =>
      new Promise((resolve, reject) => {
        const t = db.transaction(CLIPS, mode);
        const req = fn(t.objectStore(CLIPS), t);
        t.oncomplete = () => {
          db.close();
          resolve(req ? req.result : undefined);
        };
        t.onerror = () => {
          db.close();
          reject(t.error);
        };
        // 事务被中止（如 QuotaExceeded）时只监听 onerror 会永远挂起
        t.onabort = () => {
          db.close();
          reject(t.error || new Error('IndexedDB 事务被中止'));
        };
      })
  );
}

function clipKey(courseId, idx) {
  return `${courseId}#${idx}`;
}

export const recstore = {
  // put 存一句录音（同 key 覆盖）。
  put: (courseId, idx, blob) =>
    run('readwrite', (s) =>
      s.put({ key: clipKey(courseId, idx), courseId, idx: Number(idx), blob, at: Date.now() })
    ),

  // get 取一句录音，没有就返回 undefined。
  get: (courseId, idx) =>
    run('readonly', (s) => s.get(clipKey(courseId, idx))).then((r) => (r ? r.blob : undefined)),

  // all 取整门课的录音，返回 Map<句号, Blob>。
  all: (courseId) =>
    run('readonly', (s) => s.index(COURSE_IDX).getAll(courseId)).then((rows) => {
      const map = new Map();
      for (const r of rows || []) map.set(Number(r.idx), r.blob);
      return map;
    }),

  // clear 清掉整门课的录音。
  clear: (courseId) =>
    run('readwrite', (s, t) => {
      const req = s.index(COURSE_IDX).openCursor(IDBKeyRange.only(courseId));
      req.onsuccess = () => {
        const cur = req.result;
        if (!cur) return;
        cur.delete();
        cur.continue();
      };
      return t; // 让调用方等事务提交，而不是等某一条 delete
    }),
};

// clearPractice 抹掉一门课的全部本地练习痕迹：进度、手改文本、评分（localStorage）
// 和录音（IndexedDB）。只在课程被「重新生成覆盖」时调用 —— 时间轴变了，
// 这些数据按句号索引，留着会指向别的句子。
export function clearPractice(courseId) {
  for (const k of ['ra.done', 'ra.edits', 'ra.score']) {
    try {
      localStorage.removeItem(`${k}.${courseId}`);
    } catch (e) {
      /* 隐私模式写不了，忽略 */
    }
  }
  return recstore.clear(courseId).catch(() => {});
}
