# ReadAlong 开发交接文档

> 面向后续开发者（或 AI 助手）的完整上下文。读完这篇不需要再看历史对话。
>
> 最后更新：2026-11-06　版本：v2.0.0　仓库：`git@github.com:phj2ll/readalong.git`

---

## 0. 先搞清楚项目在哪

项目**唯一在维护的副本就在 WSL 里**：

| 位置 | 路径 |
| --- | --- |
| WSL（主目录，开发在这里） | `/home/wwwroot/readalong` |
| Windows 侧访问同一份文件 | `\\wsl$\Ubuntu-22.04-Recovered\home\wwwroot\readalong` |
| GitHub | `git@github.com:phj2ll/readalong.git`（main = `40eb080`，tag `v2.0.0`） |

```bash
cd /home/wwwroot/readalong
git remote -v      # origin git@github.com:phj2ll/readalong.git
git log --oneline -3
```

> 如果以 root 身份操作时报 `detected dubious ownership`，执行一次：
> `git config --global --add safe.directory /home/wwwroot/readalong`

历史遗留目录（**已废弃，不要再改**）：

- `/home/wwwroot/readalong-legacy-python/` — 最早的 Python + PyInstaller 版（本地仓库，无 remote，2 个提交）
- Windows 工作区的 `readalong-go/` — 第一版 Go 重写（手搓 webview2 窗口 + 内置 HTTP server），已废弃
- `readalong-wails/`（Windows 工作区那份）已搬迁到 WSL，**不要再用那份**

---

## 1. 产品需求

### 1.1 要解决的问题

家长/老师手上有 B 站上的英语课文跟读视频，但视频只能"整段播放"，没法逐句反复练。
需要一个工具把视频变成**可逐句跟读、可录音对比的课程**。

### 1.2 核心用户流程

```
粘贴视频链接 → 自动下载音轨、语音识别、切句 → 生成课程
→ 打开课程 → 逐句听原声 → 录音跟读 → A/B 对比 → 标记练过
```

### 1.3 功能清单（v2.0.0 已实现）

**生成**
- 支持 B 站及 yt-dlp 能解析的站点；自动识别分 P
- 语言可选：英语 / 中文 / 自动识别
- 五步流水线带百分比进度和滚动日志
- 首次运行自动下载外部程序（yt-dlp / ffmpeg / whisper-cli）和语音模型
- 课程标题自动整理：`外研新交际英语一年级上册【课本+跟读】 p22 课文跟读unit 5`
  → `P22 课文跟读unit 5 · 外研新交际英语一年级上册`

**练习**
- 三种模式：单句循环 / A/B 对比（原声→我的录音）/ 整篇连读
- 语速 1x / 0.75x
- 每句可「听原声」「录音」「听我的录音」
- 句子文本可直接编辑，改动存 localStorage
- 练过的句子标绿，顶部显示 `已练 12/48` 进度条
- 快捷键：`空格` 重听当前句，`R` 开始/停止录音
- 按静音间隔自动分「段」，长课文有分段标题

**课程库**
- 卡片列表（标题、句数、时长、创建时间）
- 打开 / 删除 / 打开数据目录

### 1.4 明确不做 / 待定

- **评分**：需要接 Azure Pronunciation Assessment 或讯飞 ISE/SOE，未做
- **录音持久化**：目前只在内存，刷新即丢（见 §8.2）
- **手机端 APK**：同一套代码不可能产出。原因见 §9

---

## 2. 技术选型与理由

| 选择 | 理由 |
| --- | --- |
| **Go + Wails v2** | 单 exe 分发、原生窗口（不用开浏览器）、GitHub Actions 交叉编译成熟 |
| **Vite + 原生 JS**（无框架） | 前端只有两个页面，引入 React/Vue 是纯负担；产物 13KB |
| **whisper.cpp CLI** | 纯 Go 没有可用 Whisper 实现；CGO 绑定会把构建搞复杂。CLI + JSON 最省事，且 `CGO_ENABLED=0` 保持可交叉编译 |
| **yt-dlp / ffmpeg** | 事实标准。运行时下载到数据目录，发布产物保持单文件 |
| **文件存储，不用数据库** | 课程目录可以直接拷走、直接删；无迁移成本 |
| **Whisper small.en / small** | base.en 质量不够（`This is the blackboard` → `Sis the board`），small.en 可用 |

