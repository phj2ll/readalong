// 讯飞语音评测的纯浏览器实现：WebSocket + WebCrypto 签名 + AudioContext 解码转 PCM。
// Reader（无后端）模式下由 api.js 调用；凭据存在 localStorage。

const ISE_HOST = 'ise-api.xfyun.cn';
const ISE_PATH = '/v2/open-ise';

const enc = new TextEncoder();

function b64(buf) {
  const bytes = new Uint8Array(buf);
  let s = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    s += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(s);
}

// webm/opus 等录音解码成 16k 单声道 PCM (Int16)。入参 ArrayBuffer 或 Uint8Array。
async function toPCM16k(audioBytes) {
  const ab =
    audioBytes instanceof ArrayBuffer
      ? audioBytes
      : audioBytes.buffer.slice(
          audioBytes.byteOffset,
          audioBytes.byteOffset + audioBytes.byteLength
        );
  const raw = new AudioContext();
  let decoded;
  try {
    decoded = await raw.decodeAudioData(ab);
  } finally {
    raw.close();
  }
  const frames = Math.max(1, Math.ceil(decoded.duration * 16000));
  const off = new OfflineAudioContext(1, frames, 16000);
  const src = off.createBufferSource();
  src.buffer = decoded;
  src.connect(off.destination);
  src.start();
  const rendered = await off.startRendering();
  const f = rendered.getChannelData(0);
  const pcm = new Int16Array(f.length);
  for (let i = 0; i < f.length; i++) {
    const v = Math.max(-1, Math.min(1, f[i]));
    pcm[i] = v < 0 ? v * 0x8000 : v * 0x7fff;
  }
  return pcm;
}

function iseEntCategory(text, lang) {
  const ent = String(lang || '').trim() === 'zh' ? 'cn_vip' : 'en_vip';
  const words = (text.trim().match(/\S+/g) || []).length;
  const category = words <= 1 && [...text].length < 20 ? 'read_word' : 'read_sentence';
  return { ent, category };
}

function pickScore(xml, attr) {
  const m = xml.match(new RegExp(`${attr}="([0-9.]+)"`));
  return m ? parseFloat(m[1]) : 0;
}

// assessWeb 评分：text 参考文本，lang 'en'|'zh'，audioBytes 录音原始字节（webm 等）
export async function assessWeb(text, lang, audioBytes, settings) {
  if (!settings || !settings.appId || !settings.apiKey || !settings.apiSecret) {
    throw new Error('先在设置里填讯飞评测凭据');
  }
  const pcm = await toPCM16k(audioBytes);
  const { ent, category } = iseEntCategory(text, lang);

  const date = new Date().toUTCString();
  const origin = `host: ${ISE_HOST}\ndate: ${date}\nPOST ${ISE_PATH} HTTP/1.1`;
  const key = await crypto.subtle.importKey(
    'raw',
    enc.encode(settings.apiSecret),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign']
  );
  const sig = b64(await crypto.subtle.sign('HMAC', key, enc.encode(origin)));
  const auth = b64(
    enc.encode(
      `api_key="${settings.apiKey}", algorithm="hmac-sha256", headers="host date request-line", signature="${sig}"`
    )
  );
  const url = `wss://${ISE_HOST}${ISE_PATH}?authorization=${encodeURIComponent(auth)}&date=${encodeURIComponent(date)}&host=${ISE_HOST}`;

  const ws = new WebSocket(url);
  await new Promise((resolve, reject) => {
    ws.onopen = resolve;
    ws.onerror = () => reject(new Error('连接讯飞评测失败'));
    setTimeout(() => reject(new Error('连接讯飞评测超时')), 15000);
  });

  const send = (obj) =>
    new Promise((resolve, reject) => {
      ws.onmessage = () => resolve(); // 忽略中间帧
      ws.send(JSON.stringify(obj));
      setTimeout(resolve, 60);
    });

  await send({
    common: { app_id: settings.appId },
    business: {
      sub: 'ise', ent, category, cmd: 'ssb',
      tte: 'utf-8', ttp_skip: true, aue: 'raw', auf: 'audio/L16;rate=16000',
      rstcmd: 'all', group: 'pupil',
    },
    data: { status: 0, cmd: 'ssb', text: b64(enc.encode(text + '\r\n')) },
  });

  const frameSize = 1280 * 8;
  for (let off = 0; off < pcm.byteLength; off += frameSize) {
    const chunk = pcm.subarray(off, Math.min(off + frameSize, pcm.byteLength));
    await send({
      business: { cmd: 'auw', aus: off === 0 ? 1 : 2, tte: 'utf-8' },
      data: { status: 1, cmd: 'auw', audio: b64(chunk.buffer.slice(chunk.byteOffset, chunk.byteOffset + chunk.byteLength)) },
    });
  }
  await send({ business: { cmd: 'auw', aus: 4, tte: 'utf-8' }, data: { status: 2, cmd: 'auw' } });

  const xml = await new Promise((resolve, reject) => {
    let result = '';
    const timer = setTimeout(() => reject(new Error('评测超时')), 60000);
    ws.onmessage = (ev) => {
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch (e) {
        return;
      }
      if (msg.code !== 0) {
        clearTimeout(timer);
        reject(new Error(`讯飞评测出错（${msg.code}）：${msg.message}`));
        return;
      }
      if (msg.data && msg.data.data) {
        result = atob(msg.data.data);
      }
      if (msg.data && msg.data.status === 2) {
        clearTimeout(timer);
        resolve(result);
      }
    };
    ws.onerror = () => reject(new Error('连接中断'));
  });
  ws.close();

  if (!/total_score="[0-9.]+"/.test(xml)) {
    throw new Error('录音太短或没有识别到朗读内容');
  }
  return {
    total: pickScore(xml, 'total_score'),
    accuracy: pickScore(xml, 'accuracy_score'),
    fluency: pickScore(xml, 'fluency_score'),
    integrity: pickScore(xml, 'integrity_score'),
  };
}
