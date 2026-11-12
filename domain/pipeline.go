package domain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"readalong/config"
	"readalong/model"
)

// Progress 让上层把流水线进度接到界面上。
type Progress interface {
	Step(step string, pct int)
	Log(line string)
}

// Result 是流水线产物：课程数据 + 还没落盘的 wav。
type Result struct {
	Course  model.Course
	WavPath string
}

type ytEntry struct {
	Title    string    `json:"title"`
	Duration float64   `json:"duration"`
	Uploader string    `json:"uploader"`
	Type     string    `json:"_type"`
	Entries  []ytEntry `json:"entries"`
}

var dlPctRe = regexp.MustCompile(`\[download\]\s+([\d.]+)%`)

// Run 跑完整流水线：解析 → 下音轨 → 转码 → 识别 → 切句。
func Run(rawURL, courseID, lang string, p Progress) (Result, error) {
	work := filepath.Join(config.TmpDir, courseID)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Result{}, fmt.Errorf("无法创建临时目录：%w", err)
	}
	defer os.RemoveAll(work)

	// 0. 预检外部程序。首次运行要下载 yt-dlp/ffmpeg/whisper-cli，
	// 放在最前面单独给一步进度，否则界面会长时间停在 4% 不动。
	p.Step("检查运行环境", 2)
	p.Log("检查 yt-dlp")
	ytdlp, err := EnsureYTDLP()
	if err != nil {
		return Result{}, err
	}
	for _, t := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"ffmpeg", EnsureFFmpeg},
		{"whisper-cli", EnsureWhisper},
	} {
		p.Log("检查 " + t.name)
		if _, terr := t.fn(); terr != nil {
			return Result{}, terr
		}
	}

	// 1. 解析
	p.Step("解析视频", 4)
	p.Log("正在解析视频信息")
	info, err := probeVideo(ytdlp, rawURL)
	if err != nil {
		return Result{}, err
	}
	title := CleanTitle(info.Title)
	if title == "" {
		title = courseID
	}
	duration := info.Duration
	p.Log("标题：" + title)
	if duration > 0 {
		p.Log("时长：" + FmtTS(duration))
	}

	// 2. 下载音轨
	p.Step("下载音轨", 8)
	src, err := downloadAudio(ytdlp, rawURL, work, p)
	if err != nil {
		return Result{}, err
	}
	if st, serr := os.Stat(src); serr == nil {
		p.Log(fmt.Sprintf("音轨：%s %.1fMB", filepath.Base(src), float64(st.Size())/1048576))
	}

	// 3. 转码
	p.Step("转换 16k 单声道", 38)
	wav := filepath.Join(work, "audio.wav")
	if err := toWav(src, wav); err != nil {
		return Result{}, err
	}
	if duration <= 0 {
		duration = wavDuration(wav)
	}

	// 4. 识别
	modelName := config.ModelName(lang)
	p.Step("识别音频（"+modelName+"）", 45)
	p.Log("开始识别 " + FmtTS(duration) + "，第一次会比较慢")
	asrLang := lang
	if asrLang == "" {
		asrLang = "auto"
	}
	rawSegs, detected, err := Transcribe(wav, asrLang, modelName, func(pct int) {
		p.Step("识别音频（"+modelName+"）", 45+pct*45/100)
	})
	if err != nil {
		return Result{}, err
	}

	// 5. 切句
	p.Step("切句与排版", 94)
	wordPage := pageKind(info.Title) == "word"
	segs := AddSections(BuildSegments(rawSegs, wordPage), 3.5)
	if len(segs) == 0 {
		return Result{}, fmt.Errorf("切句结果为空，可能是纯音乐或噪音")
	}
	if wordPage {
		read := func(clip string) (string, error) { return RecognizeClip(clip, asrLang, modelName) }
		if fixed, ok := AlignWordPage(wav, segs, read); ok {
			segs = fixed
			p.Log(fmt.Sprintf("按发音位置校正了单词时间轴（%d 个词）", len(segs)))
		}
	}

	p.Log(fmt.Sprintf("切出 %d 句", len(segs)))

	// defer 会删掉整个临时目录，先把 wav 挪到 TmpDir 根下暂存，
	// 等上层 Save 完成后再由上层删除这个暂存文件。
	staging := filepath.Join(config.TmpDir, fmt.Sprintf("wav-%d.wav", time.Now().UnixNano()))
	if err := moveOrCopy(wav, staging); err != nil {
		return Result{}, fmt.Errorf("暂存音频失败：%w", err)
	}

	return Result{
		Course: model.Course{
			ID:           courseID,
			Title:        title,
			Source:       rawURL,
			Uploader:     info.Uploader,
			Duration:     Round1(duration),
			Audio:        "/courses/" + courseID + "/audio.wav",
			Lang:         detected,
			Created:      time.Now().Format("2006-01-02 15:04"),
			Segments:     segs,
			AlignVersion: WordAlignVersion,
		},
		WavPath: staging,
	}, nil
}