模板来源：`https://github.com/hosea3000/health-tool`（分层结构 + Actions 发布方式照抄，**不要自己发挥结构**）。

---

## 3. 目录结构与职责

```
readalong-wails/
├── main.go                      Wails 启动、内嵌前端、/courses/* 资源路由、单实例锁
├── app.go                       对前端暴露的所有方法（Wails binding）
├── wails.json                   Wails 配置（名称、产物名、版本、前端构建命令）
├── go.mod / go.sum              依赖（go.sum 必须提交，否则 CI 挂）
│
├── config/config.go             运行期目录解析 + 模型名/路径规则
├── model/model.go               前后端共用数据结构（Segment/Course/Job/...）
├── store/store.go               课程文件读写（audio.wav + data.json + meta.json）
│
├── domain/
│   ├── tools.go                 外部程序查找与自举下载（yt-dlp/ffmpeg/whisper）
│   ├── modeldl.go               语音模型下载（hf-mirror 优先）
│   ├── asr.go                   whisper-cli 调用 + token 级时间戳还原成词
│   ├── align.go                 单词页时间戳按音频能量重排（见 5.3）
│   ├── segment.go               文本清洗、拼词、断句、分段
│   ├── pipeline.go              五步流水线编排
│   ├── proc_windows.go          Windows 子进程创建（关键：CREATE_NO_WINDOW）
│   ├── proc_other.go            非 Windows 直通
│   ├── domain_test.go           单元测试（用真实 whisper 输出做 fixture）
│   └── testdata/whisper_sample.json
│
├── frontend/
│   ├── index.html
│   ├── vite.config.js           outDir=dist, target=chrome105, dev port 34115
│   └── src/
│       ├── main.js              hash 路由：#/ 课程库、#/play/<id> 练习页
│       ├── api.js               Wails bridge 封装 + esc/fmtTime/toast
│       ├── idb.js               Reader 模式课程包仓库（IndexedDB）
│       ├── recstore.js          跟读录音仓库（IndexedDB）+ clearPractice，见 8.2
│       ├── ise.js               Reader 模式下的讯飞评测客户端（纯前端）
│       ├── reader.js            Reader（纯浏览器）入口，复用 renderPlayer
│       ├── style.css            全站样式（浅色，米白底 #f7f7f5 + 蓝色主色 #185fa5）
│       └── views/
│           ├── library.js       新建表单、模型下载进度、课程卡片、任务轮询
│           └── player.js        句子列表、三种模式、录音、进度、快捷键
│
├── build/
│   ├── appicon.png
│   └── windows/
│       ├── icon.ico
│       └── info.json            ⚠️ 必须用 Go template 占位符，见 §7.2
│
└── .github/workflows/release.yml  推 v* tag → 构建 exe → 发 Release
```

---

## 4. 数据存放

