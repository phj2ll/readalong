import './style.css';
import { renderLibrary } from './views/library.js';
import { renderPlayer } from './views/player.js';

const root = document.getElementById('app');
let cleanup = null;

function route() {
  if (typeof cleanup === 'function') {
    try {
      cleanup();
    } catch (e) {
      /* 忽略清理异常 */
    }
    cleanup = null;
  }

  const hash = location.hash.replace(/^#/, '');
  const m = hash.match(/^\/play\/([^/]+)$/);
  if (m) {
    cleanup = renderPlayer(root, decodeURIComponent(m[1]));
  } else {
    cleanup = renderLibrary(root);
  }
}

window.addEventListener('hashchange', route);
window.addEventListener('beforeunload', () => {
  if (typeof cleanup === 'function') cleanup();
});

route();
