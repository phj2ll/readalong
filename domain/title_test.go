package domain

import "testing"

// 「·」后面不一定都是系列名：分 P 标题常带「· 单词朗读」「· 6」这种尾巴，
// 只有当得了书名的才留下，否则返回空让上层兜底（课程标题 / BV 号）。
func TestSeriesOfRejectsNoise(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"P21 课文跟读unit 4 · 外研新交际英语一年级上册", "外研新交际英语一年级上册"},
		{"P6 单词跟读 · 6", ""},
		{"P6 单词跟读 · 66", ""},
		{"P6 单词跟读 · · ", ""},
		{"P6 单词跟读 · 单词朗读", "单词朗读"},
		{"没有中点的标题", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := SeriesOf(c.in); got != c.want {
			t.Fatalf("SeriesOf(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 分 P 标题前面带节号时（"6 p22 单词跟读"），CleanTitle 会把「6」当成系列名
// 塞到中点后面，书名就变成「6」。这条链路要保证这种噪声被丢掉。
func TestSeriesOfDropsSectionNumber(t *testing.T) {
	cleaned := CleanTitle("6 p22 单词跟读")
	if cleaned != "P22 单词跟读 · 6" {
		t.Fatalf("CleanTitle 结果变了：%q", cleaned)
	}
	if got := SeriesOf(cleaned); got != "" {
		t.Fatalf("系列名是节号时应当丢弃，实际 %q", got)
	}
}
