package wufetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownload_VerifiesHash(t *testing.T) {
	content := []byte("pretend-cab-bytes")
	sum := sha256.Sum256(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	f := File{Name: "test.cab", URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}
	if err := Download(context.Background(), srv.Client(), f, &buf); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatal("downloaded content mismatch")
	}
}

func TestDownload_RejectsHashMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("actual content"))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	f := File{Name: "test.cab", URL: srv.URL, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	err := Download(context.Background(), srv.Client(), f, &buf)
	if err == nil {
		t.Fatal("expected a SHA-256 mismatch error")
	}
}

func TestDownload_NoURL(t *testing.T) {
	var buf bytes.Buffer
	if err := Download(context.Background(), http.DefaultClient, File{Name: "x"}, &buf); err == nil {
		t.Fatal("expected error for a file with no URL")
	}
}
