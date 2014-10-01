// Copyright (C) 2014 Jakob Borg and Contributors (see the CONTRIBUTORS file).
// All rights reserved. Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/syncthing/syncthing/osutil"
)

var csrfTokens []string
var csrfMut sync.Mutex

// Check for CSRF token on /rest/ URLs. If a correct one is not given, reject
// the request with 403. For / and /index.html, set a new CSRF cookie if none
// is currently set.
func csrfMiddleware(prefix, apiKey string, next http.Handler) http.Handler {
	loadCsrfTokens()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow requests carrying a valid API key
		if apiKey != "" && r.Header.Get("X-API-Key") == apiKey {
			next.ServeHTTP(w, r)
			return
		}

		// Allow requests for the front page, and set a CSRF cookie if there isn't already a valid one.
		if !strings.HasPrefix(r.URL.Path, prefix) {
			cookie, err := r.Cookie("CSRF-Token")
			if err != nil || !validCsrfToken(cookie.Value) {
				cookie = &http.Cookie{
					Name:  "CSRF-Token",
					Value: newCsrfToken(),
				}
				http.SetCookie(w, cookie)
			}
			next.ServeHTTP(w, r)
			return
		}

		if r.Method == "GET" {
			// Allow GET requests unconditionally
			next.ServeHTTP(w, r)
			return
		}

		// Verify the CSRF token
		token := r.Header.Get("X-CSRF-Token")
		if !validCsrfToken(token) {
			http.Error(w, "CSRF Error", 403)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func validCsrfToken(token string) bool {
	csrfMut.Lock()
	defer csrfMut.Unlock()
	for _, t := range csrfTokens {
		if t == token {
			return true
		}
	}
	return false
}

func newCsrfToken() string {
	token := randomString(30)

	csrfMut.Lock()
	csrfTokens = append(csrfTokens, token)
	if len(csrfTokens) > 10 {
		csrfTokens = csrfTokens[len(csrfTokens)-10:]
	}
	defer csrfMut.Unlock()

	saveCsrfTokens()

	return token
}

func saveCsrfTokens() {
	name := filepath.Join(confDir, "csrftokens.txt")
	tmp := fmt.Sprintf("%s.tmp.%d", name, time.Now().UnixNano())

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer os.Remove(tmp)

	for _, t := range csrfTokens {
		_, err := fmt.Fprintln(f, t)
		if err != nil {
			return
		}
	}

	err = f.Close()
	if err != nil {
		return
	}

	osutil.Rename(tmp, name)
}

func loadCsrfTokens() {
	name := filepath.Join(confDir, "csrftokens.txt")
	f, err := os.Open(name)
	if err != nil {
		return
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		csrfTokens = append(csrfTokens, s.Text())
	}
}

func randomString(len int) string {
	bs := make([]byte, len)
	_, err := rand.Reader.Read(bs)
	if err != nil {
		l.Fatalln(logPrefix, err)
	}

	return base64.StdEncoding.EncodeToString(bs)
}
