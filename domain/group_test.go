package domain

import (
	"strings"
	"testing"
)

// 用 BV1qR4fesEm6 的真实分 P 标题锁定分组规则：
// 6 个 Unit；课文+单词合并进各自 Unit；教学视频课（Let's …）跳过。
func TestGroupPagesRealTitles(t *testing.T) {
	titles := []string{
		"Unit 1-Let's start-activity 2", "Unit 1-Let's read-activity 1", "Unit 1-Let's explore!",
		"Unit 2-Let's start-activity 2", "Unit 2-Let's read-activity 1", "Unit 2-Let's explore!",
		"Unit 3-Let's start-activity 2", "Unit 3-Let's read-activity 1", "Unit 3-Let's explore",
		"Unit 4-Let's start-activity 2", "Unit 4-Let's read-activity 1",
		"Unit 5-Let's start-activity 2", "Unit 5-Let's read-activity 1", "Unit 5-Let's explore",
		"Unit 6-Let's start-activity 2", "Unit 6-Let's read-activity 1", "Unit 6-Let's explore",
		"课文跟读unit 1", "课文跟读unit 2", "课文跟读unit 3",
		"课文跟读unit 4", "课文跟读unit 5", "课文跟读unit 6",
		"unit1-单词", "unit2-单词", "unit3-单词", "unit4-单词", "unit5-单词", "unit6-单词",
	}
	pages := make([]PageInfo, len(titles))
	for i, s := range titles {
		pages[i] = PageInfo{Title: s, Duration: 60}
	}
	groups := GroupPages(pages)
	if len(groups) != 6 {
		t.Fatalf("期望 6 个 Unit，得到 %d：%v", len(groups), groupLabels(groups))
	}
	for _, g := range groups {
		if !strings.HasPrefix(g.Label, "Unit ") {
			t.Fatalf("课程标签应为 Unit N，得到 %q", g.Label)
		}
		if len(g.Pages) != 2 {
			t.Fatalf("%s 应含课文+单词 2 个分 P，得到 %d", g.Label, len(g.Pages))
		}
		for _, gp := range g.Pages {
			if pageVideoRe.MatchString(strings.ToLower(gp.Title)) {
				t.Fatalf("%s 不应包含视频课分 P：%s", g.Label, gp.Title)
			}
		}
		// 课文在前、单词在后
		if !strings.Contains(g.Pages[0].Title, "课文") || !strings.Contains(g.Pages[1].Title, "单词") {
			t.Fatalf("%s 分 P 顺序应为课文、单词，得到：%s / %s", g.Label, g.Pages[0].Title, g.Pages[1].Title)
		}
	}
}

func groupLabels(gs []Group) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Label
	}
	return out
}
