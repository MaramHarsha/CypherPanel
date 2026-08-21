package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Signing headers (outbound-webhooks.md §4). Stable wire values — a receiver
// parses these by name, so they are part of the published contract.
const (
	HeaderEvent     = "X-CypherPanel-Event"
	HeaderDelivery  = "X-CypherPanel-Delivery"
	HeaderTimestamp = "X-CypherPanel-Timestamp"
	HeaderSignature = "X-CypherPanel-Signature"
)

// signaturePrefix is the algorithm tag on the signature header. The
// `sha256=<hex>` shape is deliberate: it is what this repo already parses for
// inbound GitHub deliveries (verifyWebhookSignature in
// core/api/rest/handlers_deployments.go), so a receiver can reuse any
// GitHub-webhook recipe (spec §4).
const signaturePrefix = "sha256="

// sign returns the X-CypherPanel-Signature value for a body at a timestamp.
//
// The signed string is `timestamp + "." + rawBody`, so a captured body cannot
// be replayed indefinitely: the receiver rejects a timestamp outside its
// freshness window and the signature is bound to it. The MAC covers the RAW
// bytes — a receiver must verify BEFORE parsing, because a signature over
// reserialised JSON signs the wrong bytes (spec §4).
func sign(secret []byte, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// applySignatureHeaders stamps one attempt's headers onto a request. Every
// value is sanitised of CR/LF so an operator-controlled string can never inject
// an extra header (spec §6); in practice only ids and event keys reach here,
// but the defense is free and the invariant is worth keeping local.
func applySignatureHeaders(req *http.Request, eventType, deliveryID string, at time.Time, secret, body []byte) {
	ts := at.Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderEvent, sanitizeHeader(eventType))
	req.Header.Set(HeaderDelivery, sanitizeHeader(deliveryID))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, sign(secret, ts, body))
}

// sanitizeHeader neutralises CR and LF in a header value so it cannot inject
// additional headers or split the request (mirrors notify.sanitizeHeader).
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}
