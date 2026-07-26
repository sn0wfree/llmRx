package admin

import (
	"errors"
	"testing"
)

func TestWithSectionErr_NoError(t *testing.T) {
	items := []int{1, 2, 3}
	s := withSectionErr(items, 100, nil)
	if s.Count != 3 {
		t.Errorf("count=%d want 3", s.Count)
	}
	if s.Error != nil {
		t.Errorf("error should be nil")
	}
}

func TestWithSectionErr_WithError(t *testing.T) {
	items := []int{1, 2, 3}
	s := withSectionErr(items, 100, errors.New("db down"))
	if s.Count != 0 {
		t.Errorf("count=%d want 0 on error", s.Count)
	}
	if s.Error == nil || *s.Error != "db down" {
		t.Errorf("error should be 'db down'")
	}
}

func TestWithSectionErr_Truncation(t *testing.T) {
	items := make([]int, 1500)
	for i := range items {
		items[i] = i
	}
	s := withSectionErr(items, 1000, nil)
	if s.Count != 1000 {
		t.Errorf("count=%d want 1000 (truncated)", s.Count)
	}
	if s.Error == nil {
		t.Errorf("error should be set on truncation")
	}
	if len(s.Items.([]int)) != 1000 {
		t.Errorf("items len=%d want 1000", len(s.Items.([]int)))
	}
}

func TestWithSectionErr_ExactCap(t *testing.T) {
	items := make([]int, 1000)
	s := withSectionErr(items, 1000, nil)
	if s.Count != 1000 {
		t.Errorf("count=%d want 1000", s.Count)
	}
	if s.Error != nil {
		t.Errorf("error should be nil at exact cap")
	}
}
