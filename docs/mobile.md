# 手机端（Android APK）

手机端**只做播放器**：课程由电脑版 Studio 生成 → 「导出整本书」得到 zip → 手机导入后离线跟读、联网评分。
不涉及 yt-dlp / whisper / 视频解析，所以可以在手机上跑。

## 结构

```
frontend/                复用同一套代码
  reader.html            Reader 入口（纯前端，无 Wails 依赖）
  vite.config.mobile.js  手机端构建：reader.html → dist-mobile/index.html
mobile/                  Capacitor 壳
  capacitor.config.json  webDir 指向 ../frontend/dist-mobile
  android/               原生工程（Gradle）
.github/workflows/android.yml   push 即构建 APK
```

- 播放 / 跟读 / 录音：`player.js` 原样复用
- 评分：`ise.js`（JS 直连讯飞 WebSocket，凭据存手机 localStorage）
- 凭据来源：**导出课程包时自动附带**（zip 内 `app-settings.json`，由 PC 的 `settings.json` 写入），手机导入课程包即自动配置，无需手填；仓库与 APK 内都不含密钥。也可在 App「评测设置」里手动填写/覆盖
- 注意：课程包里带了密钥，别把课程包外传
- 课程存储：IndexedDB（导入的 zip 解包后存 Blob）
- 录音：WebView 的 `getUserMedia` + `MediaRecorder`，需要 `RECORD_AUDIO` 权限（已在 AndroidManifest 声明）

## 本地构建（需要 Android SDK + JDK 21）

```bash
cd frontend && npm ci && npm run build:mobile
cd ../mobile && npm ci && npx cap sync android
cd android && ./gradlew assembleDebug
# 产物：mobile/android/app/build/outputs/apk/debug/app-debug.apk
```

WSL 里没装 Android SDK 时不用折腾，直接交给 CI。

## CI 出包

| 触发 | 结果 |
| --- | --- |
| push 到 `main`（改了 `frontend/**` 或 `mobile/**`） | Actions 里 `Android APK` 产物 `readalong-apk`，30 天可下 |
| push `v*` tag | 额外把 APK 挂到对应 Release，手机浏览器直接点链接安装 |
| Actions 页面手动 Run workflow | 同第一条 |

versionCode 取 `github.run_number`（自增，可直接覆盖安装），versionName 为 `0.1.<run_number>`。

## 手机上装

1. Actions → 最近一次 `Android APK` → Artifacts → 下载 `readalong-apk`
2. 解压得到 `ReadAlong-<sha>.apk`，传到手机（微信文件传输助手/USB）
3. 安装时允许「安装未知来源应用」
4. 首次录音会弹麦克风权限，必须允许，否则跟读录音不可用

## 已知风险（首次真机验证重点）

1. **WebView 录音**：Capacitor Android WebView 支持 `MediaRecorder`，但个别国产 ROM 会拦截。
   如果录音无声 → 换 `@capacitor-community/voice-recorder` 等原生录音插件（前端 `startRec/stopRec` 两个函数替换即可）。
2. **讯飞评分跨域**：WebSocket 无 CORS 限制，但讯飞可能校验来源。如果报鉴权错，
   在 `ise.js` 里调整握手参数；凭据必须与电脑版同一个 APPID 且已开通「语音评测（流式版）」。
3. **大课程包**：整本书 zip 可能上百 MB（含视频），导入时手机内存/存储要留够；
   如果太慢，可考虑导出时不带视频。
4. **后台播放**：目前 WebView 切后台后音频可能暂停，属预期；如需锁屏播放要加原生音频服务（后续）。
