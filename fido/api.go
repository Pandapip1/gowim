package fido

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// resolveLocale mirrors Fido's Check-Locale: if the locale-specific software
// download page doesn't exist (redirects or errors), fall back to en-US.
// Reverse-engineered / observed behavior (Fido.ps1).
func resolveLocale(ctx context.Context, locale string) string {
	url := "https://www.microsoft.com/" + locale + "/software-download/windows11"
	client := &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "en-US"
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "en-US"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return "en-US"
	}
	return locale
}

// collectLanguages performs the vlscppe/ov-df session handshake and queries
// getskuinformationbyproductedition for each id in editionIDs, merging the
// results by language the way Fido's Get-Windows-Languages does.
func collectLanguages(ctx context.Context, editionIDs []int, locale string) ([]languageEntry, error) {
	var languages []languageEntry
	find := func(code string) int {
		for i := range languages {
			if languages[i].code == code {
				return i
			}
		}
		return -1
	}

	for _, editionID := range editionIDs {
		sessionID := newSessionID()
		if err := checkVlscppeTag(ctx, sessionID); err != nil {
			return nil, err
		}
		if err := ovdfHandshake(ctx, sessionID); err != nil {
			return nil, err
		}
		skus, err := getSkuInformation(ctx, editionID, sessionID, locale)
		if err != nil {
			return nil, err
		}
		for _, sku := range skus {
			i := find(sku.language)
			if i == -1 {
				languages = append(languages, languageEntry{code: sku.language, display: sku.localizedLanguage})
				i = len(languages) - 1
			}
			languages[i].skus = append(languages[i].skus, languageSku{sessionID: sessionID, skuID: sku.id})
		}
	}
	return languages, nil
}

func checkVlscppeTag(ctx context.Context, sessionID string) error {
	url := fmt.Sprintf("https://vlscppe.microsoft.com/tags?org_id=%s&session_id=%s", orgID, sessionID)
	_, err := httpGetBody(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("fido: registering session: %w", err)
	}
	return nil
}

var (
	wRegexp      = regexp.MustCompile(`[?&]w=([A-F0-9]+)`)
	rticksRegexp = regexp.MustCompile(`rticks="\+?(\d+)`)
)

// ovdfHandshake performs the two-step ov-df.microsoft.com "protection" dance
// Fido reverse-engineered: fetch a small JS snippet to extract a 'w' token
// and 'rticks' timestamp, then echo them back with the current epoch time.
// Reverse-engineered / observed behavior (Fido.ps1); re-verified live
// 2026-09-14 — see the package doc comment. The wRegexp/rticksRegexp shapes
// were re-checked byte-for-byte against a live mdt.js response during that
// verification and matched unchanged.
func ovdfHandshake(ctx context.Context, sessionID string) error {
	url := fmt.Sprintf("https://ov-df.microsoft.com/mdt.js?instanceId=%s&PageId=si&session_id=%s", instanceID, sessionID)
	body, err := httpGetBody(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("fido: requesting ov-df data: %w", err)
	}
	wMatch := wRegexp.FindSubmatch(body)
	rMatch := rticksRegexp.FindSubmatch(body)
	if wMatch == nil || rMatch == nil {
		return fmt.Errorf("fido: could not extract ov-df data")
	}

	url = fmt.Sprintf("https://ov-df.microsoft.com/?session_id=%s&CustomerId=%s&PageId=si&w=%s&mdt=%d&rticks=%s",
		sessionID, instanceID, string(wMatch[1]), time.Now().UnixMilli(), string(rMatch[1]))
	if _, err := httpGetBody(ctx, url, nil); err != nil {
		return fmt.Errorf("fido: completing ov-df handshake: %w", err)
	}
	return nil
}

type skuResult struct {
	language          string
	localizedLanguage string
	id                string
}

type apiError struct {
	Type  int    `json:"Type"`
	Value string `json:"Value"`
}

