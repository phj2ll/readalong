package domain

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"readalong/config"
	"readalong/model"
)

// 三个外部程序。解析顺序：程序同级 bin/ → 数据目录 bin/ → PATH → 下载。
// 发布产物是单 exe，所以首次运行会自动落到数据目录，之后离线可用。
const (
	urlYTDLP   = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
	urlFFmpeg  = "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl-shared.zip"
	urlFFmpegB = "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip"
	urlWhisper = "https://github.com/ggml-org/whisper.cpp/releases/download/v1.7.6/whisper-bin-x64.zip"
	userAgent  = "ReadAlong"
)

// ghMirrors 是 GitHub 加速代理。国内直连 github.com 经常被重置（wsarecv:
// connection forcibly closed），直连失败后逐个尝试这些前缀镜像。
var ghMirrors = []string{
	"https://gh-proxy.com/",
	"https://ghfast.top/",
}

// ghURLs 返回 [直连, 镜像1, 镜像2, ...] 的候选地址列表。
func ghURLs(url string) []string {
	out := make([]string, 0, 1+len(ghMirrors))
	out = append(out, url)
	for _, m := range ghMirrors {
		out = append(out, m+url)
	}
	return out
}

// toolSpec 描述一个外部程序的获取方式。
type toolSpec struct {
	key   string
	label string
	exe   string
	zip   bool
	urls  []string
	pick  picker
}

type picker func(nameInZip string) (outName string, ok bool)

func pickFFmpeg(name string) (string, bool) {
	base := strings.ToLower(filepath.Base(name))
	if base == "ffmpeg.exe" {
		return "ffmpeg.exe", true
	}
	// 共享构建要带上同目录的 dll
	if strings.HasSuffix(base, ".dll") {
		return base, true
	}
	return "", false
}

func pickWhisper(name string) (string, bool) {
	if strings.HasSuffix(name, "/") {
		return "", false
	}
	base := strings.ToLower(filepath.Base(name))
	if base == "whisper-cli.exe" || strings.HasSuffix(base, ".dll") {
		return base, true
	}
	return "", false
}

func specYTDLP() toolSpec {
	return toolSpec{key: "yt-dlp", label: "yt-dlp", exe: "yt-dlp.exe", urls: ghURLs(urlYTDLP)}
}

func specFFmpeg() toolSpec {
	return toolSpec{
		key: "ffmpeg", label: "ffmpeg", exe: "ffmpeg.exe", zip: true,
		urls: append(ghURLs(urlFFmpeg), ghURLs(urlFFmpegB)...), pick: pickFFmpeg,
	}
}

func specWhisper() toolSpec {
	return toolSpec{
		key: "whisper", label: "whisper.cpp", exe: "whisper-cli.exe", zip: true,
		urls: ghURLs(urlWhisper), pick: pickWhisper,
	}
}

var (
	toolMu    sync.Mutex
	toolCache = map[string]string{}
)

// EnsureTool 保证外部程序可用，必要时下载。
func EnsureTool(spec toolSpec) (string, error) {
	toolMu.Lock()
	defer toolMu.Unlock()

	if p := toolCache[spec.key]; p != "" && fileExists(p) {
		return p, nil
	}
	for _, dir := range []string{filepath.Join(config.ExeDir, "bin"), config.BinDir} {
		cand := filepath.Join(dir, spec.exe)
		if fileExists(cand) {
			toolCache[spec.key] = cand
			return cand, nil
		}
	}
	if p, err := exec.LookPath(spec.exe); err == nil {
		toolCache[spec.key] = p
		return p, nil
	}

	if err := os.MkdirAll(config.BinDir, 0o755); err != nil {
		return "", err
	}
	var lastErr error
	for _, u := range spec.urls {
		var err error
		if spec.zip {
			err = downloadAndExtract(u, config.BinDir, spec.pick)
		} else {
			err = downloadFile(u, filepath.Join(config.BinDir, spec.exe), nil)
		}
		if err == nil {
			p := filepath.Join(config.BinDir, spec.exe)
			if fileExists(p) {
				toolCache[spec.key] = p
				return p, nil
			}
			err = fmt.Errorf("解压后未找到 %s", spec.exe)
		}
		lastErr = err
	}
	return "", fmt.Errorf("无法获取 %s：%v", spec.label, lastErr)
}

// EnsureYTDLP 返回 yt-dlp 可执行文件路径。
func EnsureYTDLP() (string, error) { return EnsureTool(specYTDLP()) }

// EnsureFFmpeg 返回 ffmpeg 可执行文件路径。
func EnsureFFmpeg() (string, error) { return EnsureTool(specFFmpeg()) }

// EnsureWhisper 返回 whisper-cli 可执行文件路径。
func EnsureWhisper() (string, error) { return EnsureTool(specWhisper()) }

// ToolStates 只在本地查找，不触发下载，用于启动时的状态展示。
func ToolStates() []model.ToolState {
	specs := []toolSpec{specYTDLP(), specFFmpeg(), specWhisper()}
	out := make([]model.ToolState, 0, len(specs))
	for _, s := range specs {
		st := model.ToolState{Key: s.key, Label: s.label}
		for _, dir := range []string{filepath.Join(config.ExeDir, "bin"), config.BinDir} {
			cand := filepath.Join(dir, s.exe)
			if fileExists(cand) {
				st.Ready, st.Path, st.Message = true, cand, "已就绪"
				break
			}
		}
		if !st.Ready {
			if p, err := exec.LookPath(s.exe); err == nil {
				st.Ready, st.Path, st.Message = true, p, "来自系统 PATH"
			}
		}
		if !st.Ready {
			st.Message = "首次生成课程时自动下载"
		}
		out = append(out, st)
	}
	return out
}

func downloadFile(url, dest string, onProgress func(done, total int64)) error {
	tmp := dest + ".part"
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		return os.Rename(tmp, dest)
	case http.StatusOK, http.StatusPartialContent:
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent && have > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		have = 0
	}
	f, err := os.OpenFile(tmp, flags, 0o644)
	if err != nil {
		return err
	}

	total := resp.ContentLength
	if total > 0 {
		total += have
	}
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			have += int64(n)
			if onProgress != nil {
				onProgress(have, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func downloadAndExtract(url, destDir string, pick picker) error {
	tmpZip := filepath.Join(os.TempDir(), fmt.Sprintf("readalong-%d.zip", time.Now().UnixNano()))
	defer os.Remove(tmpZip)

	if err := downloadFile(url, tmpZip, nil); err != nil {
		return err
	}
	zr, err := zip.OpenReader(tmpZip)
	if err != nil {
		return err
	}
	defer zr.Close()

	n := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		out, ok := pick(f.Name)
		if !ok {
			continue
		}
		if err := extractOne(f, filepath.Join(destDir, out)); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("压缩包里没有需要的文件")
	}
	return nil
}

func extractOne(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
