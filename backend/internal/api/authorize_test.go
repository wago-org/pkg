package api

import (
	"testing"

	"github.com/wago-org/registry-backend/internal/config"
)

func TestParseAuthors(t *testing.T) {
	cases := map[string][2]string{ // input → {name, github}
		"@octocat":         {"octocat", "octocat"},
		"octocat":          {"octocat", "octocat"}, // bare login
		"Jane Doe <@jane>": {"Jane Doe", "jane"},
		"Jane Doe (@jane)": {"Jane Doe", "jane"},
		"The wago authors": {"The wago authors", ""}, // spaces → name only
		"wago-org":         {"wago-org", "wago-org"}, // hyphen login
		"Bad_Login":        {"Bad_Login", ""},        // underscore not a login char
	}
	for in, want := range cases {
		got := parseAuthors([]string{in})
		if len(got) != 1 || got[0].Name != want[0] || got[0].Github != want[1] {
			t.Errorf("parseAuthors(%q) = %+v, want {Name:%q Github:%q}", in, got, want[0], want[1])
		}
	}
}

func TestIsGitHubLogin(t *testing.T) {
	for in, want := range map[string]bool{
		"octocat": true, "wago-org": true, "a": true,
		"-lead": false, "trail-": false, "has space": false, "und_er": false, "": false,
	} {
		if isGitHubLogin(in) != want {
			t.Errorf("isGitHubLogin(%q) = %v, want %v", in, isGitHubLogin(in), want)
		}
	}
}

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

func TestSafeReturn(t *testing.T) {
	a := &App{Cfg: config.Config{FrontendURL: "https://pkg.wago.sh"}}
	cases := map[string]string{
		"https://pkg.wago.sh/JairusSW/wasi": "https://pkg.wago.sh/JairusSW/wasi",
		"/JairusSW/wasi":                    "https://pkg.wago.sh/JairusSW/wasi",
		"https://pkg.wago.sh":               "https://pkg.wago.sh",
		"":                                  "",
		"https://evil.com/x":                "",                                // other origin
		"//evil.com":                        "",                                // protocol-relative
		"/\\evil.com":                       "https://pkg.wago.sh/%5Cevil.com", // backslash → stays on our host
		"https://pkg.wago.sh.evil.com/x":    "",                                // prefix trickery
	}
	for in, want := range cases {
		if got := a.safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}
