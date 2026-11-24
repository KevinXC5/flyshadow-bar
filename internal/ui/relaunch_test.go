//go:build darwin

package ui

import "testing"

func TestAppBundlePathFromExe(t *testing.T) {
	cases := []struct {
		exe  string
		want string
	}{
		{"/Applications/FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar", "/Applications/FlyShadow Bar.app"},
		{"/Users/demo/Applications/FlyShadow Bar.app/Contents/MacOS/fsbar", "/Users/demo/Applications/FlyShadow Bar.app"},
		{"/usr/local/bin/fsbar", ""},
		{"/tmp/fsbar", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := appBundlePathFromExe(tc.exe); got != tc.want {
			t.Errorf("appBundlePathFromExe(%q) = %q, want %q", tc.exe, got, tc.want)
		}
	}
}
