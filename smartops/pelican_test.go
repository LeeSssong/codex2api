package smartops

import "testing"

func TestPelicanOutputRequiresActualDocument(t *testing.T) {
	for _, s := range []string{"", "refused", "a prose explanation"} {
		if ValidatePelicanOutput(s) == nil {
			t.Fatalf("accepted invalid document %q", s)
		}
	}
	for _, s := range []string{"<!doctype html><html></html>", "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"} {
		if e := ValidatePelicanOutput(s); e != nil {
			t.Fatal(e)
		}
	}
}
