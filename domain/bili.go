package domain

// B 站直连实现：yt-dlp 抓网页容易被 412 风控，这里改用官方接口。
// 只用公开的匿名接口：分 P 列表、视频详情、WBI 签名的 playurl。

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"readalong/config"
)

// biliHTTP 带 Cookie Jar：先访问主页拿 buvid3 等风控 Cookie，接口请求自动携带。
// 裸请求（无 Cookie）会被 B 站判成爬虫，返回 412 或风控页。
var (
	biliHTTP   = &http.Client{Timeout: 90 * time.Second}
	cookieOnce sync.Once
)

func warmCookies() {
	cookieOnce.Do(func() {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return
		}
		biliHTTP.Jar = jar
		req, err := http.NewRequest("GET", "https://www.bilibili.com/", nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", biliUA)
		resp, err := biliHTTP.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	})
}

// bilibiliBV 从任意链接里取 BV 号（不带 p 参数）。
func bilibiliBV(rawURL string) string {
	if m := bvOnlyRe.FindStringSubmatch(rawURL); m != nil {
		return "BV" + m[1]
	}
	return ""
}

// biliView 是视频详情（x/web-interface/view）里我们关心的字段。
type biliView struct {
	Title    string `json:"title"`
	Duration float64 `json:"duration"`
	Owner    struct {
		Name string `json:"name"`
	} `json:"owner"`
	Pages []struct {
		Cid      int64   `json:"cid"`
		Page     int     `json:"page"`
		Part     string  `json:"part"`
		Duration float64 `json:"duration"`
	} `json:"pages"`
}

var (
	viewCache   sync.Map // bv -> *biliView
	viewCacheMu sync.Mutex
)

// biliGet 带 UA/Referer 的 GET，B 站接口不做这两个头会返回 412。
// 412/403 是风控限流，退避后重试几次基本都能过。
func biliGet(rawURL, referer string) ([]byte, error) {
	warmCookies()
	if referer == "" {
		referer = "https://www.bilibili.com/"
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(2*attempt) * time.Second)
		}
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", biliUA)
		req.Header.Set("Referer", referer)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		resp, err := biliHTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 && rerr == nil {
			return body, nil
		}
		if rerr != nil {
			lastErr = rerr
		} else {
			lastErr = fmt.Errorf("B 站接口返回 HTTP %d", resp.StatusCode)
		}
	}
	return nil, lastErr
}

// biliViewOf 取视频详情，按 BV 号缓存（一次生成只请求一次）。
func biliViewOf(bv string) (*biliView, error) {
	if v, ok := viewCache.Load(bv); ok {
		return v.(*biliView), nil
	}
	viewCacheMu.Lock()
	defer viewCacheMu.Unlock()
	if v, ok := viewCache.Load(bv); ok {
		return v.(*biliView), nil
	}
	body, err := biliGet("https://api.bilibili.com/x/web-interface/view?bvid="+bv,
		"https://www.bilibili.com/video/"+bv+"/")
	if err != nil {
		return nil, err
	}
	var r struct {
		Code int      `json:"code"`
		Msg  string   `json:"message"`
		Data biliView `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析视频详情失败：%w", err)
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("B 站接口错误（%d）：%s", r.Code, r.Msg)
	}
	viewCache.Store(bv, &r.Data)
	return &r.Data, nil
}

// pageOfURL 解析链接里的 p 参数（第几个分 P），默认 1。
func pageOfURL(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 1
	}
	if p := u.Query().Get("p"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// biliPage 返回指定分 P 的详情（标题、时长、cid）。
func biliPage(rawURL string) (bv string, page int, v *biliView, err error) {
	bv = bilibiliBV(rawURL)
	if bv == "" {
		return "", 0, nil, fmt.Errorf("链接里没有 BV 号")
	}
	v, err = biliViewOf(bv)
	if err != nil {
		return "", 0, nil, err
	}
	page = pageOfURL(rawURL)
	if page > len(v.Pages) {
		page = len(v.Pages)
	}
	if page < 1 {
		page = 1
	}
	return bv, page, v, nil
}

// ---- WBI 签名 ----

// wbiMixinKeyTab 是 B 站 WBI 混淆表（公开算法）。
var wbiMixinKeyTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}

var (
	wbiKeyOnce sync.Once
	wbiImgKey  string
	wbiSubKey  string
	wbiKeyErr  error
)

// wbiKeys 从 nav 接口拿 img_key/sub_key（未登录也能拿到）。
func wbiKeys() (string, string, error) {
	wbiKeyOnce.Do(func() {
		body, err := biliGet("https://api.bilibili.com/x/web-interface/nav", "")
		if err != nil {
			wbiKeyErr = err
			return
		}
		var r struct {
			Code int `json:"code"`
			Data struct {
				WbiImg struct {
					ImgURL string `json:"img_url"`
					SubURL string `json:"sub_url"`
				} `json:"wbi_img"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			wbiKeyErr = err
			return
		}
		base := func(p string) string {
			s := filepath.Base(p)
			return strings.TrimSuffix(s, filepath.Ext(s))
		}
		wbiImgKey = base(r.Data.WbiImg.ImgURL)
		wbiSubKey = base(r.Data.WbiImg.SubURL)
		if wbiImgKey == "" || wbiSubKey == "" {
			wbiKeyErr = fmt.Errorf("拿不到 WBI 密钥")
		}
	})
	return wbiImgKey, wbiSubKey, wbiKeyErr
}

