package auto

import (
	"strings"
)

// Task is a coarse task-type category derived from the request and
// its recent context. Unlike the complexity tier (how hard), the
// task says WHAT the user is doing — used to apply per-task model
// preferences (task_model_bias) learned from the judge pipeline
// (docs/EVALUATION-METHODOLOGY.md).
type Task string

// The nine task categories. Unknown/short context-dependent
// prompts fall into TaskGeneral and behave exactly like v1.
const (
	TaskCode       Task = "code"
	TaskUnderstand Task = "understand"
	TaskWriting    Task = "writing"
	TaskMath       Task = "math"
	TaskTranslate  Task = "translate"
	TaskData       Task = "data"
	TaskDeploy     Task = "deploy"
	TaskVisual     Task = "visual"
	TaskGeneral    Task = "general"
)

// AllTasks lists every known task.
var AllTasks = []Task{
	TaskCode, TaskUnderstand, TaskWriting, TaskMath, TaskTranslate,
	TaskData, TaskDeploy, TaskVisual, TaskGeneral,
}

// ValidTask reports whether t is a known task.
func ValidTask(t string) bool {
	for _, k := range AllTasks {
		if string(k) == t {
			return true
		}
	}
	return false
}

// TaskClassifier identifies the task type of a request. Input is
// the last user text plus recent context (task type is often
// decided by the surrounding conversation in agentic sessions).
type TaskClassifier interface {
	Classify(text string) Task
}

// taskRule is one (task, keywords, weight) rule. Higher weight =
// stronger signal. Keywords are matched on lowercased space-
// separated tokens; phrases as plain substring matches.
type taskRule struct {
	task   Task
	words  map[string]bool
	phrases []string
	weight int
}

func taskRuleSet(task Task, weight int, words []string, phrases []string) taskRule {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return taskRule{task: task, words: m, phrases: phrases, weight: weight}
}

// taskRules hold the Chinese/English keyword signals mirrored from
// scripts/learned-router/classify_samples.py. English words are
// token-matched (lowerTokens); CJK terms must go in phrases because
// lowerTokens only extracts [a-z0-9]+ tokens.
var taskRules = []taskRule{
	taskRuleSet(TaskCode, 3, []string{
		"function", "class", "def", "import", "api", "endpoint", "script",
		"refactor", "bug", "test", "git", "commit", "merge", "compile",
		"traceback", "error",
	}, []string{"代码", "函数", "接口", "实现", "编写", "修复", "单元测试",
		"分支", "编译", "报错"}),
	taskRuleSet(TaskUnderstand, 2, []string{
		"explain", "debug", "why", "error", "review", "evaluate",
	}, []string{"traceback", "分析", "排查", "解释", "为什么", "检查",
		"梳理", "逻辑", "理解", "评估", "调研", "调查", "原因"}),
	taskRuleSet(TaskWriting, 2, []string{
		"draft", "essay", "blog", "document", "report", "readme",
		"changelog",
	}, []string{"文档", "报告", "总结", "说明", "写入", "记录", "注释", "备注"}),
	taskRuleSet(TaskMath, 2, []string{
		"calculate", "equation", "solve", "correlation", "quant",
		"alpha", "beta",
	}, []string{"统计", "计算", "公式", "回测", "概率", "因子", "回归",
		"风险", "波动", "收益率", "标准差", "夏普", "回撤", "收益", "净值"}),
	taskRuleSet(TaskTranslate, 3, []string{
		"translate", "localize",
	}, []string{"翻译"}),
	taskRuleSet(TaskData, 2, []string{
		"data", "csv", "excel", "sql", "query", "pandas", "dataframe",
		"json",
	}, []string{"数据库", "表", "字段", "导入", "导出", "清洗", "列", "行"}),
	taskRuleSet(TaskDeploy, 2, []string{
		"deploy", "docker", "nginx", "server", "install", "build",
		"restart",
	}, []string{"配置", "部署", "启动", "运行", "环境", "依赖", "重启",
		"进程", "服务", "端口"}),
	taskRuleSet(TaskVisual, 2, []string{
		"plot", "chart", "dashboard", "ui", "css", "html",
	}, []string{"图表", "图", "前端", "页面", "样式", "表格", "展示", "布局"}),
}

// heuristicTaskClassifier is the zero-dependency keyword classifier.
type heuristicTaskClassifier struct{}

// NewTaskClassifier returns the heuristic task classifier.
func NewTaskClassifier() TaskClassifier { return heuristicTaskClassifier{} }

// Classify scores the input (prompt + recent context) against the
// rules and returns the highest-scoring task. Ties go to the
// earlier rule; no hits → general.
func (heuristicTaskClassifier) Classify(text string) Task {
	low := strings.ToLower(text)
	_, toks := lowerTokens(low)

	best := TaskGeneral
	bestScore := 0
	for _, r := range taskRules {
		hits := countWords(toks, r.words, 3)
		for _, p := range r.phrases {
			if strings.Contains(low, p) {
				hits++
			}
		}
		if hits > 3 {
			hits = 3
		}
		score := hits * r.weight
		if score > bestScore {
			best = r.task
			bestScore = score
		}
	}
	return best
}

// TaskContextText joins the last user text with the tail of the
// conversation (last n user/assistant/tool messages) so task
// classification sees the surrounding context, mirroring
// classify_samples.py's _recent_context_text.
func TaskContextText(userText string, contextMsgs []string, n int) string {
	if n <= 0 {
		return userText
	}
	parts := make([]string, 0, n+1)
	parts = append(parts, userText)
	start := len(contextMsgs) - n
	if start < 0 {
		start = 0
	}
	for _, m := range contextMsgs[start:] {
		if s := strings.TrimSpace(m); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}
