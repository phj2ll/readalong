// Reader 模式的本地仓库：课程包（zip 解开后）存 IndexedDB，纯前端、无后端。
// courses: 一条记录 = { id, title, source, uploader, duration, count, created, data, audioBlob, videoBlobs }
// books:   一条记录 = { id, title, coverBlob }（书的书名与封面，来自课程包）

const DB_NAME = 'readalong-reader';
const COURSES = 'courses';
const BOOKS = 'books';
const DB_VERSION = 2;

function open() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(COURSES)) {
        db.createObjectStore(COURSES, { keyPath: 'id' });
      }
      // v2 新增：书名/封面单独存，不用把图片在每门课里重复一份
      if (!db.objectStoreNames.contains(BOOKS)) {
        db.createObjectStore(BOOKS, { keyPath: 'id' });
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function run(store, mode, fn) {
  return open().then(
    (db) =>
      new Promise((resolve, reject) => {
        const t = db.transaction(store, mode);
        const req = fn(t.objectStore(store));
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
  put: (course) => run(COURSES, 'readwrite', (s) => s.put(course)),
  get: (id) => run(COURSES, 'readonly', (s) => s.get(id)),
  del: (id) => run(COURSES, 'readwrite', (s) => s.delete(id)),
  all: () => run(COURSES, 'readonly', (s) => s.getAll()),

  putBook: (book) => run(BOOKS, 'readwrite', (s) => s.put(book)),
  getBook: (id) => run(BOOKS, 'readonly', (s) => s.get(id)),
  delBook: (id) => run(BOOKS, 'readwrite', (s) => s.delete(id)),
  allBooks: () => run(BOOKS, 'readonly', (s) => s.getAll()),
};
