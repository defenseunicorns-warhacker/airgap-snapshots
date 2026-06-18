package objstore

import "testing"

func TestParseAWSCredentials(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		profile        string
		wantAK, wantSK string
		wantToken      string
		wantErr        bool
	}{
		{
			name:    "velero default profile",
			body:    "[default]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = secret123\n",
			wantAK:  "AKIAEXAMPLE",
			wantSK:  "secret123",
		},
		{
			name:      "with session token and comments",
			body:      "# creds\n[default]\naws_access_key_id=AK\naws_secret_access_key=SK\naws_session_token=TOKEN\n; trailing comment\n",
			wantAK:    "AK",
			wantSK:    "SK",
			wantToken: "TOKEN",
		},
		{
			name:    "named profile selected",
			body:    "[default]\naws_access_key_id=DEF\naws_secret_access_key=DEFSK\n[backup]\naws_access_key_id=BAK\naws_secret_access_key=BAKSK\n",
			profile: "backup",
			wantAK:  "BAK",
			wantSK:  "BAKSK",
		},
		{
			name:   "no header falls back to lone section",
			body:   "aws_access_key_id = LONE\naws_secret_access_key = LONESK\n",
			wantAK: "LONE",
			wantSK: "LONESK",
		},
		{
			name:   "single non-default named section used when default absent",
			body:   "[only]\naws_access_key_id=ONLY\naws_secret_access_key=ONLYSK\n",
			wantAK: "ONLY",
			wantSK: "ONLYSK",
		},
		{
			name:    "missing secret is an error",
			body:    "[default]\naws_access_key_id=AK\n",
			wantErr: true,
		},
		{
			name:    "requested profile absent among multiple",
			body:    "[a]\naws_access_key_id=A\naws_secret_access_key=AS\n[b]\naws_access_key_id=B\naws_secret_access_key=BS\n",
			profile: "c",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ak, sk, token, err := ParseAWSCredentials([]byte(tt.body), tt.profile)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ak=%q sk=%q", ak, sk)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ak != tt.wantAK || sk != tt.wantSK || token != tt.wantToken {
				t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)", ak, sk, token, tt.wantAK, tt.wantSK, tt.wantToken)
			}
		})
	}
}

func TestPrefixKeyRoundTrip(t *testing.T) {
	tests := []struct {
		prefix   string
		rel      string // caller key (relative to Config.Prefix)
		wantFull string
	}{
		{"backups", "backups/dev/velero-backup.json", "backups/backups/dev/velero-backup.json"},
		{"backups", "kopia/velero/p0001", "backups/kopia/velero/p0001"},
		{"backups", "snapback/ledger/default.json", "backups/snapback/ledger/default.json"},
		{"backups", "/leading/slash", "backups/leading/slash"},
		{"", "kopia/velero/p0001", "kopia/velero/p0001"},
		{"a/b", "kopia/x", "a/b/kopia/x"},
	}
	for _, tt := range tests {
		s := &minioStore{prefix: tt.prefix}
		full := s.fullKey(tt.rel)
		if full != tt.wantFull {
			t.Errorf("fullKey(%q) with prefix %q = %q, want %q", tt.rel, tt.prefix, full, tt.wantFull)
		}
		// relKey must invert fullKey so List keys round-trip back through Open.
		if got := s.relKey(full); got != trimLeadingSlash(tt.rel) {
			t.Errorf("relKey(%q) with prefix %q = %q, want %q", full, tt.prefix, got, trimLeadingSlash(tt.rel))
		}
	}
}

func trimLeadingSlash(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	return s
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		raw        string
		wantHost   string
		wantSecure bool
	}{
		{"", "", false},
		{"http://minio.uds-dev-stack.svc.cluster.local:9000", "minio.uds-dev-stack.svc.cluster.local:9000", false},
		{"https://s3.example.com", "s3.example.com", true},
		{"localhost:9000", "localhost:9000", false},
	}
	for _, tt := range tests {
		host, secure := normalizeEndpoint(tt.raw)
		if host != tt.wantHost || secure != tt.wantSecure {
			t.Errorf("normalizeEndpoint(%q) = (%q,%v), want (%q,%v)", tt.raw, host, secure, tt.wantHost, tt.wantSecure)
		}
	}
}