// wbiSign 给参数加上 wts 与 w_rid。
func wbiSign(params map[string]string) (string, error) {
	img, sub, err := wbiKeys()
	if err != nil {
		return "", err
	}
	mixin := img + sub
	var key strings.Builder
	for _, i := range wbiMixinKeyTab {
		if i < len(mixin) {
			key.WriteByte(mixin[i])
		}
	}
	mk := key.String()
	if len(mk) > 32 {
		mk = mk[:32]
	}

	params["wts"] = strconv.FormatInt(time.Now().Unix(), 10)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var q strings.Builder
	for i, k := range keys {
		if i > 0 {
			q.WriteByte('&')
		}
		v := strings.Map(func(r rune) rune {
			if strings.ContainsRune("!'()*", r) {
				return -1
			}
			return r
		}, params[k])
		q.WriteString(url.QueryEscape(k) + "=" + url.QueryEscape(v))
	}
	sum := md5.Sum([]byte(q.String() + mk))
	return q.String() + "&w_rid=" + hex.EncodeToString(sum[:]), nil
}

// biliPlayurl 调 playurl 接口（WBI 签名），返回候选地址列表：
// 每档流的 baseUrl + backupUrl。B 站 CDN 会随机对单节点返回 567（风控/限流），
// 必须准备多个地址轮换重试。
func biliPlayurl(bv string, cid int64) (audioURLs, videoURLs []string, err error) {
	params := map[string]string{
		"bvid":     bv,
		"cid":      strconv.FormatInt(cid, 10),
		"fnval":    "16",
		"fnver":    "0",
		"fourk":    "1",
		"platform": "pc",
	}
	q, err := wbiSign(params)
	if err != nil {
		return nil, nil, err
	}
	body, err := biliGet("https://api.bilibili.com/x/player/wbi/playurl?"+q,
		"https://www.bilibili.com/video/"+bv+"/")
	if err != nil {
		return nil, nil, err
	}
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data struct {
			Quality int `json:"quality"` // durl 模式的清晰度
			Dash    struct {
				Audio []struct {
					BaseURL string   `json:"baseUrl"`
					Backup  []string `json:"backupUrl"`
				} `json:"audio"`
				Video []struct {
					ID      int      `json:"id"` // 清晰度代号：116/80=1080p 64=720p 32=480p 16=360p
					BaseURL string   `json:"baseUrl"`
					Backup  []string `json:"backupUrl"`
				} `json:"video"`
			} `json:"dash"`
			Durl []struct {
				URL string `json:"url"`
			} `json:"durl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, nil, fmt.Errorf("解析流地址失败：%w", err)
	}
	if r.Code != 0 {
		return nil, nil, fmt.Errorf("取流地址失败（%d）：%s", r.Code, r.Msg)
	}
	candidates := func(base string, backup []string) []string {
		var out []string
		if base != "" {
			out = append(out, base)
		}
		for _, b := range backup {
			if b != "" {
				out = append(out, b)
			}
		}
		return out
	}
	// 音频：取码率最高的一档（识别更稳）
	if len(r.Data.Dash.Audio) > 0 {
		a := r.Data.Dash.Audio[len(r.Data.Dash.Audio)-1]
		audioURLs = candidates(a.BaseURL, a.Backup)
	}
	// 视频：选 ≤480p 里清晰度最高的一档（教学动画，480p 够用且省体积）
	best := 0
	for _, v := range r.Data.Dash.Video {
		if v.ID > best && v.ID <= 32 {
			best = v.ID
			videoURLs = candidates(v.BaseURL, v.Backup)
		}
	}
	if len(videoURLs) == 0 && len(r.Data.Dash.Video) > 0 {
		v := r.Data.Dash.Video[0] // 只有高清晰度时兜底取第一个
		videoURLs = candidates(v.BaseURL, v.Backup)
	}
	if len(videoURLs) == 0 && len(r.Data.Durl) > 0 {
		videoURLs = []string{r.Data.Durl[0].URL} // 非 DASH 兜底（音视频合一）
	}
	return audioURLs, videoURLs, nil
}

// biliAudioURL 取分 P 的音频流地址（DASH 音轨优先）。
func biliAudioURL(bv string, cid int64) (string, error) {
	audio, _, err := biliPlayurl(bv, cid)
	if err != nil {
		return "", err
	}
	if len(audio) == 0 {
		return "", fmt.Errorf("没有可用的音频流")
	}
	return audio[0], nil
}

// biliDownload 下载分 P 音频到 work 目录，返回文件路径。
func biliDownload(rawURL, work string, p Progress) (string, error) {
	bv, page, v, err := biliPage(rawURL)
	if err != nil {
		return "", err
	}
	cid := v.Pages[page-1].Cid
	audioURLs, _, err := biliPlayurl(bv, cid)
	if err != nil {
		return "", err
	}
	if len(audioURLs) == 0 {
		return "", fmt.Errorf("没有可用的音频流")
	}
	warmCookies()
	resp, err := fetchStream(audioURLs, "https://www.bilibili.com/video/"+bv+"/")
	if err != nil {
		return "", fmt.Errorf("下载音频失败：%w", err)
	}
	defer resp.Body.Close()

	dst := filepath.Join(work, "src.m4s")
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()

	total := resp.ContentLength
	written := int64(0)
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", werr
			}
			written += int64(n)
			if total > 0 {
				p.Step(fmt.Sprintf("下载音轨 %d%%", int(written*100/total)), 8+int(written*25/total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", fmt.Errorf("下载中断：%w", rerr)
		}
	}
	if written == 0 {
		return "", fmt.Errorf("下载到的音频为空")
	}
	p.Log(fmt.Sprintf("音轨：%s %.1fMB", filepath.Base(dst), float64(written)/1048576))
	return dst, nil
}

// fetchStream 依次尝试候选地址（baseUrl + backupUrl），
// 每个地址失败就换下一个，整轮失败等 0.6s/1.2s/1.8s 后重试，共 3 轮。
// B 站 CDN 偶发 HTTP 567（风控/单节点故障），多地址重试能覆盖绝大多数情况。
func fetchStream(urls []string, referer string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		for _, u := range urls {
			if u == "" {
				continue
			}
			req, err := http.NewRequest("GET", u, nil)
			if err != nil {
				lastErr = err
				continue
			}
			req.Header.Set("User-Agent", biliUA)
			req.Header.Set("Referer", referer)
			resp, err := biliHTTP.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode == 200 {
				return resp, nil
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if attempt < 2 {
			time.Sleep(time.Duration(600*(attempt+1)) * time.Millisecond)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的下载地址")
	}
	return nil, lastErr
}

// fetchToFile 把流下载到文件（带 Referer，B 站 CDN 必需）。
func fetchToFile(urls []string, referer, dst string) (int64, error) {
	resp, err := fetchStream(urls, referer)
	if err != nil {
		return 0, fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return n, err
	}
	return n, nil
}

// SaveVideo 下载教学视频课分 P：视频流+音频流合成 mp4（480p，离线播放）。
func SaveVideo(rawURL string, cid int64, dst string) error {
	bv, _, _, err := biliPage(rawURL)
	if err != nil {
		return err
	}
	if cid == 0 {
		return fmt.Errorf("分 P 的 cid 为空")
	}
	audioURLs, videoURLs, err := biliPlayurl(bv, cid)
	if err != nil {
		return err
	}
	if len(videoURLs) == 0 {
		return fmt.Errorf("没有可用的视频流")
	}

	// 目标课程目录此刻可能还没创建（生成流程是先下视频、后存课程），先兜底建好
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("无法创建课程目录：%w", err)
	}
	work := filepath.Join(config.TmpDir, "video")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	vf := filepath.Join(work, fmt.Sprintf("v-%d.m4s", time.Now().UnixNano()))
	af := filepath.Join(work, fmt.Sprintf("a-%d.m4s", time.Now().UnixNano()))
	defer os.Remove(vf)
	defer os.Remove(af)

	referer := "https://www.bilibili.com/video/" + bv + "/"
	if _, err := fetchToFile(videoURLs, referer, vf); err != nil {
		return fmt.Errorf("下载视频流失败：%w", err)
	}
	hasAudio := len(audioURLs) > 0
	if hasAudio {
		if _, err := fetchToFile(audioURLs, referer, af); err != nil {
			return fmt.Errorf("下载音频流失败：%w", err)
		}
	}

	// m4s 流 remux 成 mp4（不重编码，很快）
	ff, err := EnsureFFmpeg()
	if err != nil {
		return err
	}
	args := []string{"-y", "-i", vf}
	if hasAudio {
		args = append(args, "-i", af)
	}
	args = append(args, "-c", "copy", "-movflags", "+faststart", dst)
	cmd := NewCmd(ff, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("合成视频失败：%v %s", err, tail(errb.String(), 200))
	}
	st, serr := os.Stat(dst)
	if serr != nil || st.Size() == 0 {
		return fmt.Errorf("合成后没有生成视频文件")
	}
	return nil
}
