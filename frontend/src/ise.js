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

// 试题文本：一律加 UTF-8 BOM 头；英文还必须带节点标记（单词 [word] / 句子·篇章 [content]），
// 中文按纯文本。标记错了讯飞报 48195「试题格式错误」，表面现象却是
// seeRec.SRecWrite error: iSEInputAppend error, ret=8195。
function iseRefText(text, ent, category) {
  const marker = ent === 'en_vip' ? (category === 'read_word' ? '[word]' : '[content]') : '';
  return '\uFEFF' + marker + text;
}

function pickScore(xml, attr) {
  const m = xml.match(new RegExp(`${attr}="([0-9.]+)"`));
  return m ? parseFloat(m[1]) : 0;
}

// 讯飞返回的各项分数是 0–5 分制（满分 5.000000），前端按百分制分档（85/70），
// 这里统一折算成百分制，避免出现「4 分」这种展示。
function normScore(v) {
  return v > 0 && v <= 5 ? v * 20 : v;
}

// assessPCM 用现成的 16k 单声道 PCM 送评（和 Go 端 domain/ise.go 完全一致），
// 单独抽出来是为了能在 Node 里直接跑真机同款链路做验证。
export async function assessPCM(pcm, text, lang, settings) {
  if (!settings || !settings.appId || !settings.apiKey || !settings.apiSecret) {
    throw new Error('先在设置里填讯飞评测凭据');
  }
  if (!pcm || !pcm.byteLength) throw new Error('音频为空');

  const { ent, category } = iseEntCategory(text, lang);

  const date = new Date().toUTCString(); // 已是 "GMT" 结尾，符合讯飞要求
  // 官方文档：签名原文第三行固定为 GET（不是 POST），写错会鉴权失败
  const origin = `host: ${ISE_HOST}\ndate: ${date}\nGET ${ISE_PATH} HTTP/1.1`;
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
  let serverErr = null;
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.code !== 0 && !serverErr) {
        serverErr = new Error(`讯飞评测出错（${msg.code}）：${msg.message}`);
      }
    } catch (e) {
      /* 忽略非 JSON */
    }
  };
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('连接讯飞评测超时')), 15000);
    ws.addEventListener('open', () => {
      clearTimeout(timer);
      resolve();
    });
    ws.addEventListener('error', () => {
      clearTimeout(timer);
      reject(new Error('连接讯飞评测失败'));
    });
  });

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const send = (obj) => {
    if (serverErr) throw serverErr;
    ws.send(JSON.stringify(obj));
  };

  // 第 1 帧 ssb：参考文本放 business.text（BOM + 按题型节点标记，见 iseRefText）。
  // 键顺序严格复刻官方 demo（讯飞校验器对顺序敏感，顺序错了会报 10163）：
  // aue, auf, category, cmd, ent, sub, text, tte, ttp_skip, rstcd, group
  send({
    common: { app_id: settings.appId },
    business: {
      aue: 'raw',
      auf: 'audio/L16;rate=16000',
      category,
      cmd: 'ssb',
      ent,
      sub: 'ise',
      text: iseRefText(text, ent, category),
      tte: 'utf-8',
      ttp_skip: true,
      rstcd: 'utf8',
      group: 'pupil',
    },
    data: { status: 0 },
  });
  await sleep(60);

  // 第 2..n 帧 auw：16k/16bit/单声道 = 32KB/s，必须按实时速率
  // 每帧 1280 字节（40ms 音频）+ 40ms 间隔，灌太快会报 ret=8195。
  // 注意先转成字节视图：Int16Array.subarray 的下标是元素而非字节，直接按字节偏移切会切错。
  const bytes = new Uint8Array(pcm.buffer, pcm.byteOffset, pcm.byteLength);
  const frameSize = 1280;
  for (let off = 0, first = true; off < bytes.byteLength; off += frameSize) {
    const end = Math.min(off + frameSize, bytes.byteLength);
    send({
      business: { cmd: 'auw', aus: first ? 1 : 2, aue: 'raw' },
      // 注意：音频 base64 的键名是 data（不是 audio），与 Go 端 iseReqData.Audio 的 tag 一致
      data: { status: 1, data: b64(bytes.subarray(off, end)) },
    });
    first = false;
    await sleep(40);
  }
  // 最后一帧：结束
  send({ business: { cmd: 'auw', aus: 4, aue: 'raw' }, data: { status: 2 } });

  let xml;
  try {
    xml = await new Promise((resolve, reject) => {
      let result = '';
      const timer = setTimeout(() => reject(new Error('评测超时')), 60000);
      const onMsg = (ev) => {
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
        if (msg.data && msg.data.data) result = atob(msg.data.data);
        if (msg.data && msg.data.status === 2) {
          clearTimeout(timer);
          resolve(result);
        }
      };
      ws.addEventListener('message', onMsg);
      ws.addEventListener('error', () => reject(new Error('连接中断')));
    });
  } finally {
    // 成功、超时、服务端报错都要断开：原来只在成功路径 close，
    // 出错时连接会一直挂着，长时间反复录音会堆积
    try {
      ws.close();
    } catch (e) {
      /* 可能已经关了 */
    }
  }

  if (!/total_score="[0-9.]+"/.test(xml)) {
    throw new Error('录音太短或没有识别到朗读内容');
  }
  const total = pickScore(xml, 'total_score');
  // 单词题（read_word）的 XML 只有 total/accuracy，缺的项用总分兜底，
  // 否则界面会显示「流利 0」。
  const accuracy = pickScore(xml, 'accuracy_score') || total;
  const fluency = pickScore(xml, 'fluency_score') || total;
  return {
    total: normScore(total),
    accuracy: normScore(accuracy),
    fluency: normScore(fluency),
    integrity: normScore(pickScore(xml, 'integrity_score')),
    words: parseWords(xml),
  };
}

// 句子里每个词的分数：read_sentence 的 XML 会带
//   <word content="hello" total_score="4.2" dp_message="0" ...>
// dp_message 非 "0" 表示漏读 / 错读 / 多读；单词题（read_word）没有这层，返回空数组。
// 前端用它在句子上把读错的词标出来，比只给一个总分有用得多。
function parseWords(xml) {
  const out = [];
  if (!/<word[\s>]/.test(xml)) return out;
  let doc = null;
  try {
    doc = new DOMParser().parseFromString(xml, 'text/xml');
  } catch (e) {
    return out;
  }
  if (!doc || doc.getElementsByTagName('parsererror').length) return out;
  const nodes = doc.getElementsByTagName('word');
  for (const w of nodes) {
    const text = w.getAttribute('content') || '';
    if (!text.trim()) continue;
    out.push({
      text,
      score: normScore(parseFloat(w.getAttribute('total_score') || '0')),
      dp: w.getAttribute('dp_message') || '0',
    });
  }
  return out;
}

// assessWeb 评分：text 参考文本，lang 'en'|'zh'，audioBytes 录音原始字节（webm 等）
export async function assessWeb(text, lang, audioBytes, settings) {
  if (!settings || !settings.appId || !settings.apiKey || !settings.apiSecret) {
    throw new Error('先在设置里填讯飞评测凭据');
  }
  const pcm = await toPCM16k(audioBytes);
  return assessPCM(pcm, text, lang, settings);
}
