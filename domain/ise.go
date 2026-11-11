package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"readalong/config"
	"readalong/model"

	"github.com/gorilla/websocket"
)

// 科大讯飞语音评测（ISE）流式版 v2 客户端。
// 文档：https://www.xfyun.cn/doc/Ise/IseAPI.html
// 协议：wss 鉴权（HMAC-SHA256）→ ssb 帧传参考文本 → auw 帧流式传 16k PCM →
// 返回 XML 评分。录音是 MediaRecorder 的 webm/opus，先用 ffmpeg 转成裸 PCM。

const iseHost = "ise-api.xfyun.cn"
const isePath = "/v2/open-ise"

// ConvertToPCM 把前端录音（webm/opus 等）转成 16k 单声道裸 PCM。
func ConvertToPCM(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("录音是空的")
	}
	ff, err := EnsureFFmpeg()
	if err != nil {
		return nil, err
	}
	work := filepath.Join(config.TmpDir, "assess")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	in := filepath.Join(work, fmt.Sprintf("in-%d.webm", time.Now().UnixNano()))
	out := in + ".pcm"
	defer os.Remove(in)
	defer os.Remove(out)

	if err := os.WriteFile(in, src, 0o644); err != nil {
		return nil, err
	}
	cmd := NewCmd(ff, "-y", "-i", in, "-f", "s16le", "-ac", "1", "-ar", "16000", out)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("录音转码失败：%v %s", err, tail(errb.String(), 200))
	}
	pcm, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("录音转码后没有数据")
	}
	return pcm, nil
}

// ---------------- WebSocket 帧结构 ----------------

type iseCommon struct {
	AppID string `json:"app_id"`
}

// 讯飞校验器对 JSON 键顺序敏感（错误码 10163 的真实原因之一），
// 所以 ssb 帧和 auw 帧各用一个结构体，字段顺序严格复刻官方 demo 的输出。

// iseSSBBiz 参数上传帧（第一帧），顺序：aue, auf, category, cmd, ent, sub, text, tte, ttp_skip, rstcd, group
type iseSSBBiz struct {
	AUE      string `json:"aue"`
	AUF      string `json:"auf"`
	Category string `json:"category"`
	Cmd      string `json:"cmd"`
	Ent      string `json:"ent"`
	Sub      string `json:"sub"`
	Text     string `json:"text"` // utf8 BOM + 参考文本
	TTE      string `json:"tte"`
	TPPSkip  bool   `json:"ttp_skip"`
	Rstcd    string `json:"rstcd"`
	Group    string `json:"group"`
}

// auw 帧顺序：cmd, aus, aue
type iseAUWBiz struct {
	Cmd string `json:"cmd"`
	Aus int    `json:"aus"`
	AUE string `json:"aue"`
}

type iseReqData struct {
	Status int    `json:"status"` // 0 首帧 / 1 中间 / 2 结束
	Audio  string `json:"data,omitempty"`
}

type iseReq struct {
	Common   *iseCommon  `json:"common,omitempty"`
	Business interface{} `json:"business,omitempty"`
	Data     *iseReqData `json:"data,omitempty"`
}

type iseResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Status int    `json:"status"`
		Data   string `json:"data"` // base64 XML 评分
	} `json:"data"`
}

// ---------------- 评分解析 ----------------

var (
	totalScoreRe     = regexp.MustCompile(`total_score="([0-9.]+)"`)
	accuracyScoreRe  = regexp.MustCompile(`accuracy_score="([0-9.]+)"`)
	fluencyScoreRe   = regexp.MustCompile(`fluency_score="([0-9.]+)"`)
	integrityScoreRe = regexp.MustCompile(`integrity_score="([0-9.]+)"`)
)