运行时数据全部在 `%LOCALAPPDATA%\ReadAlong\`（可用环境变量 `READALONG_DATA` 覆盖）：

```
courses\<courseID>\audio.wav     课程音频，16kHz 单声道 PCM
courses\<courseID>\data.json     完整课程（句子 + 时间轴）
courses\<courseID>\meta.json     课程库卡片信息（列表只读这个，快）
models\ggml-small.en.bin         语音模型（465MB）
bin\yt-dlp.exe / ffmpeg.exe / whisper-cli.exe + 相关 dll
logs\readalong.log               运行日志（同时输出到 stderr）
.tmp\<courseID>\                 流水线临时目录，跑完即删
```

### courseID 规则（`store.CourseIDFromURL`）

- 从 URL 里抓 `BVxxxxxxxxxx`，有 `p=` 参数就拼成 `BV1qR4fesEm6_p21`
- 同一个分 P 永远是同一个目录：目录里已有 `data.json` 就**覆盖**这门课（见 §8.1），
  只有生成到一半留下的空目录才会让位给 `_2`、`_3`
- 只保留 `[0-9A-Za-z_.-]`，用作 `ValidID` 白名单，防目录穿越

### data.json 结构

```json
{
  "id": "BV1qR4fesEm6_p21",
  "title": "P21 课文跟读unit 4 · 外研新交际英语一年级上册",
  "source": "https://www.bilibili.com/video/BV1qR4fesEm6/?p=21",
  "uploader": "咸咸的闲人MinYeah",
  "duration": 191.1,
  "audio": "/courses/BV1qR4fesEm6_p21/audio.wav",
  "lang": "en",
  "created": "2026-11-06 11:50",
  "segments": [
    { "start": 3.28, "end": 4.68, "text": "Blackboard.", "section": "第 1 段", "section_start": 3.3 }
  ]
}
```

`section` / `section_start` 只有分段数 > 1 时才写，单段时会被清空（前端据此决定是否显示分段标题）。

---

## 5. 流水线（`domain/pipeline.go`）

```
p.Step("检查运行环境", 2)     EnsureYTDLP / EnsureFFmpeg / EnsureWhisper（首次要下载）
p.Step("解析视频", 4)         yt-dlp --dump-single-json --no-playlist --skip-download
p.Step("下载音轨", 8→33)      yt-dlp -f ba/b --newline，解析 [download] xx% 更新进度
p.Step("转换 16k 单声道", 38)  ffmpeg -ac 1 -ar 16000 -c:a pcm_s16le
p.Step("识别音频", 45→90)      whisper-cli，stderr 里抓 progress=NN%
p.Step("切句与排版", 94)       BuildSegments → AddSections(gap=3.5)
完成                          100，落盘
```

进度是**硬编码区间**，各步耗时比例大致是：下载 20%、转码 5%、识别 70%。改模型或改视频长度后这个比例会变，但不影响可用性。

### 5.1 whisper 调用参数（`domain/asr.go`）

```
-m <models/ggml-<name>.bin> -f <wav> -l <lang>
-t <threads 2~8> -ojf -of <prefix> -pp -bs 5
```

- `-ojf` = 输出 full JSON（带 token 级时间戳），这是词级时间轴的关键
- `-of <prefix>` 指定输出前缀，结果落在 `<prefix>.json`
- `-bs 5` beam search 宽度，5 是质量/速度折中
- 输出 JSON 结构：`{result:{language}, transcription:[{offsets:{from,to}, text, tokens:[{text,offsets}]}]}`

### 5.2 token → 词 的还原（最容易出问题的一环）

whisper 的 token 是 BPE 子词，不保证和"词"对齐。做法：

1. 剔除控制 token：`[_BEG_]`、`[_TT_250]` 这类，判据是 `strings.HasPrefix(t,"[_") && HasSuffix(t,"]")`
2. 每个 token 的时间区间按**字符数等分**，得到每个字符的起止时间
3. 按空白切词；中文没有空白，按句末标点（`。？！`）再切一道
4. 拼回文本时中文之间不插空格、标点前不插空格（`JoinWords`）

`domain/domain_test.go` 用真实识别输出 `testdata/whisper_sample.json` 钉住了这个行为，**改动这块必须让测试继续通过**。

### 5.3 单词页时间轴要按音频重建（`domain/align.go`）

单词分 P 是「一个词、一段静音」的节奏，whisper 对这种音频给出的词级时间戳**不可用**：

- 退化成假网格：每词固定 0.4s、起点按 2s 等距排开、结尾对齐整秒（Unit6/Unit2 实测：18 个词里 9 个落在 -91dB 的数字静音上）
- 或者整体漂移：位置看着"像"，实际错开一两个词（Unit4 后半段：标注 `Very` 的位置听到的是 `ah`，`School` 的位置听到 `old`）
- 还会多出音频里根本没有的词（Unit2 末尾凭空多出 `One Two Three`）

只按位置怎么调都救不回来（漂移量常常接近词间距），所以单词页（`pageKind == "word"`）
改走**内容对齐**，`AlignWordPage`：

1. 20ms 一帧算 dBFS，用这一段自己的噪声底/峰值定阈值，取出发声段（一个词一段）
2. 每段切出来单独复听（`RecognizeClip`），得到那一段真正念的是什么
3. 词和发声段做**单调 DP** 配对：代价 = 文字相似度（主）+ 位置（辅）
   - 一个段可以承载多个词，但仅限那段确实含这个词（`Chinese New Year`）；
     一段只念了一个词却要塞两个词，判为识别重复，丢掉
   - 后面的发声段还够用时不许挤在同一段（挤了会让后面的词整体错位）
4. 音频里确实没有的词直接丢弃；几个词共用一个段时按字数摊时间

`WordAlignVersion` 是算法版本：生成课程时写进 `data.json`，老课程在启动后台
`migrateWordTimeline` 里重做一次（音频和 whisper 都已在本地），不会每次启动都跑。

> 逐段复听是每个发声段起一次 whisper 进程，一个单词页 15~25 个词大约多花 10~20 秒，
> 只在单词分 P 上付这个代价；拿不到 whisper（模型没下完）时退回纯位置吸附。

---

## 6. 关键实现细节（踩过的坑，别重犯）

### 6.1 Windows GUI 子进程退出码 0xC000013A（最坑的一个）

`wails build` 产出的是 GUI 子系统程序，它 spawn 控制台程序（yt-dlp 等）时，
Windows 会给子进程分配一个临时控制台；该控制台销毁时子进程收到 `CTRL_CLOSE_EVENT`，
以 `0xC000013A`（STATUS_CONTROL_C_EXIT）退出 —— 表现就是"下载音轨失败"，但命令本身没报错。

**解法**：所有子进程统一走 `domain.NewCmd`，设置 `CREATE_NO_WINDOW (0x08000000)` + `HideWindow`。
**不要在任何地方直接用 `exec.Command`**。

### 6.2 RE2 不支持 `\u` 转义

Go 的 `regexp` 是 RE2，`[A-Za-z\u4e00-\u9fff]` 会 panic。
必须写 `[A-Za-z\x{4e00}-\x{9fff}]`。

### 6.3 图标/版本信息文件格式（`build/windows/info.json`）

必须用 Wails 的 Go template 占位符：

```json
{
  "fixed": { "file_version": "{{.Info.ProductVersion}}" },
  "info": { "0000": {
    "ProductVersion": "{{.Info.ProductVersion}}",
    "CompanyName": "{{.Info.CompanyName}}",
    "FileDescription": "{{.Info.ProductName}}",
    "LegalCopyright": "{{.Info.Copyright}}",
    "ProductName": "{{.Info.ProductName}}",
    "Comments": "{{.Info.Comments}}"
  } }
}
```

写成老格式（`"file_version": "2.0.0"` + `"Translation": {"LangID": "0409"}`）会让资源生成失败。

### 6.4 whisper.cpp 预编译包只有旧版本

最新版（v1.9.x）没有 Windows 预编译产物。**必须锁 v1.7.6**：
`https://github.com/ggml-org/whisper.cpp/releases/download/v1.7.6/whisper-bin-x64.zip`
里面是 `whisper-cli.exe` + 若干 `ggml-*.dll`，`pickWhisper` 会把 exe 和 dll 都解出来。

