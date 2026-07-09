package api

import "testing"

func TestHasWrite(t *testing.T) {
	for perm, want := range map[string]bool{
		"admin": true, "maintain": true, "write": true,
		"triage": false, "read": false, "none": false, "": false,
	} {
		if hasWrite(perm) != want {
			t.Errorf("hasWrite(%q) = %v, want %v", perm, hasWrite(perm), want)
		}
	}
}

func TestContainsFold(t *testing.T) {
	list := []string{"Alice", "bob"}
	for in, want := range map[string]bool{
		"alice": true, "ALICE": true, "bob": true, "BOB": true, "carol": false, "": false,
	} {
		if containsFold(list, in) != want {
			t.Errorf("containsFold(%v, %q) = %v, want %v", list, in, containsFold(list, in), want)
		}
	}
}

func TestSameRepo(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://github.com/wago-org/wasi", "https://github.com/wago-org/wasi.git", true},
		{"https://github.com/Wago-Org/Wasi", "github.com/wago-org/wasi", true},
		{"https://github.com/wago-org/wasi", "https://github.com/wago-org/other", false},
		{"https://github.com/a/b", "https://gitlab.com/a/b", false},
	}
	for _, c := range cases {
		if sameRepo(c.a, c.b) != c.want {
			t.Errorf("sameRepo(%q, %q) = %v, want %v", c.a, c.b, sameRepo(c.a, c.b), c.want)
		}
	}
}
