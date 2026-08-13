package webhook

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
)

const (
	SignatureHeader = "X-Dufflebag-Signature"
	EventHeader     = "X-Dufflebag-Event"
	DeliveryHeader  = "X-Dufflebag-Delivery"
)

func Signature(secret string, body []byte) string {
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha512=" + hex.EncodeToString(mac.Sum(nil))
}

func SetHeaders(request *http.Request, secret, operation, eventID string, body []byte) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(EventHeader, operation)
	request.Header.Set(DeliveryHeader, eventID)
	if secret != "" {
		request.Header.Set(SignatureHeader, Signature(secret, body))
	}
}
