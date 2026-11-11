// Reader 模式的课程仓库：课程包（zip 解开后）存 IndexedDB，纯前端、无后端。
// 一条记录 = { id, title, source, uploader, duration, count, created, data, audioBlob }

const DB_NAME = 'readalong-reader';
const STORE = 'courses';

function open() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      if (!req.result.objectStoreNames.contains(STORE)) {
        req.result.createObjectStore(STORE, { keyPath: 'id' });
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function run(mode, fn) {
  return open().then(
    (db) =>
      new Promise((resolve, reject) => {
        const t = db.transaction(STORE, mode);
        const req = fn(t.objectStore(STORE));
        t.oncomplete = () => {
          db.close();
          resolve(req ? req.result : undefined);
        };
        t.onerror = () => {
          db.close();
          reject(t.error);
        };
      })
  );
}

export const idb = {
  put: (course) => run('readwrite', (s) => s.put(course)),
  get: (id) => run('readonly', (s) => s.get(id)),
  del: (id) => run('readwrite', (s) => s.delete(id)),
  all: () => run('readonly', (s) => s.getAll()),
};
