package web

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// HTTP signatures as ActivityPub servers use them (the Cisco draft, "draft-cavage-http-signatures-12", which Mastodon
// and nearly everything else speaks): the sender signs the request target, the host, the date and a SHA-256 digest of
// the body with its RSA key, and says where to find the key. Weft uses nothing but the standard library for it.

// signatureSkew is how far a request's Date may be from our clock, either way.
const signatureSkew = time.Hour

// signedHeaders are what a signature must cover for us to believe it: where it goes, when, and what it carries.
var signedHeaders = []string{"(request-target)", "host", "date", "digest"}

// digestOf is the Digest header of a body.
func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
}

// signRequest sets Date, Digest and Signature on a POST that carries body, signed with key as keyID. The request's
// URL and Host must already be what the receiver will see.
func signRequest(req *http.Request, body []byte, keyID string, key *rsa.PrivateKey) error {
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("Digest", digestOf(body))
	if req.Host == "" {
		req.Host = req.URL.Host
	}
	str, err := signingString(req, signedHeaders)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(str))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return err
	}
	req.Header.Set("Signature", fmt.Sprintf(`keyId="%s",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		keyID, strings.Join(signedHeaders, " "), base64.StdEncoding.EncodeToString(sig)))
	return nil
}

// signingString is the text that is signed: one line for each of the named headers.
func signingString(r *http.Request, names []string) (string, error) {
	lines := make([]string, 0, len(names))
	for _, n := range names {
		switch n {
		case "(request-target)":
			lines = append(lines, "(request-target): "+strings.ToLower(r.Method)+" "+r.URL.RequestURI())
		case "host":
			host := r.Host
			if host == "" {
				host = r.URL.Host
			}
			lines = append(lines, "host: "+host)
		default:
			v := r.Header.Values(n)
			if len(v) == 0 {
				return "", fmt.Errorf("the signature covers %s, which the request does not have", n)
			}
			for i := range v {
				v[i] = strings.TrimSpace(v[i])
			}
			lines = append(lines, n+": "+strings.Join(v, ", "))
		}
	}
	return strings.Join(lines, "\n"), nil
}

var sigParam = regexp.MustCompile(`([A-Za-z]+)="([^"]*)"`)

// A signature is the parts of a Signature header.
type signature struct {
	keyID   string
	headers []string
	value   []byte
}

func parseSignature(h string) (*signature, error) {
	if h == "" {
		return nil, errors.New("the request is not signed")
	}
	got := map[string]string{}
	for _, m := range sigParam.FindAllStringSubmatch(h, -1) {
		got[strings.ToLower(m[1])] = m[2]
	}
	if got["keyid"] == "" || got["signature"] == "" {
		return nil, errors.New("the Signature header has no keyId or signature")
	}
	switch strings.ToLower(got["algorithm"]) {
	case "", "rsa-sha256", "hs2019": // hs2019 names no algorithm: the key says, and ours are RSA with SHA-256
	default:
		return nil, errors.New("the signature algorithm is not rsa-sha256")
	}
	v, err := base64.StdEncoding.DecodeString(got["signature"])
	if err != nil {
		return nil, errors.New("the signature is not base64")
	}
	headers := strings.Fields(strings.ToLower(got["headers"]))
	if len(headers) == 0 {
		headers = []string{"date"} // the draft's default
	}
	return &signature{keyID: got["keyid"], headers: headers, value: v}, nil
}

// checkSignedRequest does what needs no key: the signature is there and covers what it must, the body is the one
// the Digest says and the Date is near. It returns the signature, ready to be checked against a key.
func checkSignedRequest(r *http.Request, body []byte, now time.Time) (*signature, error) {
	sig, err := parseSignature(r.Header.Get("Signature"))
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	for _, h := range sig.headers {
		covered[h] = true
	}
	for _, need := range signedHeaders {
		if !covered[need] {
			return nil, fmt.Errorf("the signature does not cover %s", need)
		}
	}
	want := digestOf(body)
	got := strings.TrimSpace(r.Header.Get("Digest"))
	if i := strings.IndexByte(got, '='); i > 0 && strings.EqualFold(got[:i], "sha-256") {
		got = "SHA-256" + got[i:]
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return nil, errors.New("the Digest is not that of the body")
	}
	date, err := http.ParseTime(r.Header.Get("Date"))
	if err != nil {
		return nil, errors.New("the Date is missing or not a date")
	}
	if d := now.Sub(date); d > signatureSkew || d < -signatureSkew {
		return nil, errors.New("the Date is too far from the time here")
	}
	return sig, nil
}

// verify checks the signature against a public key (PEM, PKIX).
func (sig *signature) verify(r *http.Request, publicPEM string) error {
	blk, _ := pem.Decode([]byte(publicPEM))
	if blk == nil {
		return errors.New("the key is not PEM")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return errors.New("the key cannot be read")
	}
	pub, ok := k.(*rsa.PublicKey)
	if !ok {
		return errors.New("the key is not RSA")
	}
	str, err := signingString(r, sig.headers)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(str))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig.value); err != nil {
		return errors.New("the signature does not match")
	}
	return nil
}

// privateKey reads the account's signing key.
func privateKey(privatePEM string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(privatePEM))
	if blk == nil {
		return nil, errors.New("the signing key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the signing key is not RSA")
	}
	return rk, nil
}

// signGet signs a GET that carries no body: the request target, the host and the date. Servers that ask for signed
// fetches (Mastodon's "authorized fetch") check it against the key at keyID, which is the account's actor.
func signGet(req *http.Request, keyID string, key *rsa.PrivateKey) error {
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	if req.Host == "" {
		req.Host = req.URL.Host
	}
	names := []string{"(request-target)", "host", "date"}
	str, err := signingString(req, names)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(str))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return err
	}
	req.Header.Set("Signature", fmt.Sprintf(`keyId="%s",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		keyID, strings.Join(names, " "), base64.StdEncoding.EncodeToString(sig)))
	return nil
}