### 6.5 模型下载要走镜像

`huggingface.co` 国内基本不通，`modeldl.go` 里镜像顺序是 `hf-mirror.com` → `huggingface.co`。
`downloadFile` 支持 HTTP Range 断点续传（`.part` 文件）。

### 6.6 课程音频必须走自定义处理器

前端 `<audio>` 要能拖进度，必须返回支持 Range 的响应。
`main.go` 的 `newAssetHandler` 把 `/courses/*` 拦下来，用 `http.ServeFile` 从数据目录读文件（自带 Range 支持），其余交给内嵌的前端资源。

Wails 的 `/wails/runtime.js` 和 IPC 在进入自定义 handler 之前就已处理，不受影响。

### 6.7 播放页 audio.src 必须还原

录音用 `URL.createObjectURL(blob)` 塞进 `<audio>`。录音播放结束走的是 `ended` 事件
（没有 `stopAt`，`timeupdate` 里会直接 return），如果不处理，`audio.src` 就一直卡在 blob URL，
之后点「原声」播不出来。`player.js` 里 `stopAudio()` + `ended` 监听共同负责还原。

### 6.8 分数口径：分母是全部题目，不是已评题目

任务行 / 书架上的分数**不是**「已评句子的平均分」，而是：

```
得分 = 已评分数总和 / 全部题数        （没评过的题按 0 分算，100 分制）
```

