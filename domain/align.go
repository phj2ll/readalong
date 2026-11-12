package domain

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"readalong/config"
	"readalong/model"
)

// WordAlignVersion 是单词页时间轴校正的算法版本：算法改进了就加一，
// 老课程启动时会按这个版本重做一次（见 app.go 的 migrateWordTimeline）。
const WordAlignVersion = 1

// 单词页对齐参数。音频统一是 16k 单声道 16bit（toWav 保证）。
const (
	alignFrameDur = 0.02 // 能量帧长（秒）
	alignSpanGap  = 0.25 // 相邻发声段间隔小于它就算同一段（词内停顿）
	alignSpanMin  = 0.12 // 短于它的发声段当噪声丢掉
	alignSpanMax  = 8.0  // 长于它的发声段（音乐、连续说话）不参与吸附
	alignClipPad  = 0.06 // 复听时前后各留一点，别把词头词尾削掉
)

// SoundSpan 是音频里一段连续的发声区间，单位秒。
type SoundSpan struct {
	S float64
	E float64
}

// ClipRead 复听一小段音频、只返回听到的文字（真实实现是 whisper）。可为 nil。
type ClipRead func(wavPath string) (string, error)

// alignWord 是「一个词」：来自某个句段，带着按字数摊出来的原始时间。
type alignWord struct {
	Text  string
	Label int
	S     float64
	E     float64
}

// AlignWordPage 用音频重建一个「单词页」的时间轴。
//
// 单词跟读音频是「念一个词、停一会儿」的节奏，whisper 对这种音频给的词级
// 时间戳不可靠：要么退化成每词 0.4 秒、按 2 秒等距排开的假网格，要么整体
// 漂移一两秒（漂到后面直接指向别的词，甚至指向静音）。位置怎么算都救不回来，
// 所以这里改走「内容对齐」：
//
//  1. 按能量取出发声段（一个词一段）
//  2. 逐段复听，得到每段真正念的是什么（read 为 nil 时跳过，只按位置）
//  3. 词和发声段按「文字相似度 + 位置」做单调 DP 配对
//  4. 音频里确实没有的词（识别多出来的）直接丢掉
//
// 返回重建后的段落和是否改动过；读不出音频或找不到发声段时原样返回。
func AlignWordPage(wavPath string, segs []model.Segment, read ClipRead) ([]model.Segment, bool) {
	if len(segs) < 2 {
		return segs, false
	}
	samples, rate, err := wavSamples(wavPath)
	if err != nil || len(samples) == 0 {
		return segs, false
	}
	first, last := segs[0].Start, segs[len(segs)-1].End
	if last <= first {
		return segs, false
	}

	// 识别给的时间轴可能整体偏后，最后一个词的发音也会落在段尾之后。
	spans := speechSpans(samples, rate, first-2.0, last+4.0)
	cand := make([]SoundSpan, 0, len(spans))
	for _, sp := range spans {
		if sp.E < first-2.5 || sp.S > last+4.5 {
			continue
		}
		if sp.E-sp.S > alignSpanMax {
			continue
		}
		cand = append(cand, sp)
	}
	if len(cand) == 0 {
		return segs, false
	}

	texts := recognizeSpans(samples, rate, cand, read)

	words := expandWords(segs)
	spot := matchWords(words, cand, texts)

	out := rebuildSegments(segs, words, cand, spot)
	// 复听质量太差时别把数据改坏：配对成功的词不到一半就放弃
	if len(out) < len(segs)/2 {
		return segs, false
	}
	return out, true
}

// RepairWordTimeline 把课程里所有单词页按音频重建一遍，
// 顺带丢掉识别多出来、音频里根本没有的词。返回改动的段数。
func RepairWordTimeline(c *model.Course, wavPath string, read ClipRead) int {
	out := make([]model.Segment, 0, len(c.Segments))
	changed := 0
	for i := 0; i < len(c.Segments); {
		j := i
		for j < len(c.Segments) && c.Segments[j].Section == c.Segments[i].Section {
			j++
		}
		group := c.Segments[i:j]
		if pageKind(c.Segments[i].Section) == "word" {
			if fixed, ok := AlignWordPage(wavPath, group, read); ok && len(fixed) > 0 {
				if len(fixed) != len(group) || !sameTimes(fixed, group) {
					changed += len(group)
				}
				out = append(out, fixed...)
				i = j
				continue
			}
		}
		out = append(out, group...)
		i = j
	}
	if changed > 0 {
		c.Segments = out
	}
	return changed
}

