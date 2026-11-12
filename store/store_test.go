package store

import (
	"os"
	"path/filepath"
	"testing"

	"readalong/config"
)

// 同一个分 P 只能有一个目录：生成过之后再提交同一个链接要覆盖那门课，
// 不能再凭空多出一门 _150512 式的副本（否则练习进度按 id 走会丢）。
func TestCourseIDFromURLReusesExistingCourse(t *testing.T) {
	old := config.CoursesDir
	config.CoursesDir = t.TempDir()
	defer func() { config.CoursesDir = old }()

	const url = "https://www.bilibili.com/video/BV1qR4fesEm6/?p=23"
	base := CourseIDFromURL(url)
	if base != "BV1qR4fesEm6_p23" {
		t.Fatalf("目录名不对：%q", base)
	}

	// 生成完成后（目录里有 data.json）再提交同一个链接，仍然是同一个目录
	dir := filepath.Join(config.CoursesDir, base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CourseIDFromURL(url); got != base {
		t.Fatalf("同一链接应复用 %q，实际 %q", base, got)
	}

	// 只有「生成到一半」的空目录（没有 data.json）才让位给 _2
	half := filepath.Join(config.CoursesDir, "BV1qR4fesEm6_p24")
	if err := os.MkdirAll(half, 0o755); err != nil {
		t.Fatal(err)
	}
	got := CourseIDFromURL("https://www.bilibili.com/video/BV1qR4fesEm6/?p=24")
	if got != "BV1qR4fesEm6_p24_2" {
		t.Fatalf("半截目录应让位给 _2，实际 %q", got)
	}
}
