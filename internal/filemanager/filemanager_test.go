package filemanager

import "testing"

func TestCleanRel_NeutralisesTraversal(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		".":                   "",
		"index.php":           "index.php",
		"a/b/c.txt":           "a/b/c.txt",
		"../../etc/passwd":    "etc/passwd",     // .. collapsed against the virtual root
		"/etc/passwd":         "etc/passwd",     // absolute input rebased under root
		"a/../../../b":        "b",              // escaping .. removed
		"a/./b":               "a/b",            // . segments removed
		"..\\..\\windows\\x":  "windows/x",      // backslash smuggling neutralised
		"public_html/../logs": "logs",           // stays within the tree
	}
	for in, want := range cases {
		if got := CleanRel(in); got != want {
			t.Errorf("CleanRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanRel_NeverStartsWithDotDotOrSlash(t *testing.T) {
	for _, in := range []string{"../x", "../../..", "/../x", "....//x", "\\..\\x"} {
		got := CleanRel(in)
		if len(got) > 0 && (got[0] == '/' ) {
			t.Errorf("CleanRel(%q) = %q starts with a separator", in, got)
		}
		if got == ".." || len(got) >= 3 && got[:3] == "../" {
			t.Errorf("CleanRel(%q) = %q still escapes", in, got)
		}
	}
}
