package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func loadSample(t *testing.T) wxFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/whisper_sample.json")
	if err != nil {
		t.Fatalf("读取样例失败：%v", err)
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var f wxFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析样例失败：%v", err)
	}
	if len(f.Transcription) == 0 {
		t.Fatal("样例里没有 transcription")
	}
	return f
}

// whisper 的 [_BEG_] / [_TT_250] 混进文本是最容易出的错，这里钉住。
func TestSegmentWordsStripsSpecialTokens(t *testing.T) {
	f := loadSample(t)
	for i, s := range f.Transcription {
		for _, w := range segmentWords(s) {
			if isSpecialToken(w.W) {
				t.Fatalf("第 %d 段：控制 token 泄漏到文本 %q", i, w.W)
			}
			if strings.TrimSpace(w.W) == "" {
				t.Fatalf("第 %d 段：出现空词", i)
			}
			if w.E < w.S {
				t.Fatalf("第 %d 段：时间倒挂 %+v", i, w)
			}
		}
	}
}

func TestSegmentWordsKeepsText(t *testing.T) {
	f := loadSample(t)
	got := JoinWords(segmentWords(f.Transcription[0]))
	want := strings.TrimSpace(f.Transcription[0].Text)
	if !strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
		t.Fatalf("文本对不上：got %q want %q", got, want)
	}
}

func TestBuildSegmentsOnRealOutput(t *testing.T) {
	f := loadSample(t)
	segs := BuildSegments(rawSegmentsFrom(f), false)
	if len(segs) == 0 {
		t.Fatal("切句结果为空")
	}
	prev := -1.0
	for _, s := range segs {
		if s.End <= s.Start {
			t.Fatalf("时间段非法：%+v", s)
		}
		if s.Start < prev {
			t.Fatalf("时间没有递增：%+v 在 %.2f 之后", s, prev)
		}
		if !hasLetterRe.MatchString(s.Text) {
			t.Fatalf("句子没有字母：%q", s.Text)
		}
		if strings.Contains(s.Text, "[_") {
			t.Fatalf("句子里混入控制标记：%q", s.Text)
		}
		prev = s.Start
	}
	t.Logf("切成 %d 句，首句：%s [%.2f-%.2f]", len(segs), segs[0].Text, segs[0].Start, segs[0].End)
}

func TestAddSections(t *testing.T) {
	segs := BuildSegments(rawSegmentsFrom(loadSample(t)), false)
	out := AddSections(segs, 3.5)
	if len(out) == 0 {
		t.Fatal("分段失败")
	}
	// 只有一段时不应输出分段标题
	sections := map[string]struct{}{}
	for _, s := range out {
		sections[s.Section] = struct{}{}
	}
	if len(sections) == 1 {
		if _, ok := sections[""]; !ok {
			t.Fatalf("单段时应清空 section，实际 %v", sections)
		}
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  blackboard  ", "Blackboard"},
		{"1. Door", "Door"},
		{"Open the door!", "Open the door!"},
		{"this is my classroom", "This is my classroom."},
		{"[00:03] hello there", "Hello there."},
		{"What's this ?", "What's this?"},
		{"- Window", "Window"},
	}
	for _, c := range cases {
		if got := CleanText(c.in); got != c.want {
			t.Errorf("CleanText(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestCleanTitle(t *testing.T) {
	in := "外研新交际英语一年级上册【课本+课文动画+课文跟读+单词跟读】 p22 课文跟读unit 5"
	want := "P22 课文跟读unit 5 · 外研新交际英语一年级上册"
	if got := CleanTitle(in); got != want {
		t.Fatalf("CleanTitle = %q, 期望 %q", got, want)
	}
}

func TestJoinWords(t *testing.T) {
	if got := JoinWords([]Word{{W: "你"}, {W: "好"}, {W: "。"}}); got != "你好。" {
		t.Fatalf("中文拼词 = %q", got)
	}
	if got := JoinWords([]Word{{W: "This"}, {W: "is"}, {W: "the"}, {W: "door"}, {W: "."}}); got != "This is the door." {
		t.Fatalf("英文拼词 = %q", got)
	}
}

func TestSplitChunks(t *testing.T) {
	ws := []Word{
		{W: "Open"}, {W: "the"}, {W: "door"}, {W: "."},
		{W: "This"}, {W: "is"}, {W: "my"}, {W: "classroom"}, {W: "."},
	}
	if chunks := splitChunks(ws, false); len(chunks) != 2 {
		t.Fatalf("课文模式应切成 2 句，实际 %d 句", len(chunks))
	}
	// 单词模式：逗号就是边界，短停顿也切
	words := []Word{
		{W: "Hello,"}, {W: "nice,"}, {W: "meet,"}, {W: "you."},
	}
	if chunks := splitChunks(words, true); len(chunks) != 4 {
		t.Fatalf("单词模式应切成 4 个词，实际 %d 句", len(chunks))
	}
	// 单词模式但停顿大（>0.75s）也切
	words2 := []Word{
		{W: "Let's", S: 0, E: 0.5}, {W: "us.", S: 1.4, E: 1.9},
	}
	if chunks := splitChunks(words2, true); len(chunks) != 2 {
		t.Fatalf("单词模式长停顿应切，实际 %d 句", len(chunks))
	}
	// 课文模式逗号不切
	words3 := []Word{{W: "apple,"}, {W: "banana."}}
	if chunks := splitChunks(words3, false); len(chunks) != 1 {
		t.Fatalf("课文模式逗号不应切，实际 %d 句", len(chunks))
	}
}
