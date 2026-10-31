package main

import "testing"

func luhnValid(pan string) bool {
	sum := 0
	double := false
	for i := len(pan) - 1; i >= 0; i-- {
		d := int(pan[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func TestLuhnDigit(t *testing.T) {
	for _, body := range []string{"457123000000001", "457123987654321", "411111111111111", "457123555555555"} {
		pan := body + string(luhnDigit(body))
		if !luhnValid(pan) {
			t.Fatalf("%s is not Luhn-valid", pan)
		}
	}
	// Known test card.
	if got := luhnDigit("411111111111111"); got != '1' {
		t.Fatalf("check digit = %c, want 1", got)
	}
}

func TestHashPANIsPeppered(t *testing.T) {
	a := (&cardServer{pepper: "a"}).hashPAN("4571230000000017")
	b := (&cardServer{pepper: "b"}).hashPAN("4571230000000017")
	if a == b || len(a) != 64 {
		t.Fatalf("pepper not applied: %s %s", a, b)
	}
}
