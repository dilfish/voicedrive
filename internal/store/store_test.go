package store

import "testing"

func TestMin(t *testing.T) {
	tests := []struct {
		name       string
		a, b, want int
	}{{"left", 1, 2, 1}, {"right", 2, 1, 1}, {"equal", 3, 3, 3}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := min(tt.a, tt.b); got != tt.want {
				t.Fatalf("min(%d,%d)=%d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestErrorsAreDistinct(t *testing.T) {
	if ErrNotFound == ErrConflict {
		t.Fatal("not found and conflict must remain distinct")
	}
}