所以「10 个单词只读了 2 个、两个都 100 分」显示 **20**，不是 100 —— 这个数字本身就是完成度，
用户一眼能看出读了多少。实现在 `frontend/src/score.js` 的 `scoreFor()`，调用方必须给出总题数：

- 任务行：`scoreFor(scores, t.indexes)`（分母 = 该任务的题数，也就是 `indexes.length`）
- Unit 行 / 课程卡：`scoreFor(scores, null, course.count)`（分数表只知道评过的句子，
  所以总题数必须从 `meta.json` 的 `count` 传进来，不能靠 `Object.keys(scores).length`）
- 整本书：`已得分总和 / 总句数`

每句只保留**历史最好成绩**（`ra.score.<courseId>`），重录不会把分数改低。

---

## 7. 构建与发布

### 7.1 本地构建

```bash
# 环境：Go 1.25+、Node 22+、Wails CLI v2.13.0
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0

cd frontend && npm install && cd ..
wails dev                                        # 热重载开发
wails build                                      # 产物 build/bin/readalong.exe
```

WSL 里注意：
- `wails dev` 需要图形环境（Windows 11 的 WSLg 自带；老版本要自己起 X server）
- 只想出包的话 `wails build -platform windows/amd64` 交叉编译即可，产物直接在 Windows 上跑

不用 `wails build` 而直接 `go build .` 也能过编译，但产物是**控制台版**且没有图标/清单，
只适合快速验证编译是否通过。

### 7.2 发布（GitHub Actions）

推 `v*` tag 即触发 `.github/workflows/release.yml`：

```bash
git tag v2.0.1 && git push origin v2.0.1
```

流程：两个工作流并行，都由 tag 触发

- `release.yml`：ubuntu-latest → setup-go 1.25 → setup-node 22 → `go install wails@v2.13.0`
  → 从 tag 写 `wails.json` 的 `productVersion` → `wails build -clean -platform windows/amd64`
  → 确保 Release 存在再上传 exe
- `android.yml`：`npm run build:mobile` → `npx cap sync android` → gradle 出 APK（见 §9）

一个 tag 的 Release 有两个资产（两个 job 并行上传，Release 记录会先出现、资产后到，
别在开头几分钟里判断“产物没了”）：

| 资产 | 内容 |
| --- | --- |
| `readalong.exe` | 桌面 Studio（课程生成 + 练习），约 12MB |
| `ReadAlong-<sha>.apk` | 手机版（Capacitor 壳 + Reader 前端），见 §9 |

> 曾经还有一个 `readalong-reader.zip`（`frontend/dist` 打的纯前端包）。Reader 已经随 APK 发布、
> 桌面用户用不到，v2.0.2 起不再上传；需要时本地 `cd frontend && npm run build` 自己打包即可。

`latest` 是 `android.yml` 在 push main 时滚动的**预发布** Release，按设计只挂 APK，不带 exe。

仓库 Secret：`ANDROID_KEYSTORE_B64`、`ANDROID_KEYSTORE_PW`（缺任意一个就退化成 debug 包并在
日志里告警，工作流不会红）。

⚠️ **`go.sum` 必须提交**，否则 CI 报 `missing go.sum entry`。
⚠️ 在 Linux 上交叉编译 Windows 不需要 mingw，Wails v2 的 Windows 实现是纯 Go + syscall。

### 7.3 仓库凭据

推送用 SSH。密钥在 WSL 的 `/root/.ssh/id_rsa`，对应 GitHub 账号 **phj2ll**。
```
git clone git@github.com:phj2ll/readalong.git
```

---

## 8. 已知问题 / 待优化

### 8.1 重新生成同一链接会产生副本（**已修**）

老行为：`store.CourseIDFromURL` 发现有 `data.json` 就追加时间后缀，
于是同一个分 P 生成出 `BV1qR4fesEm6_p22_160512` 这样的第二门课，练习进度还按 id 走、等于清零。

