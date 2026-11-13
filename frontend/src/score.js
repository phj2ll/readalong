// 评分统计。
//
// 口径：100 分制，分母是「全部题目」，没评过的题按 0 分算。
// 例：10 个单词只读了 2 个（两个都 100 分）→ 20 分，不是 100 分。
// 这样任务行/书架的分数天然就是「完成度」，读了多少一眼能看出来。
//
// 分数本体存在 localStorage 的 ra.score.<courseId>，形如 { <句号>: { total, accuracy, fluency } }，
// 每句只保留历史最好成绩。

// scoreFor 按给定的句号列表统计；indexes 传 null 时按分数表里的键统计，
// 但那种情况下必须显式给 total（分数表只知道评过的句子，不知道总题数）。
export function scoreFor(scores, indexes, total) {
  const keys = indexes || Object.keys(scores || {});
  let sum = 0;
  let n = 0;
  for (const i of keys) {
    const s = scores && scores[i];
    if (s && s.total > 0) {
      sum += s.total;
      n++;
    }
  }
  const t = total == null ? keys.length : total;
  return { score: t > 0 ? sum / t : 0, sum, n, total: t };
}

// formatScore 去掉多余的 0：20 → "20"，86.666… → "86.67"
export function formatScore(v) {
  return String(Math.round((Number(v) || 0) * 100) / 100);
}

// scoreGrade 配色分档（≥85 绿、60~84 蓝、其余灰，低分不刺眼）
export function scoreGrade(v) {
  if (v >= 85) return 'great';
  if (v >= 60) return 'good';
  return 'low';
}

// readCourseScores 读一门课的评分表；存储坏了就当没评过。
export function readCourseScores(courseId) {
  try {
    return JSON.parse(localStorage.getItem(`ra.score.${courseId}`) || '{}');
  } catch (e) {
    return {};
  }
}
