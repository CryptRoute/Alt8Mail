package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

var base32NoPadding = base32.StdEncoding.WithPadding(base32.NoPadding)

func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32NoPadding.EncodeToString(buf), nil
}

func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(at.Unix() / 30)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

func ValidateTOTP(secret, code string, at time.Time, window int) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	if window < 0 {
		window = 0
	}
	for offset := -window; offset <= window; offset++ {
		candidate, err := TOTPCode(secret, at.Add(time.Duration(offset*30)*time.Second))
		if err == nil && subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func TOTPURI(secret, account, issuer string) string {
	label := issuer + ":" + account
	return "otpauth://totp/" + urlPathEscape(label) + "?secret=" + secret +
		"&issuer=" + urlQueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
}

func GenerateRecoveryCodes(count int) ([]string, error) {
	if count < 1 {
		count = 10
	}
	codes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		raw := base32NoPadding.EncodeToString(buf)
		codes = append(codes, raw[:5]+"-"+raw[5:10])
	}
	return codes, nil
}

func NormalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	code = strings.ReplaceAll(code, "-", "")
	return code
}

func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	secret = strings.TrimRight(secret, "=")
	return base32NoPadding.DecodeString(secret)
}

func urlPathEscape(value string) string {
	replacer := strings.NewReplacer("%", "%25", " ", "%20", ":", "%3A", "/", "%2F", "@", "%40", "?", "%3F", "&", "%26", "=", "%3D", "+", "%2B")
	return replacer.Replace(value)
}

func urlQueryEscape(value string) string {
	return strings.ReplaceAll(urlPathEscape(value), ":", "%3A")
}
