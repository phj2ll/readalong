package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"readalong/model"
)

var (
	endPunctRe   = regexp.MustCompile(`[.?!。？！]+$`)
	leadNumRe    = regexp.MustCompile(`^\s*\d+\s*[.、]\s*`)
	isoHeadRe    = regexp.MustCompile(`^\[[^\]]{1,20}\]\s*`)
	dashHeadRe   = regexp.MustCompile(`^[-–—]\s*`)
	spacePunctRe = regexp.MustCompile(`\s+([,.;:?!’'])`)
	hasLetterRe  = regexp.MustCompile(`[A-Za-z\x{4e00}-\x{9fff}]`)
	anyEndRe     = regexp.MustCompile(`[.?!]$`)
	bracketRe    = regexp.MustCompile(`【[^】]*】`)
	partRe       = regexp.MustCompile(`\bp(\d+)\s+(.+)$`)
)

// Word 是最小时间单元，S/E 单位为秒。
type Word struct {
	W string
	S float64
	E float64
}

// CleanText 清掉编号、时间戳、多余空格，并把首字母大写。
func CleanText(text string) string {
	text = leadNumRe.ReplaceAllString(text, "")
	text = isoHeadRe.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	text = spacePunctRe.ReplaceAllString(text, "$1")
	text = dashHeadRe.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)

	r := []rune(text)
	if len(r) > 0 && unicode.IsLetter(r[0]) {
		r[0] = unicode.ToUpper(r[0])
		text = string(r)
	}
	if len(strings.Fields(text)) >= 2 && !anyEndRe.MatchString(text) {
		text += "."
	}
	return text
}

// CleanTitle 把「系列名【标签】 p22 分P标题」整理成「P22 分P标题 · 系列名」。
func CleanTitle(raw string) string {
	t := bracketRe.ReplaceAllString(raw, " ")
	t = strings.Trim(strings.Join(strings.Fields(t), " "), " -_·|")
	if m := partRe.FindStringSubmatch(t); m != nil {
		series := strings.Trim(t[:strings.Index(t, m[0])], " -_·|")
		part := "P" + m[1] + " " + strings.TrimSpace(m[2])
		if series != "" {
			return part + " · " + series
		}
		return part
	}
	return t
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

func leadingPunct(r rune) bool {
	return strings.ContainsRune(`,.;:?!’'"，。；：？！、）】》`, r)
}

// JoinWords 拼词：中文之间不插空格，标点前不插空格。
func JoinWords(ws []Word) string {
	var b strings.Builder
	var prev rune
	for _, w := range ws {
		t := strings.TrimSpace(w.W)
		if t == "" {
			continue
		}
		r := []rune(t)
		if b.Len() > 0 && !isCJK(prev) && !isCJK(r[0]) && !leadingPunct(r[0]) {
			b.WriteByte(' ')
		}
		b.WriteString(t)
		prev = r[len(r)-1]
	}
	return b.String()
}

// wordBreakRe 单词表模式下的断点标点：逗号/分号/顿号就是词边界。
var wordBreakRe = regexp.MustCompile(`[,;，；、]+$`)

// splitChunks 按断点规则把词序列切块。
// wordMode=false（课文）：只在句末标点或 >0.75s 停顿处切（逗号是句子内部，不能切）。
// wordMode=true（单词表）：逗号/顿号也切，停顿阈值降到 0.4s——单词表里
// 老师念词节奏快，词间停顿常常不到 0.75s，只靠停顿会把几个词粘成一条。
func splitChunks(words []Word, wordMode bool) [][]Word {
	var chunks [][]Word
	var cur []Word
	for _, w := range words {
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			gap := w.S - prev.E
			brk := endPunctRe.MatchString(strings.TrimSpace(prev.W)) || gap > 0.75
			if wordMode && !brk {
				brk = wordBreakRe.MatchString(strings.TrimSpace(prev.W)) || gap > 0.4
			}
			if brk {
				chunks = append(chunks, cur)
				cur = nil
			}
		}
		cur = append(cur, w)
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// BuildSegments 把识别片段切成单句，过滤过短或无意义的碎片。
// wordMode=true 时按单词表规则切（逗号即边界、停顿阈值 0.4s、最短时长放宽）。
func BuildSegments(raw []RawSegment, wordMode bool) []model.Segment {
	minDur := 0.45
	if wordMode {
		minDur = 0.3 // 单个短单词也要保留
	}
	var out []model.Segment
	for _, rs := range raw {
		var groups [][]Word
		if len(rs.Words) > 0 {
			groups = splitChunks(rs.Words, wordMode)
		} else {
			groups = [][]Word{{{W: rs.Text, S: rs.Start, E: rs.End}}}
		}
		for _, g := range groups {
			if len(g) == 0 {
				continue
			}
			text := CleanText(strings.TrimRight(JoinWords(g), ",，;；、:："))
			start := Round2(g[0].S)
			end := Round2(g[len(g)-1].E)
			if text == "" || end-start < minDur {
				continue
			}
			if !hasLetterRe.MatchString(text) {
				continue
			}
			if len([]rune(text)) > 220 {
				continue
			}
			out = append(out, model.Segment{Start: start, End: end, Text: text})
		}
	}
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].Start < out[k-1].Start; k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}

// AddSections 按静音间隔分段，超过 gap 秒另起一段。
func AddSections(segs []model.Segment, gap float64) []model.Segment {
	if len(segs) == 0 {
		return segs
	}
	sec := 1
	prevEnd := segs[0].Start
	base := segs[0].Start
	for i := range segs {
		if segs[i].Start-prevEnd > gap {
			sec++
			base = segs[i].Start
		}
		segs[i].Section = fmt.Sprintf("第 %d 段", sec)
		segs[i].SectionStart = Round1(base)
		prevEnd = segs[i].End
	}
	if sec == 1 {
		for i := range segs {
			segs[i].Section = ""
			segs[i].SectionStart = 0
		}
	}
	return segs
}

// Round1 保留一位小数。
func Round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// Round2 保留两位小数。
func Round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

// FmtTS 把秒格式化成 m:ss。
func FmtTS(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
