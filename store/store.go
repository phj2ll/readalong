// Package store 负责课程数据的读写，一切以文件为准，不引入数据库。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"readalong/config"
	"readalong/model"
)

var (
	bvRe   = regexp.MustCompile(`BV([0-9A-Za-z]{10})`)
	idRe   = regexp.MustCompile(`^[0-9A-Za-z_.-]+$`)
	slugRe = regexp.MustCompile(`[^0-9A-Za-z_.-]`)
)

// CourseIDFromURL 生成稳定的课程目录名，同一个分 P 命中同一个目录。
func CourseIDFromURL(rawURL string) string {
	bv := "v"
	if m := bvRe.FindStringSubmatch(rawURL); m != nil {
		bv = "BV" + m[1]
	}
	p := ""
	if i := strings.Index(rawURL, "?"); i >= 0 {
		q := rawURL[i+1:]
		if j := strings.Index(q, "#"); j >= 0 {
			q = q[:j]
		}
		for _, kv := range strings.Split(q, "&") {
			if strings.HasPrefix(kv, "p=") {
				p = strings.TrimPrefix(kv, "p=")
			}
		}
	}
	base := bv
	if p != "" {
		base = bv + "_p" + p
	}
	base = slugRe.ReplaceAllString(base, "")

	cid := base
	for i := 2; DirExists(filepath.Join(config.CoursesDir, cid)) &&
		!FileExists(filepath.Join(config.CoursesDir, cid, "data.json")); i++ {
		cid = base + "_" + strconv.Itoa(i)
	}
	if FileExists(filepath.Join(config.CoursesDir, cid, "data.json")) {
		cid = base + "_" + time.Now().Format("150405")
	}
	return cid
}

// ValidID 校验课程 id，防目录穿越。
func ValidID(id string) bool { return idRe.MatchString(id) }

// Save 把课程写到磁盘：audio.wav + data.json + meta.json。
func Save(c model.Course, wavPath string) error {
	dir := filepath.Join(config.CoursesDir, c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	audio := filepath.Join(dir, "audio.wav")
	if FileExists(audio) {
		_ = os.Remove(audio)
	}
	if err := moveFile(wavPath, audio); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "data.json"), c); err != nil {
		return err
	}
	meta := model.CourseSummary{
		ID:       c.ID,
		Title:    c.Title,
		Source:   c.Source,
		Uploader: c.Uploader,
		Duration: c.Duration,
		Count:    len(c.Segments),
		Created:  c.Created,
	}
	return writeJSON(filepath.Join(dir, "meta.json"), meta)
}

// List 按创建时间倒序返回全部课程摘要。
func List() []model.CourseSummary {
	items := []model.CourseSummary{}
	entries, err := os.ReadDir(config.CoursesDir)
	if err != nil {
		return items
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(config.CoursesDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m model.CourseSummary
		if json.Unmarshal(raw, &m) == nil && m.ID != "" {
			items = append(items, m)
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Created > items[b].Created })
	return items
}

// Load 读取一门课程。
func Load(id string) (model.Course, error) {
	var c model.Course
	raw, err := os.ReadFile(filepath.Join(config.CoursesDir, id, "data.json"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}

// Delete 删除课程目录。
func Delete(id string) error {
	return os.RemoveAll(filepath.Join(config.CoursesDir, id))
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// FileExists 判断路径是否是可读文件。
func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// DirExists 判断目录是否存在。
func DirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
