package fido

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestFindVersion(t *testing.T) {
	ver, err := findVersion("windows 11")
	if err != nil {
		t.Fatalf("findVersion: %v", err)
	}
	if ver.name != "Windows 11" {
		t.Fatalf("got %q, want %q", ver.name, "Windows 11")
	}

	if _, err := findVersion(""); err == nil {
		t.Fatal("expected error for empty version")
	}
	if _, err := findVersion("does not exist"); err == nil {
		t.Fatal("expected error for unknown version")
	}
}

func TestFindRelease(t *testing.T) {
	ver, err := findVersion("Windows 11")
	if err != nil {
		t.Fatal(err)
	}

	rel, err := findRelease(ver, "Latest")
	if err != nil {
		t.Fatalf("findRelease(Latest): %v", err)
	}
	if rel != &ver.releases[0] {
		t.Fatal("Latest did not select the first release")
	}

	rel, err = findRelease(ver, "25H2")
	if err != nil {
		t.Fatalf("findRelease(25H2): %v", err)
	}
	if !strings.HasPrefix(rel.name, "25H2") {
		t.Fatalf("got %q", rel.name)
	}

	if _, err := findRelease(ver, ""); err == nil {
		t.Fatal("expected error for empty release")
	}
	if _, err := findRelease(ver, "99H9"); err == nil {
		t.Fatal("expected error for unknown release")
	}
}

func TestFindEdition(t *testing.T) {
	ver, err := findVersion("Windows 11")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := findRelease(ver, "Latest")
	if err != nil {
		t.Fatal(err)
	}

	ed, err := findEdition(rel, "Home/Pro/Edu")
	if err != nil {
		t.Fatalf("findEdition: %v", err)
	}
	if len(ed.ids) != 2 {
		t.Fatalf("got %d ids, want 2", len(ed.ids))
	}

	if _, err := findEdition(rel, ""); err == nil {
		t.Fatal("expected error for empty edition")
	}
	if _, err := findEdition(rel, "does not exist"); err == nil {
		t.Fatal("expected error for unknown edition")
	}
}

func TestSelectLanguage(t *testing.T) {
	languages := []languageEntry{
		{code: "en-us", display: "English"},
		{code: "en-gb", display: "English International"},
	}

	lang, err := selectLanguage(languages, "International")
	if err != nil {
		t.Fatalf("selectLanguage: %v", err)
	}
	if lang.code != "en-gb" {
		t.Fatalf("got %q", lang.code)
	}

	if _, err := selectLanguage(languages, "Klingon"); err == nil {
		t.Fatal("expected error for unknown language")
	}
}

func TestSelectArch(t *testing.T) {
	links := []downloadLink{
		{arch: "x64", url: "https://example.invalid/x64.iso"},
		{arch: "ARM64", url: "https://example.invalid/arm64.iso"},
	}

	link, err := selectArch(links, "arm64")
	if err != nil {
		t.Fatalf("selectArch: %v", err)
	}
	if link.url != "https://example.invalid/arm64.iso" {
		t.Fatalf("got %q", link.url)
	}

	if _, err := selectArch(links, "x86"); err == nil {
		t.Fatal("expected error for unavailable arch")
	}
}

func TestArchFromType(t *testing.T) {
	cases := map[int]string{0: "x86", 1: "x64", 2: "ARM64", 99: "Unknown"}
	for in, want := range cases {
		if got := archFromType(in); got != want {
			t.Errorf("archFromType(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchUEFIShellLink(t *testing.T) {
	ver, err := findVersion("UEFI Shell 2.2")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := findRelease(ver, "24H2")
	if err != nil {
		t.Fatal(err)
	}
	ed, err := findEdition(rel, "Release")
	if err != nil {
		t.Fatal(err)
	}

	res, err := fetchUEFIShellLink(ver, rel, ed)
	if err != nil {
		t.Fatalf("fetchUEFIShellLink: %v", err)
	}
	const want = "https://github.com/pbatard/UEFI-Shell/releases/download/24H2/UEFI-Shell-2.2-24H2-RELEASE.iso"
	if res.URL != want {
		t.Fatalf("got %q, want %q", res.URL, want)
	}
	if res.FileName != "UEFI-Shell-2.2-24H2-RELEASE.iso" {
		t.Fatalf("got file name %q", res.FileName)
	}

	ed, err = findEdition(rel, "Debug")
	if err != nil {
		t.Fatal(err)
	}
	res, err = fetchUEFIShellLink(ver, rel, ed)
	if err != nil {
		t.Fatalf("fetchUEFIShellLink (debug): %v", err)
	}
	if !strings.HasSuffix(res.URL, "-DEBUG.iso") {
		t.Fatalf("got %q, want a -DEBUG.iso suffix", res.URL)
	}
}

func TestFetchDownloadURLRequiresLanguageAndArch(t *testing.T) {
	// This exercises only the pre-network validation path (Version/Release/
	// Edition resolution and the Language/Arch required-ness checks), which
	// return before any HTTP call is made — see resolveLocale onward in
	// FetchDownloadURL. It must not require network access.
	ctx := context.Background()
	cfg := Config{Version: "Windows 11", Release: "Latest", Edition: "Home/Pro/Edu"}
	if _, err := FetchDownloadURL(ctx, cfg); err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("expected a language-required error, got %v", err)
	}

	cfg.Language = "English"
	if _, err := FetchDownloadURL(ctx, cfg); err == nil || !strings.Contains(err.Error(), "arch") {
		t.Fatalf("expected an arch-required error, got %v", err)
	}
}

// TestBannedMessageRegexHandlesEmbeddedNewline is a regression test for the
// fix described in msgPattern's doc comment: a fixture modeled on the real,
// live HTML observed 2026-09-14 (an attribute value whose embedded anchor
// tag wraps onto a second physical line) must still be extracted whole.
func TestBannedMessageRegexHandlesEmbeddedNewline(t *testing.T) {
	const fixture = `<input id="msg-01" type="hidden" value="Your request was blocked. Contact &lt;a href=&#34;https://support.microsoft.com/&#34;
   target=&#34;_blank&#34;> Support&lt;/a> and cite message code 715-123130 and "/>`

	m := msgPattern.FindStringSubmatch(fixture)
	if m == nil {
		t.Fatal("msgPattern did not match a value containing an embedded newline")
	}
	msg := strings.ReplaceAll(m[1], "&lt;", "<")
	msg = htmlTagPattern.ReplaceAllString(msg, "")
	msg = whitespacePattern.ReplaceAllString(msg, " ")
	if !strings.Contains(msg, "715-123130") {
		t.Fatalf("reconstructed message lost the message code: %q", msg)
	}
	if strings.Contains(msg, "<a") || strings.Contains(msg, "target=") {
		t.Fatalf("reconstructed message still contains raw tag markup: %q", msg)
	}

	// Sanity-check the pre-fix pattern (without (?s)) really does fail on
	// the same fixture, so this test would have caught the regression.
	brokenPattern := regexp.MustCompile(`<input id="msg-01" type="hidden" value="(.*?)"/>`)
	if brokenPattern.FindStringSubmatch(fixture) != nil {
		t.Fatal("expected the non-dotall pattern to fail to match a multi-line value")
	}
}

func TestNewSessionIDFormat(t *testing.T) {
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newSessionID()
		if !uuidPattern.MatchString(id) {
			t.Fatalf("newSessionID() = %q, does not look like a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("newSessionID() produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}
