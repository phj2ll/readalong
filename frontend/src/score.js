// 评分统计：把「已评过」的句子汇总成均分，用来在任务行 / Unit 行显示进度。
// 分数本体存在 localStorage 的 ra.score.<courseId> 里，形如 { <句号>: { total, accuracy, fluency } }。
// 只统计 total > 0 的句子（没评过的不算进分母，否则刚开始读会被拉成 0 分）。

// summarizeIndexes 按给定的句号列表统计（任务行用：一个任务可能只是课程里的一段）。
export function summarizeIndexes(scores, indexes) {
  let sum = 0;
  let n = 0;
  for (const i of indexes) {
    const s = scores && scores[i];
    if (s && s.total > 0) {
      sum += s.total;
      n++;
    }
  }
  return { avg: n ? Math.round(sum / n) : 0, n, sum };
}

// summarizeScores 统计整门课。
export function summarizeScores(scores) {
  return summarizeIndexes(scores, Object.keys(scores || {}));
}

// readCourseScores 读一门课的评分表；存储坏了就当没评过。
export function readCourseScores(courseId) {
  try {
    return JSON.parse(localStorage.getItem(`ra.score.${courseId}`) || '{}');
  } catch (e) {
    return {};
  }
}

// scoreGrade 给分档，配色用（和播放页的分数徽标一致）。
export function scoreGrade(t) {
  if (t >= 85) return 'great';
  if (t >= 70) return 'good';
  if (t > 0) return 'weak';
  return '';
}