// moveOrCopy 把文件挪到新位置，跨卷时退化成复制。
func moveOrCopy(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// PageInfo 是多 P 视频里一个分 P 的概要。
type PageInfo struct {
	Title    string
	Duration float64
	Cid      int64 // B 站分 P 的内容 ID，直连下载音频用
}

// biliUA 是 B 站接口要求的常规浏览器 UA。
const biliUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var bvOnlyRe = regexp.MustCompile(`BV([0-9A-Za-z]{10})`)

// biliPages 用视频详情接口一次拿到全部分 P 的真实标题和 cid。
// 详情会缓存，后续探测/下载不会再发请求。
func biliPages(rawURL string) ([]PageInfo, bool) {
	bv := bilibiliBV(rawURL)
	if bv == "" || !strings.Contains(rawURL, "bilibili.com") {
		return nil, false
	}
	v, err := biliViewOf(bv)
	if err != nil || len(v.Pages) == 0 {
		return nil, false
	}
	pages := make([]PageInfo, 0, len(v.Pages))
	for _, pg := range v.Pages {
		pages = append(pages, PageInfo{Title: pg.Part, Duration: pg.Duration, Cid: pg.Cid})
	}
	return pages, true
}

// ListPages 探测链接是不是多 P。是就返回全部分 P（按顺序），单 P 返回空切片。
// 优先走 B 站分 P 接口（标题完整、一次请求）；其他站点退回 yt-dlp 完整解析。
func ListPages(rawURL string) ([]PageInfo, error) {
	if pages, ok := biliPages(rawURL); ok {
		if len(pages) <= 1 {
			return nil, nil
		}
		return pages, nil
	}

	bin, err := EnsureYTDLP()
	if err != nil {
		return nil, err
	}
	args := []string{"--dump-single-json", "--no-warnings", "--skip-download"}
	if strings.Contains(rawURL, "bilibili.com") {
		args = append(args,
			"--user-agent", biliUA,
			"--add-header", "Referer:https://www.bilibili.com/",
			"--sleep-requests", "1",
		)
	}
	cmd := NewCmd(bin, append(args, rawURL)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("解析分 P 列表失败：%v %s", err, tail(errb.String(), 200))
	}
	var e ytEntry
	if uerr := json.Unmarshal(out, &e); uerr != nil {
		return nil, fmt.Errorf("解析结果不是合法 JSON：%w", uerr)
	}
	if e.Type != "playlist" || len(e.Entries) <= 1 {
		return nil, nil
	}
	pages := make([]PageInfo, 0, len(e.Entries))
	for _, en := range e.Entries {
		pages = append(pages, PageInfo{Title: en.Title, Duration: en.Duration})
	}
	return pages, nil
}

// WithPage 给链接拼上分 P 参数。
func WithPage(rawURL string, page int) string {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sp=%d", rawURL, sep, page)
}

// ---------------- 按 Unit 分组合并 ----------------

// GroupPage 是分组里的一个分 P。
type GroupPage struct {
	Page  int    // 第几个分 P（1 起）
	Title string // 分 P 标题
}

// Group 是一门合并课程：一个 Unit（或无 unit 的单个 P）。
type Group struct {
	Label  string      // 展示名，如 "Unit 1" / "P3"
	Suffix string      // 课程目录后缀，如 "u1" / "p3"
	Pages  []GroupPage // 按顺序要合并的分 P
	Videos []GroupPage // 关联的教学视频课分 P（不生成跟读，内嵌播放）
}

var unitRe = regexp.MustCompile(`(?i)unit\s*([0-9]+)`)
var cnUnitRe = regexp.MustCompile(`第\s*([0-9一二三四五六七八九十]+)\s*单元`)

// cnNum 把中文数字转成阿拉伯数字（教材单元数一般不超过二十）。
func cnNum(s string) string {
	digit := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	switch {
	case s == "十":
		return "10"
	case s == "二十":
		return "20"
	}
	rs := []rune(s)
	if len(rs) == 1 {
		if d, ok := digit[rs[0]]; ok {
			return fmt.Sprintf("%d", d)
		}
		return s // 已经是阿拉伯数字
	}
	// 十X / X十 / X十Y
	if len(rs) == 2 {
		if rs[0] == '十' {
			if d, ok := digit[rs[1]]; ok {
				return fmt.Sprintf("1%d", d)
			}
		}
		if rs[1] == '十' {
			if d, ok := digit[rs[0]]; ok {
				return fmt.Sprintf("%d0", d)
			}
		}
	}
	if len(rs) == 3 && rs[1] == '十' {
		a, ok1 := digit[rs[0]]
		b, ok2 := digit[rs[2]]
		if ok1 && ok2 {
			return fmt.Sprintf("%d%d", a, b)
		}
	}
	return s
}

// unitOf 从分 P 标题里提取单元号，兼容 Unit 1 / unit1 / 第1单元 / 第一单元。
func unitOf(title string) string {
	if m := unitRe.FindStringSubmatch(title); m != nil {
		return m[1]
	}
	if m := cnUnitRe.FindStringSubmatch(title); m != nil {
		return cnNum(m[1])
	}
	return ""
}

// pageVideoRe 匹配「教学视频课」分 P（Let's start / Let's read / Let's explore 等），
// 这类是动画教学内容，不适合切句跟读，直接跳过。
var pageVideoRe = regexp.MustCompile(`(?i)let's|lets\s|explore|activity|动画|story time`)

// pageKind 判断分 P 类型：word 单词 / text 课文 / video 视频课（跳过）。
func pageKind(title string) string {
	t := strings.ToLower(title)
	if strings.Contains(t, "单词") || strings.Contains(t, "word") {
		return "word"
	}
	if strings.Contains(t, "课文") || strings.Contains(t, "text") {
		return "text"
	}
	if pageVideoRe.MatchString(t) {
		return "video"
	}
	return "text"
}

// GroupPages 把多 P 按 unit 归组成课：
//   - 「课文跟读unit N」「unitN-单词」归入 Unit N 这门课（课文在前、单词在后）
//   - 「Unit N-Let's …」教学视频课跳过不生成
//   - 没有 unit 关键词的分 P 各自成一门课
func GroupPages(pages []PageInfo) []Group {
	var out []Group
	byKey := map[string]int{}
	videosByUnit := map[string][]GroupPage{} // 视频课分 P 按 unit 归档
	for i, pg := range pages {
		if pageKind(pg.Title) == "video" {
			if u := unitOf(pg.Title); u != "" {
				gp := GroupPage{Page: i + 1, Title: pg.Title}
				videosByUnit["u"+u] = append(videosByUnit["u"+u], gp)
			}
			continue
		}
		gp := GroupPage{Page: i + 1, Title: pg.Title}
		if u := unitOf(pg.Title); u != "" {
			key := "u" + u
			if idx, ok := byKey[key]; ok {
				out[idx].Pages = append(out[idx].Pages, gp)
				continue
			}
			byKey[key] = len(out)
			out = append(out, Group{Label: "Unit " + u, Suffix: key, Pages: []GroupPage{gp}})
			continue
		}
		// 没有 unit 关键词：单独成课，标签用分 P 的实际内容名，别只给个 Pxx
		label := CleanTitle(pg.Title)
		if j := strings.LastIndex(label, "·"); j >= 0 {
			label = strings.TrimSpace(label[:j])
		}
		if label == "" {
			label = fmt.Sprintf("P%d", i+1)
		}
		out = append(out, Group{
			Label:  label,
			Suffix: fmt.Sprintf("p%d", i+1),
			Pages:  []GroupPage{gp},
		})
	}
	// 把视频课分 P 挂到对应 unit 的课程上
	for idx := range out {
		out[idx].Videos = videosByUnit[out[idx].Suffix]
	}
	return out
}

// SeriesOf 从 CleanTitle 结果里取中点后面的系列名（书名）。
func SeriesOf(cleaned string) string {
	if i := strings.LastIndex(cleaned, "·"); i >= 0 {
		return strings.TrimSpace(cleaned[i+1:])
	}
	return ""
}

// subProgress 给单个分 P 的进度映射一层前缀和区间。
type subProgress struct {
	base   int
	span   int
	prefix string
	up     Progress
}

func (s subProgress) Step(step string, pct int) {
	v := s.base + pct*s.span/100
	if v > 99 {
		v = 99
	}
	s.up.Step(s.prefix+" "+step, v)
}

func (s subProgress) Log(line string) { s.up.Log(s.prefix + " " + line) }

// RunGrouped 把一个 Unit 的多个分 P 合并成一门课：
// 音轨按顺序拼接，句子时间戳加上偏移，每个分 P 是课内一个分段。
func RunGrouped(rawURL, courseID, lang, label string, group []GroupPage, p Progress) (Result, error) {
	work := filepath.Join(config.TmpDir, courseID)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Result{}, fmt.Errorf("无法创建临时目录：%w", err)
	}
	defer os.RemoveAll(work)

	p.Step("检查运行环境", 2)
	ytdlp, err := EnsureYTDLP()
	if err != nil {
		return Result{}, err
	}
	if _, err := EnsureFFmpeg(); err != nil {
		return Result{}, err
	}
	if _, err := EnsureWhisper(); err != nil {
		return Result{}, err
	}

	span := 100 / len(group)
	if span < 1 {
		span = 1
	}
	modelName := config.ModelName(lang)
	asrLang := lang
	if asrLang == "" {
		asrLang = "auto"
	}

	var segs []model.Segment
	var wavs []string
	var series, uploader, detected string
	var offset float64

	for k, gp := range group {
		sub := subProgress{base: k * span, span: span, prefix: fmt.Sprintf("P%d", gp.Page), up: p}
		pageURL := WithPage(rawURL, gp.Page)

		sub.Step("解析视频", 4)
		info, err := probeVideo(ytdlp, pageURL)
		if err != nil {
			return Result{}, err
		}
		if uploader == "" {
			uploader = info.Uploader
		}
		if series == "" {
			series = SeriesOf(CleanTitle(info.Title))
		}

		sub.Step("下载音轨", 8)
		pdir := filepath.Join(work, fmt.Sprintf("p%d", gp.Page))
		if err := os.MkdirAll(pdir, 0o755); err != nil {
			return Result{}, err
		}
		src, err := downloadAudio(ytdlp, pageURL, pdir, sub)
		if err != nil {
			return Result{}, err
		}

		sub.Step("转换 16k 单声道", 38)
		wav := filepath.Join(pdir, "audio.wav")
		if err := toWav(src, wav); err != nil {
			return Result{}, err
		}

		sub.Step("识别音频（"+modelName+"）", 45)
		rawSegs, det, err := Transcribe(wav, asrLang, modelName, func(pct int) {
			sub.Step("识别音频（"+modelName+"）", 45+pct*45/100)
		})
		if err != nil {
			return Result{}, err
		}
		if detected == "" {
			detected = det
		}

		// 单词分 P 用单词表切分规则（逗号即边界、停顿阈值 0.4s）
		wordPage := pageKind(gp.Title) == "word"
		pageSegs := BuildSegments(rawSegs, wordPage)
		if len(pageSegs) == 0 {
			sub.Log("没有识别到内容，跳过这个分 P")
			continue
		}
		if wordPage {
			// whisper 对「一个词一段静音」的单词跟读音频会给退化时间戳
			// （每词固定 0.4s、按 2s 等距排开），改成逐段复听 + 真实发声区间落点。
			read := func(clip string) (string, error) { return RecognizeClip(clip, asrLang, modelName) }
			if fixed, ok := AlignWordPage(wav, pageSegs, read); ok {
				pageSegs = fixed
				sub.Log(fmt.Sprintf("按发音位置校正了单词时间轴（%d 个词）", len(pageSegs)))
			}
		}
		for i := range pageSegs {
			pageSegs[i].Start += offset
			pageSegs[i].End += offset
			pageSegs[i].Section = CleanTitle(gp.Title)
			pageSegs[i].SectionStart = offset
		}
		segs = append(segs, pageSegs...)
		wavs = append(wavs, wav)
		offset += wavDuration(wav)
		sub.Log(fmt.Sprintf("切出 %d 句", len(pageSegs)))
	}

	if len(wavs) == 0 {
		return Result{}, fmt.Errorf("各分 P 都没有识别到内容")
	}

	p.Step("拼接音频", 96)
	final := filepath.Join(work, "audio.wav")
	if err := concatWavs(wavs, final); err != nil {
		return Result{}, err
	}

	title := label
	if series != "" {
		title = label + " · " + series
	}

	staging := filepath.Join(config.TmpDir, fmt.Sprintf("wav-%d.wav", time.Now().UnixNano()))
	if err := moveOrCopy(final, staging); err != nil {
		return Result{}, fmt.Errorf("暂存音频失败：%w", err)
	}

	return Result{
		Course: model.Course{
			ID:           courseID,
			Title:        title,
			Source:       rawURL,
			Uploader:     uploader,
			Duration:     Round1(offset),
			Audio:        "/courses/" + courseID + "/audio.wav",
			Lang:         detected,
			Created:      time.Now().Format("2006-01-02 15:04"),
			Segments:     segs,
			AlignVersion: WordAlignVersion,
		},
		WavPath: staging,
	}, nil
}

