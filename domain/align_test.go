package domain

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"readalong/model"
)

// writeBurstWav 造一个 16k 单声道 16bit 的测试音频：
// 只有 bursts 里有 440Hz 正弦，其余是数字静音，和真实单词跟读音轨一样。
func writeBurstWav(t *testing.T, total float64, bursts []SoundSpan) string {
	t.Helper()
	const rate = 16000
	n := int(total * rate)
	pcm := make([]byte, n*2)
	for _, b := range bursts {
		from := int(b.S * rate)
		to := int(b.E * rate)
		for i := from; i < to && i < n; i++ {
			v := 0.3 * math.Sin(2*math.Pi*440*float64(i)/rate)
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(v*32767)))
		}
	}
	head := make([]byte, 44)
	copy(head[0:4], "RIFF")
	binary.LittleEndian.PutUint32(head[4:], uint32(36+len(pcm)))
	copy(head[8:12], "WAVE")
	copy(head[12:16], "fmt ")
	binary.LittleEndian.PutUint32(head[16:], 16)
	binary.LittleEndian.PutUint16(head[20:], 1)
	binary.LittleEndian.PutUint16(head[22:], 1)
	binary.LittleEndian.PutUint32(head[24:], rate)
	binary.LittleEndian.PutUint32(head[28:], rate*2)
	binary.LittleEndian.PutUint16(head[32:], 2)
	binary.LittleEndian.PutUint16(head[34:], 16)
	copy(head[36:40], "data")
	binary.LittleEndian.PutUint32(head[40:], uint32(len(pcm)))

	path := filepath.Join(t.TempDir(), "word.wav")
	if err := os.WriteFile(path, append(head, pcm...), 0o644); err != nil {
		t.Fatalf("写测试音频失败：%v", err)
	}
	return path
}

// fakeReader 按时间顺序返回预设的「听到的文字」，模拟逐段复听。
func fakeReader(texts []string) (ClipRead, *int) {
	n := 0
	return func(string) (string, error) {
		if n >= len(texts) {
			return "", nil
		}
		t := texts[n]
		n++
		return t, nil
	}, &n
}

// 单词页的经典坏数据：每个词 0.4 秒、按 2 秒等距排开，和发音完全错开。
func gridSegments(words []string, start, step float64) []model.Segment {
	out := make([]model.Segment, len(words))
	for i, w := range words {
		s := start + float64(i)*step
		out[i] = model.Segment{Start: Round2(s), End: Round2(s + 0.4), Text: w, Section: "unit6-单词"}
	}
	return out
}

var sixBursts = []SoundSpan{
	{S: 2.10, E: 2.60},
	{S: 4.25, E: 4.75},
	{S: 6.35, E: 6.95},
	{S: 8.40, E: 8.95},
	{S: 10.55, E: 11.10},
	{S: 12.60, E: 13.25},
}

// 网格时间轴整体漂移时，要靠复听到的内容把每个词放回它自己那段。
func TestAlignWordPageSnapsByContent(t *testing.T) {
	wav := writeBurstWav(t, 15, sixBursts)
	segs := gridSegments([]string{"Red", "Green", "Blue", "Black", "White", "Yellow"}, 1.70, 2.0)
	read, _ := fakeReader([]string{"red", "green", "blue", "black", "white", "yellow"})

	got, ok := AlignWordPage(wav, segs, read)
	if !ok {
		t.Fatal("应该完成校正")
	}
	if len(got) != len(segs) {
		t.Fatalf("词数不该变：%d -> %d", len(segs), len(got))
	}
	for i, b := range sixBursts {
		if math.Abs(got[i].Start-b.S) > 0.05 || math.Abs(got[i].End-b.E) > 0.05 {
			t.Fatalf("第 %d 个词 %q 没落到真实发音上：got %.2f–%.2f 期望 %.2f–%.2f",
				i, got[i].Text, got[i].Start, got[i].End, b.S, b.E)
		}
	}
}

// 位置看起来更像相邻词时，内容优先：标注 "Very" 必须落在听到 very 的那段。
func TestAlignWordPagePrefersContentOverPosition(t *testing.T) {
	wav := writeBurstWav(t, 15, sixBursts)
	// 故意把标注时间往左挪，让「位置」指向上一段
	segs := []model.Segment{
		{Start: 1.20, End: 1.60, Text: "Red"},
		{Start: 3.30, End: 3.70, Text: "Green"},
		{Start: 5.30, End: 5.70, Text: "Blue"},
		{Start: 7.40, End: 7.80, Text: "Black"},
		{Start: 9.50, End: 9.90, Text: "White"},
		{Start: 11.60, End: 12.00, Text: "Yellow"},
	}
	read, _ := fakeReader([]string{"red", "green", "blue", "black", "white", "yellow"})
	got, ok := AlignWordPage(wav, segs, read)
	if !ok {
		t.Fatal("应该完成校正")
	}
	for i, b := range sixBursts {
		if math.Abs(got[i].Start-b.S) > 0.05 {
			t.Fatalf("第 %d 个词 %q 落在 %.2f，期望 %.2f", i, got[i].Text, got[i].Start, b.S)
		}
	}
}

