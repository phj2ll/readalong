# ReadAlong · 跟读课程生成器

粘一个视频链接，自动生成可跟读的课程：切句、逐句循环、录音对比。

Windows 单文件桌面应用，Go + Wails v2，前端 Vite（原生 JS，无框架）。

## 怎么用

1. 打开应用，第一次会自动下载语音模型（`small.en`，约 465MB），顶栏能看到进度。
2. 粘 B 站（或任何 yt-dlp 能解析的）视频链接，选语言，点「生成课程」。
3. 生成完自动跳到练习页：
   - **原声**：只听这一句
   - **录音**：跟读，读完点「停止」，会回放你的录音
   - 三种模式：单句循环 / A/B 对比 / 整篇连读
   - 句子文字可以直接改，改动存在本地
   - 快捷键：`空格` 重听当前句，`R` 开始/停止录音

首次生成会顺带下载 `yt-dlp`、`ffmpeg`、`whisper-cli` 到数据目录，之后离线可用。

## 数据放在哪

`%LOCALAPPDATA%\ReadAlong\`

```
courses\<id>\audio.wav     课程音频（16k 单声道）
courses\<id>\data.json     句子 + 时间轴
courses\<id>\meta.json     课程库卡片信息
models\ggml-small.en.bin   语音模型
bin\                       yt-dlp / ffmpeg / whisper-cli
logs\readalong.log
```

顶栏「数据目录」按钮可直接打开。课程是纯文件，拷走 `courses` 目录就等于备份。

## 开发

需要 Go 1.25、Node 22、Wails CLI v2.13.0。

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
cd frontend && npm install && cd ..
wails dev          # 热重载开发
wails build        # 产出 build/bin/readalong.exe
go test ./...      # 切句/拼词/控制 token 的单元测试
```

流水线分五步，都在 `domain/pipeline.go`：

```
解析视频 (yt-dlp --dump-single-json)
  → 下载音轨 (yt-dlp -f ba/b)
  → 转 16k 单声道 wav (ffmpeg)
  → 识别 (whisper-cli -ojf，带 token 级时间戳)
  → 切句 + 分段
```

`domain/asr.go` 里把 whisper 的 BPE token 按时间等分还原成词级时间轴，
并剔除 `[_BEG_]` / `[_TT_250]` 这类控制 token —— 这是最容易出问题的一环，
`domain/domain_test.go` 用真实识别输出钉住了这个行为。

## 发布

推 `v*` 标签即触发 GitHub Actions（`.github/workflows/release.yml`）：
ubuntu 上用 Go 1.25 + Node 22 + Wails CLI 交叉编译出 Windows exe，
直接挂到 Release 里。

```bash
git tag v2.0.1 && git push origin v2.0.1
```

版本号会同时写进 `wails.json` 的 `productVersion` 和 exe 的 `-X main.version`。

## 结构

```
config/    运行期目录解析
model/     前后端共用的数据结构
store/     课程文件读写（audio.wav + data.json + meta.json）
domain/    业务逻辑：外部工具下载、模型下载、ASR、切句、流水线
app.go     对前端暴露的绑定方法
main.go    Wails 启动 + /courses/* 自定义资源处理器
frontend/  Vite 前端（hash 路由：#/ 课程库，#/play/<id> 练习页）
```

没有引入数据库，一切以文件为准 —— 课程目录可以直接拷、直接删。
