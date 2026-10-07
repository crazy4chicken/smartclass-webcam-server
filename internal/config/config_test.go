package config

import (
	"slices"
	"testing"
)

// TestSecrets verifies the credential inventory the service hands to the
// redactor: every secret the configuration carries is listed, whatever form the
// connection string takes, and unset values stay out.
func TestSecrets(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "URL connection string",
			env: map[string]string{
				"WEBCAM_DB_URL":                  "postgres://webcam:s3cr3t-db-password@127.0.0.1:5432/webcam?sslmode=disable",
				"WEBCAM_S3_ACCESS_KEY":           "access-key-id",
				"WEBCAM_S3_SECRET_KEY":           "s3-secret-key",
				"WEBCAM_TEAMUSERS_SVC_TOKEN":     "svc-token-value",
				"WEBCAM_TEAMUSERS_CLIENT_SECRET": "client-secret-value",
			},
			want: []string{
				"access-key-id",
				"s3-secret-key",
				"svc-token-value",
				"client-secret-value",
				"s3cr3t-db-password",
			},
		},
		{
			name: "keyword connection string",
			env: map[string]string{
				"WEBCAM_DB_URL": "host=127.0.0.1 user=webcam password='quoted-password' dbname=webcam",
			},
			want: []string{"", "", "", "", "quoted-password"},
		},
		{
			name: "nothing configured",
			env:  map[string]string{"WEBCAM_DB_URL": "postgres://webcam@127.0.0.1:5432/webcam"},
			want: []string{"", "", "", "", ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{
				"WEBCAM_DB_URL", "WEBCAM_S3_ACCESS_KEY", "WEBCAM_S3_SECRET_KEY",
				"WEBCAM_TEAMUSERS_SVC_TOKEN", "WEBCAM_TEAMUSERS_CLIENT_SECRET",
			} {
				t.Setenv(key, "")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			t.Setenv("WEBCAM_TEAMUSERS_URL", "http://127.0.0.1:8081")

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Secrets(); !slices.Equal(got, tc.want) {
				t.Fatalf("Secrets() = %q, want %q", got, tc.want)
			}
		})
	}
}
