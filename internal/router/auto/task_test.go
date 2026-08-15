package auto

import (
	"reflect"
	"testing"
)

func TestTaskClassifierBasic(t *testing.T) {
	c := NewTaskClassifier()
	cases := []struct {
		name string
		in   string
		want Task
	}{
		{"code", "write a python function to parse csv", TaskCode},
		{"code2", "修复这个 bug，函数实现有问题", TaskCode},
		{"math", "计算夏普比率和回撤", TaskMath},
		{"math2", "calculate the correlation between factors", TaskMath},
		{"translate", "把这段翻译成英文", TaskTranslate},
		{"data", "导入 csv 到数据库表", TaskData},
		{"deploy", "docker 部署到服务器", TaskDeploy},
		{"writing", "写一份项目报告文档", TaskWriting},
		{"visual", "前端页面布局调整，图表展示", TaskVisual},
		{"understand", "分析一下为什么会报错", TaskUnderstand},
		{"general", "你好", TaskGeneral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Classify(tc.in); got != tc.want {
				t.Errorf("Classify(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestTaskContextText(t *testing.T) {
	got := TaskContextText("继续", []string{"分析一下数据", "好的"}, 2)
	if got != "继续 分析一下数据 好的" {
		t.Errorf("TaskContextText = %q", got)
	}
	// n=0 → only user text
	if got := TaskContextText("继续", []string{"a"}, 0); got != "继续" {
		t.Errorf("TaskContextText n=0 = %q", got)
	}
}

func TestReorderByTask(t *testing.T) {
	candidates := []string{"deepseek-v4-flash", "mimo-v2.5", "MiniMax-M3"}
	bias := map[string][]string{
		"math": {"MiniMax-M3", "mimo-v2.5-pro"},
	}
	// math: MiniMax-M3 moves to front (mimo-v2.5-pro not in candidates)
	out, applied := ReorderByTask(candidates, "math", bias)
	if !applied {
		t.Fatal("expected applied=true")
	}
	want := []string{"MiniMax-M3", "deepseek-v4-flash", "mimo-v2.5"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("ReorderByTask = %v, want %v", out, want)
	}

	// unknown task → unchanged
	out2, applied2 := ReorderByTask(candidates, "unknown", bias)
	if applied2 || !reflect.DeepEqual(out2, candidates) {
		t.Errorf("unknown task: applied=%v out=%v", applied2, out2)
	}

	// general → unchanged
	_, applied3 := ReorderByTask(candidates, "general", bias)
	if applied3 {
		t.Errorf("general should not apply bias")
	}
}

func TestMoveToFront(t *testing.T) {
	candidates := []string{"MiniMax-M3", "mimo-v2.5", "deepseek-v4-flash"}
	out, moved := MoveToFront(candidates, "deepseek-v4-flash")
	if !moved {
		t.Fatal("expected moved=true")
	}
	want := []string{"deepseek-v4-flash", "MiniMax-M3", "mimo-v2.5"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("MoveToFront = %v, want %v", out, want)
	}

	// already first → no move
	if _, moved = MoveToFront(out, "deepseek-v4-flash"); moved {
		t.Error("already-first should not move")
	}
	// unknown → no move
	if _, moved = MoveToFront(candidates, "nope"); moved {
		t.Error("unknown model should not move")
	}
}