现在改成**覆盖**：

- `store.CourseIDFromURL` 直接返回 base id，只有「生成到一半留下的空目录」才让位给 `base_2`、`base_3`
- `app.CreateCourse` 先用 `store.CourseExists(cid)` 判断，是覆盖就写进 job 日志，
  并在 `model.Job.Replaced` 上打标记
- 前端（`views/library.js`）看到 `replaced` 就调 `clearPractice(courseId)`
  把 `ra.done` / `ra.edits` / `ra.score` 和录音一起清掉 ——
  句子时间轴已经变了，这些按句号索引的数据留着只会指向别的句子

批量生成（`runBatch`）仍是我行我素地「已存在就跳过」，要重来请先在课程库删掉整本书。

### 8.2 录音只存内存（**已修**）

老行为：`player.js` 用 `state.mine = new Map()` 存 Blob，刷新/切课程即丢（分数在 localStorage 里还在，
但点「我的录音」只会提示「这句还没录音」）。

现在录音落到 IndexedDB（`frontend/src/recstore.js`，库名 `readalong-clips`）：

- key = `<courseId>#<句号>`，每句只留最新一条（重录覆盖），占用上限 = 句数 × 单条录音
- 进课程时 `loadClips()` 先把整门课读回 `state.mine` 再渲染；`playMine` 没缓存时再兜底查一次
- 删课程（`api.del`）时顺带清掉这门课的录音，不留孤儿数据
- 桌面版和 Reader 共用这套（都是浏览器 IndexedDB，Go 侧不用动）

### 8.3 无评分（**已完成**，原方案留档）

最终用的是**讯飞 ISE 流式评测**（`domain/ise.go` + `frontend/src/ise.js`）：

- 桌面版走 `app.AssessRecording`（Go 侧 WebSocket + 鉴权签名），Reader 走 `ise.js` 纯前端实现
- 凭据存 `settings.json`（课程库右上角「评测设置」），三项齐全才会自动评分
- 录完自动送评，分数只保留历史最好成绩（`ra.score.<courseId>`）；
  焦点卡显示总分 + 准确/流利明细，列表页显示分数徽标
- 坑都在 `domain/ise.go` 注释里：JSON 键顺序、日期必须 `GMT` 而非 `UTC`、
  签名原文用 `GET`、英文试题要带 `[word]`/`[content]` 节点、40ms 一帧慢慢灌

### 8.4 旧 Python 版数据不兼容

`%LOCALAPPDATA%\ReadAlong\models\small.en\model.bin` 是 **CTranslate2 格式**（faster-whisper 用的），
whisper.cpp 不认，只认 `models\ggml-<name>.bin`。所以首次运行会重新下 465MB，那 483MB 旧模型可以直接删。

旧课程（p21/p22）的 `data.json` 缺 `id/duration/uploader` 字段，但 `meta.json` 是完整的，
所以课程库能正常显示、能练；只有播放页顶部时长会显示 0:00。不影响使用。

### 8.5 首次生成等待较久

模型 465MB + 工具 60MB，国内即便走镜像也要几分钟。UI 有进度条，但第一次体验仍然重。
如果要优化：把 `ggml-base.en.bin`（142MB）作为首次默认，质量降一档但下载快 3 倍。

### 8.6 书名解析（**已修**，出过「书名变成 6」这种怪事）

书名 = `SeriesOf(CleanTitle(分P标题))`，也就是「`·` 后面那段」。三个坑：

- `cleaned[i+1:]` 切「`·`」——它是 **2 字节** UTF-8，切完留下 0xB7，书名写进 JSON 会变成乱码字符。
  现在统一按 `len(titleSep)` 跳（`store/zip.go` 里同样的 bug 也一起修了）
- 分 P 标题前面带节号时（`6 p22 单词跟读`），`CleanTitle` 会把「6」当系列名塞到中点后面，
  书名就成了「6」。`SeriesOf` 现在丢弃纯数字 / 纯符号 / 只有一个字的结果，让上层兜底
- `runBatch` 里 `BookTitle = SeriesOf(...)` 没有兜底，某本书只有这类课程时会退化成裸 `BV` 号

