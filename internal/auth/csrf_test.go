package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFProtect(t *testing.T) {
	h := CSRFProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	form := func(token string) *strings.Reader {
		return strings.NewReader(url.Values{CSRFFieldName: {token}}.Encode())
	}

	cases := []struct {
		name   string
		method string
		token  string // token milik session; kosong berarti belum login
		body   *strings.Reader
		header map[string]string
		want   int
	}{
		{"GET tidak dicek", "GET", "abc", nil, nil, 200},
		{"POST token cocok", "POST", "abc", form("abc"), nil, 200},
		{"POST token salah", "POST", "abc", form("xyz"), nil, 403},
		{"POST tanpa token", "POST", "abc", form(""), nil, 403},
		{"token lewat header", "POST", "abc", nil, map[string]string{csrfHeader: "abc"}, 200},
		{"DELETE tanpa token", "DELETE", "abc", nil, nil, 403},
		{"belum login, origin sama", "POST", "", form(""), map[string]string{"Origin": "http://example.com"}, 200},
		{"belum login, referer sama", "POST", "", form(""), map[string]string{"Referer": "http://example.com/login"}, 200},
		{"belum login, origin lain", "POST", "", form(""), map[string]string{"Origin": "http://jahat.test"}, 403},
		{"belum login, origin null", "POST", "", form(""), map[string]string{"Origin": "null"}, 403},
		{"belum login, tanpa origin", "POST", "", form(""), nil, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var req *http.Request
			if c.body != nil {
				req = httptest.NewRequest(c.method, "http://example.com/x", c.body)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req = httptest.NewRequest(c.method, "http://example.com/x", nil)
			}
			for k, v := range c.header {
				req.Header.Set(k, v)
			}
			if c.token != "" {
				req = req.WithContext(WithCSRFToken(req.Context(), c.token))
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != c.want {
				t.Fatalf("status %d, want %d", rr.Code, c.want)
			}
		})
	}
}
