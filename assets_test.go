package worker

import "testing"

func TestContentType(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"index.html", "text/html; charset=utf-8"},
		{"style.css", "text/css; charset=utf-8"},
		{"app.js", "text/javascript; charset=utf-8"},
		{"data.json", "application/json"},
		{"image.png", "image/png"},
		{"no-extension", "application/octet-stream"},
		{"", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := contentType(tt.path)
			if got != tt.want {
				t.Errorf("contentType(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestContentType_UppercaseExtension(t *testing.T) {
	got := contentType("file.HTML")
	if got == "application/octet-stream" {
		t.Error("contentType should handle uppercase extension")
	}
}

func TestContentType_UnknownExtension(t *testing.T) {
	got := contentType("file.xyz999")
	if got != "application/octet-stream" {
		t.Errorf("contentType(unknown ext) = %q, want application/octet-stream", got)
	}
}

func TestContentType_DeepPath(t *testing.T) {
	got := contentType("/a/b/c/d.json")
	if got != "application/json" {
		t.Errorf("contentType(deep path) = %q, want application/json", got)
	}
}