另外 `migrateWordTimeline`（§5.3）早期版本会拿 `data.json` 还原 `Course` 后 `SaveMeta`，
把只存在于 `meta.json` 的 `bookTitle` 覆盖成空（真实数据里 `BV1qR4fesEm6_u3` 就这样丢过书名）。
现在 `store.summaryOf` 在新值为空时保留卡片上的旧值。

---

## 9. APK 是怎么来的（Capacitor 壳，重要，别重复讨论）

「Wails 出不了 APK」这半句仍然成立：Wails 只有 Windows/macOS/Linux target，而桌面版的干活能力
来自三个外部命令行程序（`yt-dlp`、`ffmpeg`、`whisper-cli`）+ 465MB 模型，Android 上跑不了。

所以 APK 走的是**只装 Reader 的壳**：

- `frontend` 用 `npm run build:mobile` 产出 `dist-mobile`（Reader 入口：纯前端 + JS 直连讯飞评分）
- `mobile/` 是 Capacitor 工程：`appId = com.phj2ll.readalong`，`webDir = ../frontend/dist-mobile`
- `.github/workflows/android.yml`：`npx cap sync android` → `gradlew assembleRelease` 出 APK
- 课程靠桌面版「导出整本书」的 zip 导入，手机端不解析链接、不做 ASR、不生成课程

手机端能做：听原音、跟读、录音回放、讯飞评分；不能做：贴 B 站链接、切句、生成课程。
签名：vivo 等 ROM 拒装 debug 证书的包，所以必须走 release 签名；keystore 与密码都在仓库 Secret
（`ANDROID_KEYSTORE_B64` / `ANDROID_KEYSTORE_PW`），本地 `mobile/keystore/readalong.jks` 不入库
（`.gitignore` 已挡）。**不要再把密码写进 workflow 或任何提交里。**

---

## 10. 接手后建议的下一步

按优先级：

1. **跑起来确认功能完整**
   ```bash
   cd frontend && npm install && cd ..
   wails dev
   ```
   用 `https://www.bilibili.com/video/BV1qR4fesEm6/?p=23` 走一遍生成流程。

2. ~~修 §8.1 的重复课程问题~~ / ~~录音持久化（§8.2）~~ / ~~加评分（§8.3）~~ —— 已做完

3. 细节打磨：
   - 播放页加"上一句/下一句"按钮
   - 长课程加筛选（只看未练）
   - 生成失败时的错误提示更具体（目前是把 yt-dlp/ffmpeg 的 stderr 尾部截一段）

---

## 11. 环境备忘

**WSL（主要开发环境）**
- 项目在 `/home/wwwroot/readalong`，git 已连 remote，可直接 `git push`
- 依赖没随项目搬过来，第一次要装：
  ```bash
  cd /home/wwwroot/readalong/frontend && npm install
  go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
  export PATH="$PATH:$(go env GOPATH)/bin"
  ```
- `wails dev` 需要图形环境：Windows 11 的 WSLg 自带；老版本要自己配 X server / VcXsrv
- 不出窗口的替代做法：`wails build -platform windows/amd64` 交叉编译，产物放到 Windows 上双击运行
- 语音模型和工具在 Windows 侧的 `%LOCALAPPDATA%\ReadAlong\`，WSL 里运行时会重新下到 `~/.readalong`
  （或者设 `READALONG_DATA=/mnt/c/Users/penghuangjie/AppData/Local/ReadAlong` 复用，省一次下载）

**Windows（仅历史产物）**
- Go 1.25.4 留在 `WorkBuddy/2026-11-05-17-44-16/_gosdk/sdk/go`，需要时可用
- git 在 PortableGit 的 `cmd/git.exe`

**关于 Wails 窗口**
- 在没有真实桌面会话的沙箱/服务环境里，Wails 会弹一个标题为 "Error" 的对话框然后卡住，
  连最小 Wails 程序也一样 —— 这是 WebView2 起不来，不是代码问题
- 判断方法：`Get-Process | ? { $_.MainWindowTitle -ne "" }`，看到进程窗口标题是 `Error` 就是这种情况