func parseScore(xml, attr string) float64 {
	re := totalScoreRe
	switch attr {
	case "accuracy":
		re = accuracyScoreRe
	case "fluency":
		re = fluencyScoreRe
	case "integrity":
		re = integrityScoreRe
	}
	m := re.FindStringSubmatch(xml)
	if m == nil {
		return 0
	}
	var v float64
	fmt.Sscanf(m[1], "%g", &v)
	return normIseScore(v)
}

// normIseScore 把讯飞的 0–5 分制折算成百分制（讯飞各项满分是 5.000000，
// 而前端按 85/70 分档展示，不折算会显示成「4 分」）。
func normIseScore(v float64) float64 {
	if v > 0 && v <= 5 {
		return v * 20
	}
	return v
}

// ---------------- 主流程 ----------------

// Assess 调讯飞评测接口，返回评分。
func Assess(pcm []byte, text, lang string, st model.Settings) (model.AssessResult, error) {
	var zero model.AssessResult
	if st.AppID == "" || st.APIKey == "" || st.APISecret == "" {
		return zero, fmt.Errorf("讯飞评测凭据未配置")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return zero, fmt.Errorf("参考文本为空")
	}
	if len(pcm) == 0 {
		return zero, fmt.Errorf("音频为空")
	}

	conn, err := iseDial(st)
	if err != nil {
		return zero, fmt.Errorf("连接讯飞评测失败：%w", err)
	}
	defer conn.Close()
	// 写截止时间只在这里设一次的话，音频按实时速率送（1280B/40ms），
	// 超过约 30 秒的录音后面每一帧都会 i/o timeout；所以每帧写之前重新计时。
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	// 第 1 帧：ssb，参考文本放 business（utf8 BOM 头 + 明文），data 只带 status=0
	ent, category := iseEntCategory(text, lang)
	if err := conn.WriteJSON(iseReq{
		Common: &iseCommon{AppID: st.AppID},
		Business: &iseSSBBiz{
			AUE: "raw", AUF: "audio/L16;rate=16000",
			Category: category, Cmd: "ssb",
			Ent: ent, Sub: "ise",
		// 试题文本必须带节点标记（官方文档格式），否则引擎拿不到试题内容，
		// 送音频时报「SRecWrite error/iSEInputAppend error, ret=8195」。
		// 英文单词题用 [word]、句子/篇章用 [content]，中文按纯文本，详见 iseRefText。
		Text: iseRefText(text, ent, category),
			TTE: "utf-8", TPPSkip: true,
			Rstcd: "utf8", Group: "pupil",
		},
		Data: &iseReqData{Status: 0},
	}); err != nil {
		return zero, fmt.Errorf("发送文本失败：%w", err)
	}

	// 第 2..n 帧：auw，流式送音频。16k/16bit/单声道 = 32000 字节/秒，
	// 必须按实时速率送：每帧 1280 字节（正好 40ms 音频）+ 40ms 间隔。
	// 之前一帧塞 8 帧的量、还是 40ms 间隔，8 倍速灌入会让讯飞内部
	// 录音缓冲溢出，报「SRecWrite error/iSEInputAppend error, ret=8195」。
	const frameSize = 1280
	for off, first := 0, true; off < len(pcm); off += frameSize {
		end := off + frameSize
		if end > len(pcm) {
			end = len(pcm)
		}
		aus := 2
		if first {
			aus = 1
			first = false
		}
		// 每帧重新计写超时：整段音频按实时速率发送，长录音要几十秒
		_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err := conn.WriteJSON(iseReq{
			Business: &iseAUWBiz{Cmd: "auw", Aus: aus, AUE: "raw"},
			Data: &iseReqData{
				Status: 1,
				Audio:  base64.StdEncoding.EncodeToString(pcm[off:end]),
			},
		}); err != nil {
			return zero, fmt.Errorf("发送音频失败：%w", err)
		}
		time.Sleep(40 * time.Millisecond)
	}

	// 最后一帧：结束（data 传空）
	if err := conn.WriteJSON(iseReq{
		Business: &iseAUWBiz{Cmd: "auw", Aus: 4, AUE: "raw"},
		Data:     &iseReqData{Status: 2},
	}); err != nil {
		return zero, fmt.Errorf("结束音频失败：%w", err)
	}

	// 音频送完才开始等结果，重新计读超时（长录音的上传本身就要几十秒）
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	// 收结果，直到 status=2 或报错
	xml := ""
	for {
		var resp iseResp
		if err := conn.ReadJSON(&resp); err != nil {
			return zero, fmt.Errorf("读取评测结果失败：%w", err)
		}
		if resp.Code != 0 {
			return zero, fmt.Errorf("讯飞评测出错（%d）：%s", resp.Code, resp.Message)
		}
		if resp.Data != nil && resp.Data.Data != "" {
			if b, err := base64.StdEncoding.DecodeString(resp.Data.Data); err == nil {
				xml = string(b)
			}
		}
		if resp.Data != nil && resp.Data.Status == 2 {
			break
		}
	}
	if xml == "" {
		return zero, fmt.Errorf("讯飞评测没有返回结果")
	}
	if totalScoreRe.FindString(xml) == "" {
		return zero, fmt.Errorf("录音太短或没有识别到朗读内容")
	}
	// 单词题（read_word）的 XML 只有 total/accuracy，缺的项用总分兜底，
	// 否则界面会显示「流利 0」。
	total := parseScore(xml, "total")
	accuracy := parseScore(xml, "accuracy")
	fluency := parseScore(xml, "fluency")
	if accuracy == 0 {
		accuracy = total
	}
	if fluency == 0 {
		fluency = total
	}
	return model.AssessResult{
		Total:     total,
		Accuracy:  accuracy,
		Fluency:   fluency,
		Integrity: parseScore(xml, "integrity"),
	}, nil
}

