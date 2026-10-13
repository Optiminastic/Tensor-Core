package phone

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"9113994682", "919113994682", true},
		{"+91 91139 94682", "919113994682", true},
		{"09113994682", "919113994682", true},
		{"0091-9113994682", "919113994682", true},
		{"919113994682", "919113994682", true},
		{"+1 415 555 0100", "14155550100", true},
		{"12345", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Normalize(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestMatch(t *testing.T) {
	if !Match("919113994682", "+91 91139 94682") {
		t.Error("same number in different formats should match")
	}
	if !Match("919113994682", "09113994682") {
		t.Error("leading zero form should match")
	}
	if Match("919113994682", "919113994683") {
		t.Error("different numbers must not match")
	}
	if Match("919113994682", "") || Match("", "") {
		t.Error("empty numbers must never match")
	}
}

func TestMask(t *testing.T) {
	if got := Mask("919113994682"); got != "+91******4682" {
		t.Errorf("Mask = %q", got)
	}
	if got := Mask("123"); got != "***" {
		t.Errorf("Mask short = %q", got)
	}
}