func getSkuInformation(ctx context.Context, editionID int, sessionID, locale string) ([]skuResult, error) {
	url := fmt.Sprintf("https://www.microsoft.com/software-download-connector/api/getskuinformationbyproductedition"+
		"?profile=%s&productEditionId=%d&SKU=undefined&friendlyFileName=undefined&Locale=%s&sessionID=%s",
		profileID, editionID, locale, sessionID)

	var resp struct {
		Skus []struct {
			Language          string `json:"Language"`
			LocalizedLanguage string `json:"LocalizedLanguage"`
			ID                string `json:"Id"`
		} `json:"Skus"`
		Errors []apiError `json:"Errors"`
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		body, err := httpGetBody(ctx, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Skus, resp.Errors = nil, nil
		if err := json.Unmarshal(body, &resp); err != nil {
			lastErr = fmt.Errorf("fido: parsing SKU response: %w", err)
			continue
		}
		if len(resp.Errors) > 0 {
			lastErr = apiErrorToErr(ctx, resp.Errors[0], sessionID, locale)
			continue
		}
		if len(resp.Skus) == 0 {
			lastErr = fmt.Errorf("fido: could not parse languages")
			continue
		}
		results := make([]skuResult, len(resp.Skus))
		for i, s := range resp.Skus {
			results[i] = skuResult{language: s.Language, localizedLanguage: s.LocalizedLanguage, id: s.ID}
		}
		return results, nil
	}
	return nil, lastErr
}

func getDownloadLinks(ctx context.Context, skuID, sessionID, locale string) ([]downloadLink, error) {
	url := fmt.Sprintf("https://www.microsoft.com/software-download-connector/api/GetProductDownloadLinksBySku"+
		"?profile=%s&productEditionId=undefined&SKU=%s&friendlyFileName=undefined&Locale=%s&sessionID=%s",
		profileID, skuID, locale, sessionID)

	body, err := httpGetBody(ctx, url, map[string]string{"Referer": referer})
	if err != nil {
		return nil, err
	}

	var resp struct {
		ProductDownloadOptions []struct {
			DownloadType int    `json:"DownloadType"`
			URI          string `json:"Uri"`
		} `json:"ProductDownloadOptions"`
		Errors []apiError `json:"Errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("fido: parsing download links response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, apiErrorToErr(ctx, resp.Errors[0], sessionID, locale)
	}
	if len(resp.ProductDownloadOptions) == 0 {
		return nil, fmt.Errorf("fido: could not retrieve ISO download links")
	}

	links := make([]downloadLink, len(resp.ProductDownloadOptions))
	for i, o := range resp.ProductDownloadOptions {
		links[i] = downloadLink{arch: archFromType(o.DownloadType), url: o.URI}
	}
	return links, nil
}

func apiErrorToErr(ctx context.Context, e apiError, sessionID, locale string) error {
	if e.Type == 9 {
		return fmt.Errorf("%s%s", bannedMessage(ctx, locale), sessionID)
	}
	return fmt.Errorf("%s", e.Value)
}

func archFromType(t int) string {
	switch t {
	case 0:
		return "x86"
	case 1:
		return "x64"
	case 2:
		return "ARM64"
	default:
		return "Unknown"
	}
}

var (
	// msgPattern carries the `(?s)` (dotall) flag, which the reference
	// implementation's equivalent pattern lacked. Fixed here after live
	// testing 2026-09-14 found the reference pattern no longer matches at
	// all: Microsoft's page now wraps the embedded anchor tag's attributes
	// onto a second line, putting a literal '\n' inside the msg-01 value,
	// and Go's RE2 '.' does not cross a newline without this flag. See the
	// package doc comment for the full before/after.
	msgPattern        = regexp.MustCompile(`(?s)<input id="msg-01" type="hidden" value="(.*?)"/>`)
	htmlTagPattern    = regexp.MustCompile(`<[^>]+>`)
	whitespacePattern = regexp.MustCompile(`\s+`)
	fallbackBannedMsg = "Your IP address has been banned by Microsoft for issuing too many ISO download requests or for " +
		"belonging to a region of the world where sanctions currently apply. Please try again later. " +
		"If you believe this ban to be in error, you can try contacting Microsoft by referring to " +
		"message code 715-123130 and session ID "
)

// bannedMessage mirrors Fido's Get-Code-715-123130-Message: Microsoft's own
// software-download page carries the current wording for this ban message,
// so it's scraped from there with a hardcoded fallback if that fails.
// Reverse-engineered / observed behavior (Fido.ps1); re-verified live
// 2026-09-14 (see msgPattern's comment and the package doc comment) — the
// wording itself, including the "715-123130" message code, is unchanged from
// what fallbackBannedMsg already assumed; only the regex needed a fix.
func bannedMessage(ctx context.Context, locale string) string {
	url := "https://www.microsoft.com/" + locale + "/software-download/windows11"
	body, err := httpGetBody(ctx, url, nil)
	if err != nil {
		return fallbackBannedMsg
	}
	m := msgPattern.FindSubmatch(body)
	if m == nil {
		return fallbackBannedMsg
	}
	msg := strings.ReplaceAll(string(m[1]), "&lt;", "<")
	msg = htmlTagPattern.ReplaceAllString(msg, "")
	msg = whitespacePattern.ReplaceAllString(msg, " ")
	if !strings.Contains(msg, "715-123130") {
		return fallbackBannedMsg
	}
	return msg
}

func httpGetBody(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("fido: request to %s failed: %s", url, resp.Status)
	}
	return body, nil
}
