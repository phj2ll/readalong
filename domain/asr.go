package domain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"readalong/config"
)

// RawSegment 是识别引擎给出的原始片段。
type RawSegment struct {
	Start float64
	End   float64
	Text  string
	Words []Word
}

type wxOffsets struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

type wxToken struct {
	Text    string    `json:"text"`
	Offsets wxOffsets `json:"offsets"`
}

type wxSegment struct {
	Offsets wxOffsets `json:"offsets"`
	Text    string    `json:"text"`
	Tokens  []wxToken `json:"tokens"`
}

type wxFile struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []wxSegment `json:"transcription"`
}

var asrProgressRe = regexp.MustCompile(`progress\s*=\s*(\d+)\s*%`)

// Transcribe 调 whisper-cli 出全量 JSON，再按 token 时间戳还原到词级。
// progress 用于把识别进度回报给界面，可为 nil。
func Transcribe(wav, lang, modelName string, progress func(pct int)) ([]RawSegment, string, error) {
	cli, err := EnsureWhisper()
	if err != nil {
		return nil, "", err
	}
	if !ModelReady(modelName) {
		return nil, "", fmt.Errorf("语音模型 %s 还没下载完，等它下完再生成", modelName)
	}

	prefix := strings.TrimSuffix(wav, filepath.Ext(wav)) + "_asr"
	_ = os.Remove(prefix + ".json")

	threads := runtime.NumCPU()
	if threads > 8 {
		threads = 8
	}
	if threads < 2 {
		threads = 2
	}

	cmd := NewCmd(cli,
		"-m", config.ModelPath(modelName),
		"-f", wav,
		"-l", lang,
		"-t", strconv.Itoa(threads),
		"-ojf", "-of", prefix,
		"-pp",
		"-bs", "5",
	)
	cmd.Stdout = io.Discard
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("启动 whisper-cli 失败：%w", err)
	}

	last := -9
	sc := bufio.NewScanner(stderrPipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		m := asrProgressRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		p, perr := strconv.Atoi(m[1])
		if perr != nil || p-last < 5 {
			continue
		}
		last = p
		if progress != nil {
			progress(p)
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, "", fmt.Errorf("whisper-cli 退出异常：%w", err)
	}

	raw, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return nil, "", fmt.Errorf("没读到识别结果：%w", err)
	}
	var parsed wxFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, "", fmt.Errorf("识别结果解析失败：%w", err)
	}
	if len(parsed.Transcription) == 0 {
		return nil, "", fmt.Errorf("没识别到语音，检查视频有没有声音")
	}

	detected := parsed.Result.Language
	if lang != "auto" && lang != "" {
		detected = lang
	}
	if detected == "" {
		detected = lang
	}
	return rawSegmentsFrom(parsed), detected, nil
}

// RecognizeClip 只识别一小段音频并返回文本，不给时间戳。
// 单词页要逐段复听、按「听到的词」给每段落点，用它。
func RecognizeClip(wavPath, lang, modelName string) (string, error) {
	cli, err := EnsureWhisper()
	if err != nil {
		return "", err
	}
	if !ModelReady(modelName) {
		return "", fmt.Errorf("语音模型 %s 还没下载完，等它下完再生成", modelName)
	}
	threads := runtime.NumCPU()
	if threads > 8 {
		threads = 8
	}
	if threads < 2 {
		threads = 2
	}
	cmd := NewCmd(cli,
		"-m", config.ModelPath(modelName),
		"-f", wavPath,
		"-l", lang,
		"-t", strconv.Itoa(threads),
		"-bs", "5",
		"-nt", "-np",
	)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("复听片段失败：%v %s", err, tail(errb.String(), 160))
	}
	return strings.TrimSpace(string(out)), nil
}

// rawSegmentsFrom 把 whisper 原始片段转成内部结构（秒 + 词级时间）。
func rawSegmentsFrom(f wxFile) []RawSegment {
	out := make([]RawSegment, 0, len(f.Transcription))
	for _, s := range f.Transcription {
		out = append(out, RawSegment{
			Start: float64(s.Offsets.From) / 1000,
			End:   float64(s.Offsets.To) / 1000,
			Text:  strings.TrimSpace(s.Text),
			Words: segmentWords(s),
		})
	}
	return out
}

// segmentWords 把 token 序列还原成「词 + 起止时间」。
// whisper 的 token 是 BPE 子词，按每个 token 的时间等分给它包含的字符，
// 再按空白切词，这样中英文都适用。
func segmentWords(s wxSegment) []Word {
	if len(s.Tokens) == 0 {
		return splitPlain(s.Text, float64(s.Offsets.From)/1000, float64(s.Offsets.To)/1000)
	}

	var runes []rune
	var starts, ends []float64
	for _, tk := range s.Tokens {
		// whisper 会夹带 [_BEG_] / [_TT_250] 这类控制 token，必须剔除
		if isSpecialToken(tk.Text) {
			continue
		}
		tr := []rune(tk.Text)
		if len(tr) == 0 {
			continue
		}
		from := float64(tk.Offsets.From) / 1000
		to := float64(tk.Offsets.To) / 1000
		if to < from {
			to = from
		}
		step := (to - from) / float64(len(tr))
		for i, r := range tr {
			runes = append(runes, r)
			starts = append(starts, from+step*float64(i))
			ends = append(ends, from+step*float64(i+1))
		}
	}
	if len(runes) == 0 {
		return splitPlain(s.Text, float64(s.Offsets.From)/1000, float64(s.Offsets.To)/1000)
	}
	return splitRunes(runes, starts, ends)
}

func splitRunes(rs []rune, starts, ends []float64) []Word {
	var out []Word
	mk := func(a, b int) {
		if b <= a {
			return
		}
		w := Word{W: strings.TrimSpace(string(rs[a:b])), S: starts[a], E: ends[b-1]}
		if w.W != "" {
			out = append(out, w)
		}
	}
	i := 0
	for i < len(rs) {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		start := i
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			i++
		}
		piece := start
		for k := start; k < i; k++ {
			if k > piece && isSentenceEnd(rs[k]) {
				mk(piece, k+1)
				piece = k + 1
			}
		}
		mk(piece, i)
	}
	return out
}

func isSentenceEnd(r rune) bool { return strings.ContainsRune("。？！", r) }

// isSpecialToken 判断 whisper 的控制标记，形如 [_BEG_] / [_TT_250]。
func isSpecialToken(t string) bool {
	return strings.HasPrefix(t, "[_") && strings.HasSuffix(t, "]")
}

// splitPlain 在没有 token 时间戳时，按字数比例分摊时间。
func splitPlain(text string, start, end float64) []Word {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	total := 0
	for _, f := range fields {
		total += len([]rune(f)) + 1
	}
	if total == 0 {
		return nil
	}
	cur := start
	out := make([]Word, 0, len(fields))
	for _, f := range fields {
		share := (end - start) * float64(len([]rune(f))) / float64(total)
		out = append(out, Word{W: f, S: cur, E: cur + share})
		cur += share
	}
	return out
}