// iseDial 生成带鉴权参数的 wss 地址并建立连接。
func iseDial(st model.Settings) (*websocket.Conn, error) {
	// 讯飞要求 GMT 格式日期；Go 的 RFC1123 在 UTC 下会输出 "UTC" 而不是 "GMT"，导致鉴权失败
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	// 官方文档：签名原文第三行固定为 "GET /v2/open-ise HTTP/1.1"（不是 POST！）
	origin := fmt.Sprintf("host: %s\ndate: %s\nGET %s HTTP/1.1", iseHost, date, isePath)
	mac := hmac.New(sha256.New, []byte(st.APISecret))
	mac.Write([]byte(origin))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(
		`api_key="%s", algorithm="hmac-sha256", headers="host date request-line", signature="%s"`,
		st.APIKey, sig)))

	u := fmt.Sprintf("wss://%s%s?authorization=%s&date=%s&host=%s",
		iseHost, isePath, url.QueryEscape(auth), url.QueryEscape(date), iseHost)
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := d.Dial(u, nil)
	if err != nil && resp != nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, string(body))
	}
	return conn, err
}

// iseEntCategory 按语言和文本形态选引擎与评测类型。
func iseEntCategory(text, lang string) (ent, category string) {
	if strings.TrimSpace(lang) == "zh" {
		ent = "cn_vip"
	} else {
		ent = "en_vip"
	}
	words := 0
	inWord := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			words++
			inWord = true
		}
	}
	if words <= 1 && len([]rune(text)) < 20 {
		category = "read_word"
	} else {
		category = "read_sentence"
	}
	return ent, category
}

// iseRefText 生成试题文本：一律加 UTF-8 BOM 头；英文还必须带节点标记
// （单词 [word]、句子/篇章 [content]），中文按纯文本。
// 标记错了讯飞报 48195「实例评测试卷未设置，试题格式错误」，
// 而表面现象是 seeRec.SRecWrite error: iSEInputAppend error, ret=8195。
func iseRefText(text, ent, category string) string {
	marker := ""
	if ent == "en_vip" {
		if category == "read_word" {
			marker = "[word]"
		} else {
			marker = "[content]"
		}
	}
	return "\uFEFF" + marker + text
}
