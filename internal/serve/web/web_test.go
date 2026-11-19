package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
)

// embedded is each of the page's files by its served path, / for index.html.
func embedded(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := fs.WalkDir(static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, p)
		served := strings.TrimPrefix(p, "static")
		if served == "/index.html" {
			served = "/"
		}
		files[served] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Fatalf("%d embedded files, want index.html, app.css and app.js at least", len(files))
	}
	return files
}

func TestHandlerServesEachFile(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for p, want := range embedded(t) {
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		if err == nil {
			err = res.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		typ, ok := types[path.Ext(p)]
		if !ok {
			typ = "text/html; charset=utf-8"
		}
		if res.Header.Get("Content-Security-Policy") != csp {
			t.Errorf("%s: no Content-Security-Policy", p)
		}
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != typ || string(body) != string(want) {
			t.Errorf("%s: %d %q (%d bytes), want 200 %q (%d bytes)", p, res.StatusCode, res.Header.Get("Content-Type"), len(body), typ, len(want))
		}
	}
}

// comment is an HTML or block comment, or a line that is a // comment.
var comment = regexp.MustCompile(`(?s)<!--.*?-->|/\*.*?\*/|(?m)^\s*//[^\n]*`)

// external is a URL to another host: a scheme, or // where a URL starts.
var external = regexp.MustCompile("(?i)\\b(?:https?|wss?):|[\"'`(=]//|@import")

func TestNoExternalURL(t *testing.T) {
	for p, b := range embedded(t) {
		if bad := external.FindString(comment.ReplaceAllString(string(b), "")); bad != "" {
			t.Errorf("%s has %q: the page makes no request outside the Mac it came from", p, bad)
		}
	}
	for _, s := range []string{`<script src="https://x">`, "fetch(`//x`)", `<img src=//x>`, `new WebSocket('wss://x')`} {
		if !external.MatchString(s) {
			t.Errorf("external misses %s", s)
		}
	}
}
