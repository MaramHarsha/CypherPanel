package version

import "testing"

func TestAgentCompatible(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"dev", true},
		{"", true},
		{"0.1.0", true},
		{"0.2.0", true},
		{"1.0.0", true},
		{"0.0.9", false},   // below MinAgent 0.1.0
		{"v0.1.0", true},   // leading v tolerated
		{"0.1.0-rc1", true}, // pre-release suffix stripped
		{"garbage", true},   // unparseable → allowed with a flag
	}
	for _, c := range cases {
		got, reason := AgentCompatible(c.in)
		if got != c.want {
			t.Errorf("AgentCompatible(%q) = %v (%q), want %v", c.in, got, reason, c.want)
		}
	}
}
