package main

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"net/http"
	"strings"
)

//go:embed web/settings.html web/settings.js
var webAssets embed.FS

func settingsPage(head bool) ([]byte, error) {
	b, err := webAssets.ReadFile("web/settings.html")
	if err != nil {
		return nil, err
	}
	js, _ := webAssets.ReadFile("web/settings.js")
	nonceBytes := make([]byte, 18)
	if _, err = rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	body := []byte(strings.NewReplacer("{{NONCE}}", nonce, "{{JS}}", string(js)).Replace(string(b)))
	if head {
		body = nil
	}
	return okEnvelope(managementResponse{200, http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "Content-Security-Policy": {"default-src 'none'; script-src 'nonce-" + nonce + "'; style-src 'nonce-" + nonce + "'; connect-src 'self'; base-uri 'none'; frame-ancestors 'self'; form-action 'none'"}, "Referrer-Policy": {"no-referrer"}, "X-Content-Type-Options": {"nosniff"}}, body})
}