// concatWavs 把若干 16k 单声道 wav 顺序拼成一个。
func concatWavs(wavs []string, dst string) error {
	if len(wavs) == 1 {
		return moveOrCopy(wavs[0], dst)
	}
	ff, err := EnsureFFmpeg()
	if err != nil {
		return err
	}
	list := dst + ".txt"
	var b bytes.Buffer
	for _, w := range wavs {
		q := strings.ReplaceAll(filepath.ToSlash(w), "'", `'\'`)
		fmt.Fprintf(&b, "file '%s'\n", q)
	}
	if err := os.WriteFile(list, b.Bytes(), 0o644); err != nil {
		return err
	}
	defer os.Remove(list)

	cmd := NewCmd(ff, "-y", "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", dst)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("拼接音频失败：%v %s", err, tail(errb.String(), 200))
	}
	if !fileExists(dst) {
		return fmt.Errorf("拼接后没有生成音频")
	}
	return nil
}

// biliArgs 给 yt-dlp 补 B 站需要的 UA/Referer，避免 412 风控。
func biliArgs(rawURL string) []string {
	if !strings.Contains(rawURL, "bilibili.com") {
		return nil
	}
	return []string{"--user-agent", biliUA, "--add-header", "Referer:https://www.bilibili.com/"}
}

