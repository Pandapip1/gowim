package wufetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListBuilds_ParsesAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/listid.php" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"response":{"apiVersion":"x","builds":{
			"1":{"title":"Windows 11, version 25H2 (26200.9457)","build":"26200.9457","arch":"amd64","created":100,"uuid":"aaa"},
			"2":{"title":"Windows 11, version 25H2 (26200.9539)","build":"26200.9539","arch":"amd64","created":200,"uuid":"bbb"},
			"3":{"title":"Windows 11, version 25H2 (26200.9539)","build":"26200.9539","arch":"arm64","created":200,"uuid":"ccc"}
		}}}`)
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	builds, err := c.ListBuilds(context.Background(), "25H2")
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(builds) != 3 {
		t.Fatalf("got %d builds, want 3", len(builds))
	}
	// Newest build first.
	if builds[0].Build != "26200.9539" {
		t.Fatalf("builds[0].Build = %q, want 26200.9539", builds[0].Build)
	}
	// Within the same build number, arch breaks the tie (amd64 < arm64).
	if builds[0].Arch != "amd64" || builds[1].Arch != "arm64" {
		t.Fatalf("unexpected arch ordering: %+v", builds[:2])
	}
	if builds[2].Build != "26200.9457" {
		t.Fatalf("builds[2].Build = %q, want 26200.9457", builds[2].Build)
	}
}

func TestFindBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"response":{"builds":{
			"1":{"title":"t","build":"26200.9457","arch":"amd64","created":1,"uuid":"aaa"},
			"2":{"title":"t","build":"26200.9457","arch":"arm64","created":1,"uuid":"bbb"}
		}}}`)
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	b, err := c.FindBuild(context.Background(), "26200", "amd64")
	if err != nil {
		t.Fatalf("FindBuild: %v", err)
	}
	if b.UUID != "aaa" {
		t.Fatalf("got UUID %q, want aaa", b.UUID)
	}

	if _, err := c.FindBuild(context.Background(), "26200", "mips"); err == nil {
		t.Fatal("expected error for an architecture with no matching build")
	}
}

func TestGetFiles_ParsesFileMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "some-uuid" {
			t.Fatalf("id = %q", got)
		}
		if got := r.URL.Query().Get("lang"); got != "neutral" {
			t.Fatalf("lang = %q", got)
		}
		if got := r.URL.Query().Get("edition"); got != "FOD" {
			t.Fatalf("edition = %q", got)
		}
		fmt.Fprint(w, `{"response":{"updateName":"x","arch":"amd64","build":"10.0.26200.9457","files":{
			"OpenSSH-Server-Package-amd64.cab":{"sha1":"s1","sha256":"s256","size":2329588,"url":"http://example.invalid/f"}
		}}}`)
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	files, err := c.GetFiles(context.Background(), "some-uuid", "neutral", "FOD")
	if err != nil {
		t.Fatalf("GetFiles: %v", err)
	}
	f, ok := files["OpenSSH-Server-Package-amd64.cab"]
	if !ok {
		t.Fatal("missing expected file")
	}
	if f.Name != "OpenSSH-Server-Package-amd64.cab" || f.SHA256 != "s256" || f.Size != 2329588 {
		t.Fatalf("unexpected file: %+v", f)
	}
}

func TestGetFiles_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"response":{"error":"UNSUPPORTED_COMBINATION"}}`)
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	_, err := c.GetFiles(context.Background(), "x", "neutral", "FOD")
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("got %T, want *APIError", err)
	}
	if apiErr.Code != "UNSUPPORTED_COMBINATION" {
		t.Fatalf("got code %q", apiErr.Code)
	}
}
