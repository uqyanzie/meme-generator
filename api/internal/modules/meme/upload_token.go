package meme

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type uploadTokenClaims struct {
	Key      string `json:"k"`
	MIME     string `json:"m"`
	MaxBytes int64  `json:"max"`
	Size     int64  `json:"sz,omitempty"`
	Exp      int64  `json:"exp"`
}

func signUploadToken(secret []byte, claims uploadTokenClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, nil
}

func verifyUploadToken(secret []byte, raw string, now time.Time) (uploadTokenClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return uploadTokenClaims{}, ErrInvalidUploadToken
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0]))
	expectedSig := mac.Sum(nil)

	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(expectedSig, gotSig) {
		return uploadTokenClaims{}, ErrInvalidUploadToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return uploadTokenClaims{}, ErrInvalidUploadToken
	}

	var claims uploadTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return uploadTokenClaims{}, ErrInvalidUploadToken
	}
	if claims.Key == "" || claims.MIME == "" || claims.MaxBytes <= 0 || claims.Exp < now.Unix() {
		return uploadTokenClaims{}, ErrInvalidUploadToken
	}
	return claims, nil
}