// 识别多出来的词（音频里根本没有）应该被丢掉，而不是硬塞到别人那段上。
func TestAlignWordPageDropsPhantomWords(t *testing.T) {
	bursts := []SoundSpan{
		{S: 1.00, E: 1.60},
		{S: 3.00, E: 3.60},
		{S: 5.00, E: 5.60},
	}
	wav := writeBurstWav(t, 7, bursts)
	segs := gridSegments([]string{"One", "Two", "Three", "One", "Two", "Three"}, 0.80, 2.0)
	read, _ := fakeReader([]string{"one", "two", "three"})

	got, ok := AlignWordPage(wav, segs, read)
	if !ok {
		t.Fatal("应该完成校正")
	}
	if len(got) != 3 {
		t.Fatalf("音频只有 3 个词，多余的应该丢掉，实际剩 %d：%+v", len(got), got)
	}
	for i, b := range bursts {
		if math.Abs(got[i].Start-b.S) > 0.05 {
			t.Fatalf("第 %d 个词 %q 落在 %.2f，期望 %.2f", i, got[i].Text, got[i].Start, b.S)
		}
	}
}

// 一句话连念（"Chinese New Year"）时，几个词共用一段，按字数摊开。
func TestAlignWordPageSplitsSharedSpan(t *testing.T) {
	bursts := []SoundSpan{
		{S: 1.00, E: 1.80},
		{S: 3.00, E: 4.60},
	}
	wav := writeBurstWav(t, 6, bursts)
	segs := []model.Segment{
		{Start: 0.90, End: 1.30, Text: "Hello"},
		{Start: 2.80, End: 4.20, Text: "Chinese New Year"},
	}
	read, _ := fakeReader([]string{"hello", "chinese new year"})

	got, ok := AlignWordPage(wav, segs, read)
	if !ok {
		t.Fatal("应该完成校正")
	}
	if math.Abs(got[1].Start-3.00) > 0.05 || math.Abs(got[1].End-4.60) > 0.05 {
		t.Fatalf("整句应该覆盖那一段发音：%+v", got[1])
	}
}

// 没有复听能力（read == nil）时退回按位置吸附，至少不能把词留在静音里。
func TestAlignWordPageFallsBackToPosition(t *testing.T) {
	wav := writeBurstWav(t, 15, sixBursts)
	segs := gridSegments([]string{"Red", "Green", "Blue", "Black", "White", "Yellow"}, 1.70, 2.0)
	got, ok := AlignWordPage(wav, segs, nil)
	if !ok {
		t.Fatal("应该完成校正")
	}
	for i, b := range sixBursts {
		if math.Abs(got[i].Start-b.S) > 0.05 {
			t.Fatalf("第 %d 个词落在 %.2f，期望 %.2f", i, got[i].Start, b.S)
		}
	}
}

func TestAlignWordPageKeepsOriginalWhenNoSpeech(t *testing.T) {
	wav := writeBurstWav(t, 5, nil) // 全静音
	segs := gridSegments([]string{"Red", "Green", "Blue"}, 0.2, 2.0)
	got, ok := AlignWordPage(wav, segs, nil)
	if ok {
		t.Fatal("全静音没有可落的发声段，不该报告校正成功")
	}
	if got[0].Start != segs[0].Start {
		t.Fatal("没有发声段时应该原样返回")
	}
}

func TestRepairWordTimelineOnlyTouchesWordSections(t *testing.T) {
	wav := writeBurstWav(t, 15, sixBursts)
	words := gridSegments([]string{"Red", "Green", "Blue", "Black", "White", "Yellow"}, 1.70, 2.0)
	text := []model.Segment{
		{Start: 13.9, End: 14.4, Text: "It's red.", Section: "课文跟读unit 6"},
	}
	c := &model.Course{Segments: append(append([]model.Segment{}, words...), text...)}
	read, _ := fakeReader([]string{"red", "green", "blue", "black", "white", "yellow"})

	if n := RepairWordTimeline(c, wav, read); n != 6 {
		t.Fatalf("应该校正 6 个单词段，实际 %d", n)
	}
	if c.Segments[len(c.Segments)-1].Start != 13.9 {
		t.Fatalf("课文段不该被动：%+v", c.Segments[len(c.Segments)-1])
	}
	if math.Abs(c.Segments[0].Start-2.10) > 0.05 {
		t.Fatalf("单词段没修好：%+v", c.Segments[0])
	}
}

func TestTextSim(t *testing.T) {
	cases := []struct {
		want, heard string
		min         float64
	}{
		{"Red", "Red", 1},
		{"Thank", "thank you", 0.8},
		{"Chinese", "chinese new year", 0.8},
		// 复听听错的词不该判成相似（靠位置兜底），否则容易张冠李戴
		{"Chair", "chen", 0},
		{"Black", "blue", 0},
		{"Door", "tool", 0},
		{"Red", "", 0},
	}
	for _, c := range cases {
		if got := textSim(c.want, c.heard); got < c.min {
			t.Fatalf("textSim(%q,%q)=%.2f，至少 %.2f", c.want, c.heard, got, c.min)
		}
	}
	if textSim("Door", "tool") > 0.5 || textSim("Chair", "chen") > 0.5 {
		t.Fatal("听错成别的词时相似度不该高")
	}
	_ = strings.TrimSpace
}
