// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package objstore

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// ParseAWSCredentials extracts access key / secret / session token from the AWS
// shared-credentials file format that Velero stores in its BSL credential secret
// (the `cloud` key), e.g.:
//
//	[default]
//	aws_access_key_id = AKIA...
//	aws_secret_access_key = wJalr...
//	aws_session_token = ...        # optional
//
// profile selects the section ("" defaults to "default"). If the requested
// profile is absent but the file has exactly one section, that section is used
// (tolerates secrets written without a [default] header).
func ParseAWSCredentials(body []byte, profile string) (accessKey, secretKey, sessionToken string, err error) {
	if profile == "" {
		profile = "default"
	}

	sections := map[string]map[string]string{}
	current := "" // keys before any [section] header land in the "" section
	sections[current] = map[string]string{}

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := sections[current]; !ok {
				sections[current] = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		sections[current][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if serr := sc.Err(); serr != nil {
		return "", "", "", fmt.Errorf("objstore: scan credentials: %w", serr)
	}

	kv := pickProfile(sections, profile)
	if kv == nil {
		return "", "", "", fmt.Errorf("objstore: credentials profile %q not found", profile)
	}

	accessKey = kv["aws_access_key_id"]
	secretKey = kv["aws_secret_access_key"]
	sessionToken = kv["aws_session_token"]
	if accessKey == "" || secretKey == "" {
		return "", "", "", fmt.Errorf("objstore: credentials profile %q missing access key id or secret access key", profile)
	}
	return accessKey, secretKey, sessionToken, nil
}

// pickProfile returns the named profile, falling back to a header-less section
// or a lone section when the exact name is absent.
func pickProfile(sections map[string]map[string]string, profile string) map[string]string {
	if kv, ok := sections[profile]; ok && len(kv) > 0 {
		return kv
	}
	// Keys written without any [section] header.
	if kv, ok := sections[""]; ok && len(kv) > 0 {
		return kv
	}
	// Exactly one non-empty named section: use it regardless of name.
	var only map[string]string
	count := 0
	for name, kv := range sections {
		if name == "" || len(kv) == 0 {
			continue
		}
		only = kv
		count++
	}
	if count == 1 {
		return only
	}
	return nil
}
