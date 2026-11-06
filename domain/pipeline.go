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
	segs := AddSections(BuildSegments(rawSegs), 3.5)
	if len(segs) == 0 {
		return Result{}, fmt.Errorf("切句结果为空，可能是纯音乐或噪音")
	}

	p.Log(fmt.Sprintf("切出 %d 句", len(segs)))
	return Result{
		Course: model.Course{
			ID:       courseID,
			Title:    title,
			Source:   rawURL,
			Uploader: info.Uploader,
			Duration: Round1(duration),
			Audio:    "/courses/" + courseID + "/audio.wav",
			Lang:     detected,
			Created:  time.Now().Format("2006-01-02 15:04"),
			Segments: segs,
		},
		WavPath: wav,
	}, nil
}

func probeVideo(bin, rawURL string) (ytEntry, error) {
	cmd := NewCmd(bin, "--dump-single-json", "--no-warnings", "--no-playlist", "--skip-download", rawURL)
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
	cmd := NewCmd(bin,
		"-f", "ba/b",
		"--no-playlist",
		"--newline",
		"--no-warnings",
		"-o", filepath.Join(work, "src.%(ext)s"),
		rawURL,
	)
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
