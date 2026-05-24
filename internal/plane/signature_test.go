package plane

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
)

const testSecret = "shhh-this-is-only-a-test"

func sign(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"event":"issue","action":"create"}`)
	good := sign(t, testSecret, body)

	tests := []struct {
		name    string
		secret  string
		sig     string
		body    []byte
		wantErr error
	}{
		{
			name:    "valid",
			secret:  testSecret,
			sig:     good,
			body:    body,
			wantErr: nil,
		},
		{
			name:    "empty secret rejected",
			secret:  "",
			sig:     good,
			body:    body,
			wantErr: ErrEmptySecret,
		},
		{
			name:    "missing header",
			secret:  testSecret,
			sig:     "",
			body:    body,
			wantErr: ErrMissingSignature,
		},
		{
			name:    "wrong secret",
			secret:  testSecret,
			sig:     sign(t, "different-secret", body),
			body:    body,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "tampered body",
			secret:  testSecret,
			sig:     good,
			body:    []byte(`{"event":"issue","action":"delete"}`),
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "garbage signature",
			secret:  testSecret,
			sig:     "not-hex-at-all",
			body:    body,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "length mismatch",
			secret:  testSecret,
			sig:     good + "ff",
			body:    body,
			wantErr: ErrInvalidSignature,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tc.sig != "" {
				h.Set(HeaderSignature, tc.sig)
			}
			err := VerifySignature(tc.secret, h, tc.body)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifySignature err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