func sameTimes(a, b []model.Segment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i].Start-b[i].Start) > 0.001 || math.Abs(a[i].End-b[i].End) > 0.001 {
			return false
		}
	}
	return true
}

// recognizeSpans 把每段发声切出来单独复听，返回听到的文字（失败的位置留空）。
func recognizeSpans(samples []int16, rate int, spans []SoundSpan, read ClipRead) []string {
	out := make([]string, len(spans))
	if read == nil {
		return out
	}
	base := config.TmpDir
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return out
	}
	dir, err := os.MkdirTemp(base, "align-")
	if err != nil {
		return out
	}
	defer os.RemoveAll(dir)

	for i, sp := range spans {
		p := filepath.Join(dir, fmt.Sprintf("c%03d.wav", i))
		if err := writeClip(p, samples, rate, sp); err != nil {
			continue
		}
		if txt, rerr := read(p); rerr == nil {
			out[i] = txt
		}
	}
	return out
}

// writeClip 把 [sp.S-pad, sp.E+pad] 写成 16k 单声道 16bit 的 wav。
func writeClip(path string, samples []int16, rate int, sp SoundSpan) error {
	from := int(math.Max(0, (sp.S-alignClipPad)*float64(rate)))
	to := int(math.Min(float64(len(samples)), (sp.E+alignClipPad)*float64(rate)))
	if to <= from {
		return fmt.Errorf("片段为空")
	}
	pcm := make([]byte, (to-from)*2)
	for i := from; i < to; i++ {
		binary.LittleEndian.PutUint16(pcm[(i-from)*2:], uint16(samples[i]))
	}
	head := make([]byte, 44)
	copy(head[0:4], "RIFF")
	binary.LittleEndian.PutUint32(head[4:], uint32(36+len(pcm)))
	copy(head[8:12], "WAVE")
	copy(head[12:16], "fmt ")
	binary.LittleEndian.PutUint32(head[16:], 16)
	binary.LittleEndian.PutUint16(head[20:], 1)
	binary.LittleEndian.PutUint16(head[22:], 1)
	binary.LittleEndian.PutUint32(head[24:], uint32(rate))
	binary.LittleEndian.PutUint32(head[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(head[32:], 2)
	binary.LittleEndian.PutUint16(head[34:], 16)
	copy(head[36:40], "data")
	binary.LittleEndian.PutUint32(head[40:], uint32(len(pcm)))
	return os.WriteFile(path, append(head, pcm...), 0o644)
}

// expandWords 把每个句段按空白拆成词，时间按字数摊开。
func expandWords(segs []model.Segment) []alignWord {
	var out []alignWord
	for i, s := range segs {
		fields := strings.Fields(s.Text)
		if len(fields) == 0 {
			continue
		}
		total := 0
		for _, f := range fields {
			total += len([]rune(f)) + 1
		}
		t := s.Start
		for _, f := range fields {
			share := (s.End - s.Start) * float64(len([]rune(f))+1) / float64(total)
			out = append(out, alignWord{Text: f, Label: i, S: t, E: t + share})
			t += share
		}
	}
	return out
}

// matchWords 把词和发声段配对：单调（都按念的顺序）、一个段可以被多个词共用、
// 允许跳过词（音频里没有）和跳过段（噪声）。有复听文字时以文字为主、位置为辅，
// 没有时只按位置。
func matchWords(words []alignWord, spans []SoundSpan, texts []string) []int {
	const (
		maxDist = 3.0 // 中心和中心差这么多就不考虑配对
	)
	withText := false
	for _, t := range texts {
		if strings.TrimSpace(t) != "" {
			withText = true
			break
		}
	}
	// 丢掉一个词（音频里没有）vs 塞到别人的发声段上：共用一个发声段要额外付钱，
	// 而且只有复听到的内容确实是这个词（如 "Chinese New Year" 里的 new）才允许共用。
	const (
		sharePen = 0.9  // 共用一段的额外代价
		skipSpan = 0.4  // 跳过一段噪声的代价
		simShare = 0.55 // 低于这个相似度就不许共用（说明那段念的是别的词）
	)
	skipWord := 4.0
	if !withText {
		skipWord = 8.0 // 没有复听信息时不要丢词，按位置尽量都放上去
	}

	n, m := len(words), len(spans)
	spot := make([]int, n)
	for i := range spot {
		spot[i] = -1
	}
	if n == 0 || m == 0 {
		return spot
	}

	cost := make([][]float64, n)
	sims := make([][]float64, n)
	for i := range cost {
		cost[i] = make([]float64, m)
		sims[i] = make([]float64, m)
		for k := range cost[i] {
			cost[i][k] = math.Inf(1)
		}
	}
	for i, w := range words {
		for k := range spans {
			d := math.Abs((spans[k].S+spans[k].E)/2 - (w.S+w.E)/2)
			sim := textSim(w.Text, texts[k])
			sims[i][k] = sim
			if !withText {
				if d > maxDist {
					continue
				}
				cost[i][k] = d
				continue
			}
			// 又远又不像是同一个词，直接不考虑；只满足一条时仍可配（位置或内容救人）
			if d > maxDist && sim < 0.4 {
				continue
			}
			// 文字权重高于位置：识别给的词级时间轴本来就在漂，
			// 「内容完全对得上但远一点」要赢过「位置近但内容完全不同」。
			cost[i][k] = 3.0*(1-sim) + 0.5*d
		}
	}

	// A[i][k]：处理完前 i+1 个词、第 i 个词落在第 k 段
	// B[i][k]：处理完前 i+1 个词、第 i 个词被跳过，最后落点还是第 k 段（k==m 表示还没落点）
	const none = -1
	A := make([][]float64, n)
	B := make([][]float64, n)
	bpA := make([][]int, n) // kp*2=来自 A(i-1,kp)，kp*2+1=来自 B(i-1,kp)
	bpB := make([][]int, n) // 0=来自 A(i-1,k)，1=来自 B(i-1,k)
	for i := 0; i < n; i++ {
		A[i] = make([]float64, m)
		B[i] = make([]float64, m+1)
		bpA[i] = make([]int, m)
		bpB[i] = make([]int, m+1)
		for k := 0; k < m; k++ {
			A[i][k] = math.Inf(1)
			bpA[i][k] = none
		}
		for k := 0; k <= m; k++ {
			B[i][k] = math.Inf(1)
			bpB[i][k] = none
		}
	}

	for k := 0; k < m; k++ {
		A[0][k] = cost[0][k]
	}
	B[0][m] = skipWord

	for i := 1; i < n; i++ {
		for k := 0; k < m; k++ {
			if math.IsInf(cost[i][k], 1) {
				continue
			}
			best, from := math.Inf(1), none
			for kp := 0; kp <= m; kp++ {
				// 上一个词要么落在第 kp 段，要么被跳过（此时落点仍是 kp；kp==m 表示还没有落点）
				cands := []struct {
					v   float64
					enc int
				}{}
				if kp < m {
					cands = append(cands, struct {
						v   float64
						enc int
					}{A[i-1][kp], kp * 2})
				}
				cands = append(cands, struct {
					v   float64
					enc int
				}{B[i-1][kp], kp*2 + 1})
				for _, c := range cands {
					if math.IsInf(c.v, 1) {
						continue
					}
					if kp < m && kp > k {
						continue // 顺序不能倒
					}
					add := 0.0
					if kp < m {
						add = skipSpan * float64(k-kp-1) // 中间被跳过的发声段
						if kp == k {
							// 共用一段：只有那一段确实含这个词才允许，
							// 否则就是识别多出来的重复词，不许占别人的位置。
							if withText && sims[i][k] < simShare {
								continue
							}
							// 一段只念了一个词，却要塞进来两个词，那多半是识别重复了
							// （如单词表里出现两次 "Black"），宁可丢掉不要挤。
							if withText && len(strings.Fields(normText(texts[k]))) < 2 {
								continue
							}
							// 后面还剩足够的发声段就别挤在一起（挤了会让后面的词整体错位）
							if n-1-i <= m-1-k {
								add += 3.0
							} else {
								add += sharePen
							}
						}
					}
					if v := c.v + add; v < best {
						best, from = v, c.enc
					}
				}
			}
			if from != none {
				A[i][k] = best + cost[i][k]
				bpA[i][k] = from
			}
		}
		for k := 0; k <= m; k++ {
			best, from := math.Inf(1), none
			if k < m && !math.IsInf(A[i-1][k], 1) {
				best, from = A[i-1][k], 0
			}
			if !math.IsInf(B[i-1][k], 1) && B[i-1][k] < best {
				best, from = B[i-1][k], 1
			}
			if from != none {
				B[i][k] = best + skipWord
				bpB[i][k] = from
			}
		}
	}

	// 收尾：整体代价最小的状态
	best, k, skipped := math.Inf(1), none, false
	for kk := 0; kk < m; kk++ {
		if A[n-1][kk] < best {
			best, k, skipped = A[n-1][kk], kk, false
		}
	}
	for kk := 0; kk <= m; kk++ {
		if B[n-1][kk] < best {
			best, k, skipped = B[n-1][kk], kk, true
		}
	}
	if k == none {
		return spot
	}

	for i := n - 1; i >= 0; {
		if skipped {
			spot[i] = -1
			enc := bpB[i][k]
			i--
			if enc < 0 {
				break
			}
			skipped = enc == 1
			continue
		}
		spot[i] = k
		enc := bpA[i][k]
		i--
		if enc < 0 {
			break
		}
		k = enc / 2
		skipped = enc%2 == 1
	}
	return spot
}

// rebuildSegments 按配对结果重建段落：时间取发声段的边界，
// 音频里没有的词（没配上）直接去掉。
func rebuildSegments(segs []model.Segment, words []alignWord, spans []SoundSpan, spot []int) []model.Segment {
	type acc struct {
		s, e float64
		got  bool
	}
	hits := make([]acc, len(segs))
	for i, w := range words {
		k := spot[i]
		if k < 0 || k >= len(spans) {
			continue
		}
		sp := spans[k]
		a := &hits[w.Label]
		if !a.got || sp.S < a.s {
			a.s = sp.S
		}
		if !a.got || sp.E > a.e {
			a.e = sp.E
		}
		a.got = true
	}

	out := make([]model.Segment, 0, len(segs))
	for i, s := range segs {
		if !hits[i].got {
			continue
		}
		ns := s
		ns.Start = hits[i].s
		ns.End = hits[i].e
		out = append(out, ns)
	}
	if len(out) == 0 {
		return nil
	}

	// 几个词共用一段时，按字数把这一段分开
	for i := 0; i < len(out); {
		j := i + 1
		for j < len(out) && math.Abs(out[j].Start-out[i].Start) < 0.001 && math.Abs(out[j].End-out[i].End) < 0.001 {
			j++
		}
		if j-i > 1 {
			spS, spE := out[i].Start, out[i].End
			total := 0
			for p := i; p < j; p++ {
				total += textWeight(out[p].Text)
			}
			t := spS
			for p := i; p < j; p++ {
				share := (spE - spS) * float64(textWeight(out[p].Text)) / float64(total)
				out[p].Start = t
				out[p].End = t + share
				t += share
			}
		}
		i = j
	}

	prevEnd := 0.0
	for i := range out {
		if out[i].Start < prevEnd {
			out[i].Start = prevEnd
		}
		out[i].Start = Round2(out[i].Start)
		out[i].End = Round2(out[i].End)
		if out[i].End < out[i].Start+0.12 {
			out[i].End = Round2(out[i].Start + 0.12)
		}
		prevEnd = out[i].End
	}
	return out
}

// textSim 粗略比较「标注词」和「复听到的文字」有多像（0~1）。
func textSim(want, heard string) float64 {
	a := normText(want)
	b := normText(heard)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	for _, tok := range strings.Fields(b) {
		if tok == a {
			return 0.85
		}
	}
	if len(a) >= 2 && strings.HasPrefix(b, a) {
		return 0.7
	}
	if len(b) >= 2 && strings.HasPrefix(a, b) {
		return 0.7
	}
	if r := levRatio(a, b); r >= 0.5 {
		return r
	}
	return 0
}

func normText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '\'':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func levRatio(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	max := len(ar)
	if len(br) > max {
		max = len(br)
	}
	if max == 0 {
		return 1
	}
	return 1 - float64(levDistance(ar, br))/float64(max)
}

func levDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := prev[j-1]
			if a[i-1] != b[j-1] {
				c++
			}
			if v := prev[j] + 1; v < c {
				c = v
			}
			if v := cur[j-1] + 1; v < c {
				c = v
			}
			cur[j] = c
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// speechSpans 在 [from,to] 里找发声段：20ms 一帧算能量，
// 用这段音频自己的噪声底和峰值定阈值，再合并近邻、丢掉噪声。
func speechSpans(samples []int16, rate int, from, to float64) []SoundSpan {
	levels := frameLevels(samples, rate)
	if len(levels) == 0 {
		return nil
	}
	f0 := int(math.Max(0, math.Floor(from/alignFrameDur)))
	f1 := int(math.Min(float64(len(levels)), math.Ceil(to/alignFrameDur)))
	if f1-f0 < 5 {
		return nil
	}

	window := append([]float64(nil), levels[f0:f1]...)
	sort.Float64s(window)
	floor := percentile(window, 0.15)
	peak := percentile(window, 0.99)
	if peak-floor < 10 {
		return nil // 一路都是同一音量（纯音乐/底噪），分不出语音
	}
	thr := floor + 0.4*(peak-floor)
	if thr > -30 {
		thr = -30
	}
	if thr < floor+3 {
		thr = floor + 3
	}

	var runs []SoundSpan
	start := -1
	for f := f0; f < f1; f++ {
		loud := levels[f] > thr
		if loud && start < 0 {
			start = f
		}
		if !loud && start >= 0 {
			runs = append(runs, SoundSpan{S: float64(start) * alignFrameDur, E: float64(f) * alignFrameDur})
			start = -1
		}
	}
	if start >= 0 {
		runs = append(runs, SoundSpan{S: float64(start) * alignFrameDur, E: float64(f1) * alignFrameDur})
	}

	merged := make([]SoundSpan, 0, len(runs))
	for _, r := range runs {
		if n := len(merged); n > 0 && r.S-merged[n-1].E < alignSpanGap {
			merged[n-1].E = r.E
			continue
		}
		merged = append(merged, r)
	}
	out := merged[:0]
	for _, r := range merged {
		if r.E-r.S >= alignSpanMin {
			out = append(out, r)
		}
	}
	return out
}

// frameLevels 把样本切成 20ms 帧，算每帧的 dBFS。
func frameLevels(samples []int16, rate int) []float64 {
	size := int(math.Round(alignFrameDur * float64(rate)))
	if size <= 0 {
		size = 320
	}
	n := len(samples) / size
	out := make([]float64, n)
	for f := 0; f < n; f++ {
		base := f * size
		sum := 0.0
		for k := 0; k < size; k++ {
			v := float64(samples[base+k]) / 32768
			sum += v * v
		}
		rms := math.Sqrt(sum / float64(size))
		if rms < 1e-6 {
			out[f] = -96 // 数字静音
			continue
		}
		out[f] = 20 * math.Log10(rms)
	}
	return out
}

// wavSamples 读 16bit PCM 的 wav，返回单声道样本和采样率。
func wavSamples(path string) ([]int16, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("不是合法的 wav：%s", path)
	}

	var channels, rate, bits int
	var data []byte
	for pos := 12; pos+8 <= len(raw); {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if size > len(raw)-body {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
				rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
				bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
			}
		case "data":
			data = raw[body : body+size]
		}
		pos = body + size + size%2
	}
	if bits != 16 || channels <= 0 || rate <= 0 || len(data) < 2 {
		return nil, 0, fmt.Errorf("只支持 16bit PCM 的 wav：%s", path)
	}

	total := len(data) / 2
	out := make([]int16, 0, total/channels)
	for i := 0; i+channels <= total; i += channels {
		if channels == 1 {
			out = append(out, int16(binary.LittleEndian.Uint16(data[i*2:])))
			continue
		}
		sum := 0
		for k := 0; k < channels; k++ {
			sum += int(int16(binary.LittleEndian.Uint16(data[(i+k)*2:])))
		}
		out = append(out, int16(sum/channels))
	}
	return out, rate, nil
}

// textWeight 用字母/数字个数估算一个词该分到多少时间。
func textWeight(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	if n == 0 {
		n = 1
	}
	return n
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
