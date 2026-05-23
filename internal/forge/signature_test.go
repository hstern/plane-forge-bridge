package forge

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	const secret = "shhh-its-a-secret"
	body := []byte(`{"hello":"world"}`)
	good := computeSignature(secret, body)

	tests := []struct {
		name    string
		secret  string
		header  string
		body    []byte
		wantErr error
	}{
		{
			name:    "happy path",
			secret:  secret,
			header:  good,
			body:    body,
			wantErr: nil,
		},
		{
			name:    "missing header",
			secret:  secret,
			header:  "",
			body:    body,
			wantErr: ErrMissingSignature,
		},
		{
			name:    "empty secret is rejected",
			secret:  "",
			header:  good,
			body:    body,
			wantErr: ErrEmptySecret,
		},
		{
			name:    "wrong secret",
			secret:  "different-secret",
			header:  good,
			body:    body,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "header is not hex",
			secret:  secret,
			header:  "not-a-hex-string!!",
			body:    body,
			wantErr: ErrInvalidSignature,
		},
		{
			name:    "tampered body",
			secret:  secret,
			header:  good,
			body:    []byte(`{"hello":"WORLD"}`),
			wantErr: ErrInvalidSignature,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tc.header != "" {
				h.Set(HeaderSignature, tc.header)
			}
			err := VerifySignature(tc.secret, h, tc.body)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifySignature() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestVerifySignature_NearMiss is a smoke test that a signature off by a
// single bit still fails. Real constant-time behaviour is enforced by the
// fact that the implementation uses hmac.Equal — we verify that by reading
// the source rather than measuring timing here, which would be flaky.
func TestVerifySignature_NearMiss(t *testing.T) {
	t.Parallel()

	const secret = "shhh-its-a-secret"
	body := []byte(`{"hello":"world"}`)
	good := computeSignature(secret, body)

	// Flip the last hex nibble. This still parses as hex, so we exercise the
	// hmac.Equal branch rather than the hex.DecodeString branch.
	var nearMiss string
	switch good[len(good)-1] {
	case '0':
		nearMiss = good[:len(good)-1] + "1"
	default:
		nearMiss = good[:len(good)-1] + "0"
	}

	h := http.Header{}
	h.Set(HeaderSignature, nearMiss)
	if err := VerifySignature(secret, h, body); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("near-miss signature: got %v, want ErrInvalidSignature", err)
	}
}

// TestVerifySignature_UsesConstantTimeCompare is a defensive guard: it reads
// the signature.go source and fails if someone replaces hmac.Equal with a
// `==` or bytes.Equal comparison. Cheap insurance against a future
// well-meaning refactor.
func TestVerifySignature_UsesConstantTimeCompare(t *testing.T) {
	t.Parallel()
	src := mustReadFile(t, "signature.go")
	if !strings.Contains(src, "hmac.Equal(") {
		t.Fatal("signature.go must compare MACs with hmac.Equal (constant time)")
	}
}