func probeVideo(bin, rawURL string) (ytEntry, error) {
	// B 站：走接口拿详情，完全不碰网页，避开 412 风控
	if strings.Contains(rawURL, "bilibili.com") {
		_, page, v, err := biliPage(rawURL)
		if err != nil {
			return ytEntry{}, fmt.Errorf("解析视频失败：%w", err)
		}
		pg := v.Pages[page-1]
		title := pg.Part
		if title == "" {
			title = v.Title
		}
		return ytEntry{Title: title, Duration: pg.Duration, Uploader: v.Owner.Name}, nil
	}

	args := append([]string{"--dump-single-json", "--no-warnings", "--no-playlist", "--skip-download"}, biliArgs(rawURL)...)
	cmd := NewCmd(bin, append(args, rawURL)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return ytEntry{}, fmt.Errorf("解析视频失败：%v %s", err, tail(errb.String(), 260))
	}
	var e ytEntry
	if uerr := json.Unmarshal(out, &e); uerr != nil {
		return ytEntry{}, fmt.Errorf("解析结果不是合法 JSON：%w", uerr)
	}
	if e.Type == "playlist" && len(e.Entries) > 0 {
		return e.Entries[0], nil
	}
	return e, nil
}

func downloadAudio(bin, rawURL, work string, p Progress) (string, error) {
	// B 站：用 playurl 接口直接下音频流，绕开 yt-dlp 网页请求
	if strings.Contains(rawURL, "bilibili.com") {
		return biliDownload(rawURL, work, p)
	}

	args := append([]string{
		"-f", "ba/b",
		"--no-playlist",
		"--newline",
		"--no-warnings",
		"-o", filepath.Join(work, "src.%(ext)s"),
	}, biliArgs(rawURL)...)
	cmd := NewCmd(bin, append(args, rawURL)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 yt-dlp 失败：%w", err)
	}

	last := -1.0
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		m := dlPctRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		v, perr := strconv.ParseFloat(m[1], 64)
		if perr != nil || v-last < 10 {
			continue
		}
		last = v
		p.Step("下载音轨", 8+int(v*25/100))
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("音轨下载失败：%v %s", err, tail(errb.String(), 260))
	}

	matches, _ := filepath.Glob(filepath.Join(work, "src.*"))
	for _, m := range matches {
		if strings.HasSuffix(m, ".part") || strings.HasSuffix(m, ".ytdl") {
			continue
		}
		return m, nil
	}
	return "", fmt.Errorf("音轨下载失败：没找到输出文件")
}

func toWav(src, dst string) error {
	ff, err := EnsureFFmpeg()
	if err != nil {
		return err
	}
	cmd := NewCmd(ff, "-y", "-i", src, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", dst)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg 转码失败：%v %s", err, tail(errb.String(), 300))
	}
	if !fileExists(dst) {
		return fmt.Errorf("ffmpeg 没有生成 wav")
	}
	return nil
}

// wavDuration 直接读 RIFF 头算时长。
func wavDuration(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	head := make([]byte, 44)
	if _, err := f.Read(head); err != nil {
		return 0
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0
	}
	byteRate := int(binary32(head[28:32]))
	st, err := f.Stat()
	if err != nil || st.Size() <= 44 || byteRate <= 0 {
		return 0
	}
	return float64(int(st.Size())-44) / float64(byteRate)
}

func binary32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
