package app

import (
	"strings"
	"testing"
)

// Values from RFC 7616 section 3.9.1 (SHA-256 example).
func TestDigestAuthorizationRFC7616(t *testing.T) {
	challenge := `Digest realm="http-auth@example.org", qop="auth, auth-int", algorithm=SHA-256, ` +
		`nonce="7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`

	got, err := digestAuthorizationWithCnonce(challenge, "GET", "/dir/index.html",
		"Mufasa", "Circle of Life", "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ")
	if err != nil {
		t.Fatal(err)
	}
	wantResponse := `response="753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1"`
	if !strings.Contains(got, wantResponse) {
		t.Errorf("digest response mismatch:\n got  %s\n want …%s…", got, wantResponse)
	}
	for _, part := range []string{`username="Mufasa"`, `algorithm=SHA-256`, `qop=auth`, `nc=00000001`, `opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %s in header: %s", part, got)
		}
	}
}

func TestDigestAuthorizationRejectsNonDigest(t *testing.T) {
	if _, err := digestAuthorization(`Basic realm="x"`, "GET", "/", "u", "p"); err == nil {
		t.Error("expected error for non-digest challenge")
	}
}
