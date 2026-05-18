package telemetry

import "testing"

func TestBucket(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "0"},
		{0, "0"},
		{1, "1-10"},
		{10, "1-10"},
		{11, "11-100"},
		{100, "11-100"},
		{101, "101-1000"},
		{1000, "101-1000"},
		{1001, "1000+"},
		{1_000_000, "1000+"},
	}
	for _, c := range cases {
		if got := bucket(c.in); got != c.want {
			t.Errorf("bucket(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsOptedIn(t *testing.T) {
	yes := []string{"true", "TRUE", "1", "yes", " on "}
	no := []string{"", "false", "0", "no", "off", "definitely"}
	for _, v := range yes {
		if !isOptedIn(v) {
			t.Errorf("isOptedIn(%q) should be true", v)
		}
	}
	for _, v := range no {
		if isOptedIn(v) {
			t.Errorf("isOptedIn(%q) should be false", v)
		}
	}
}
