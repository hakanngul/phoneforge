package update

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.4.2", "0.4.1", 1},
		{"0.4.1", "0.4.2", -1},
		{"v0.4.2", "0.4.2", 0},
		{"0.10.0", "0.9.0", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.1.0-dev", "0.1.0", -1},
		{"0.1.0", "0.1.0-dev", 1},
		{"0.1.0-dev", "0.1.0-dev", 0},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0+build5", "1.0.0", 0},
		{"garbage", "0.0.1", -1},
		{"0.0.1", "garbage", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	for _, v := range []string{"0.1.0-dev", "v1.2.3", "1.2.3-beta.1"} {
		if !ValidVersion(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"", "1.2", "1.2.3.4", "01.2.3", "1.2.x", "1.2.3-", "1.2.3-beta_1", "1.2.3-be ta", "1.2.3-a/b", "1.2.3+bu!ld", "1.2.3+", "1.2.3-a..b", "1.2.3-é"} {
		if ValidVersion(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}
