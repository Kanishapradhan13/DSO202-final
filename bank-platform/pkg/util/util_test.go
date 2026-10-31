package util

import "testing"

func TestNu(t *testing.T) {
	for in, want := range map[int64]string{
		0:          "Nu. 0.00",
		5:          "Nu. 0.05",
		123450:     "Nu. 1,234.50",
		100000000:  "Nu. 1,000,000.00",
		-250:       "-Nu. 2.50",
		1234567890: "Nu. 12,345,678.90",
	} {
		if got := Nu(in); got != want {
			t.Errorf("Nu(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestPaging(t *testing.T) {
	limit, offset := Page(0, "")
	if limit != 25 || offset != 0 {
		t.Fatalf("defaults = %d,%d", limit, offset)
	}
	next := NextToken(25, 25, 0)
	if next == "" {
		t.Fatal("expected a next token")
	}
	_, offset = Page(25, next)
	if offset != 25 {
		t.Fatalf("offset = %d, want 25", offset)
	}
	if NextToken(10, 25, 0) != "" {
		t.Fatal("short page should have no next token")
	}
	if l, _ := Page(1000, ""); l != 25 {
		t.Fatalf("oversized page not clamped: %d", l)
	}
}

func TestMaskTail(t *testing.T) {
	if got := MaskTail("12345678901", 4); got != "•••••••8901" {
		t.Fatalf("MaskTail = %q", got)
	}
}

func TestRandomDigits(t *testing.T) {
	s := RandomDigits(12)
	if len(s) != 12 {
		t.Fatalf("len = %d", len(s))
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("non-digit %q", c)
		}
	}
}
