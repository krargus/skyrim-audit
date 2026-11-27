package main

import (
	"net/http/httptest"
	"testing"
)

// The mod endpoint moves and deletes folders, so it must refuse anything that is
// not a same-origin JSON request.
func TestGuardDestructive(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		origin      string
		wantErr     bool
	}{
		{"plain json from the UI", "application/json", "", false},
		{"same origin", "application/json; charset=utf-8", "http://127.0.0.1:8721", false},
		{"cross origin", "application/json", "http://evil.example", true},
		{"no content type", "", "", true},
		{"simple request content type", "text/plain", "", true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8721/api/mod", nil)
		r.Host = "127.0.0.1:8721"
		if tc.contentType != "" {
			r.Header.Set("Content-Type", tc.contentType)
		}
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		err := guardDestructive(r)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected refusal", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected refusal: %v", tc.name, err)
		}
	}
}
