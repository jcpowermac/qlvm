package cli

import (
	"testing"
)

func TestCoresList(t *testing.T) {
	tests := []struct {
		cores []int
		want  string
	}{
		{nil, "(none)"},
		{[]int{6}, "6"},
		{[]int{6, 7}, "6-7"},
		{[]int{6, 7, 8}, "6-8"},
		{[]int{0, 1}, "0-1"},
		{[]int{0, 1, 6, 7}, "0-1, 6-7"},
		{[]int{6, 8, 10}, "6, 8, 10"},
		{[]int{6, 7, 9, 10}, "6-7, 9-10"},
	}
	for _, tt := range tests {
		got := coresList(tt.cores)
		if got != tt.want {
			t.Errorf("coresList(%v) = %q, want %q", tt.cores, got, tt.want)
		}
	}
}
