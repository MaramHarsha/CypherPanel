// Profile photos. This is the one place the panel accepts a file from a person
// and hands it back to a browser, so the rules are deliberately narrow.
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	// Registered for their DecodeConfig side effect: the header of a real PNG
	// or JPEG has to parse, not merely start with the right bytes.
	_ "image/jpeg"
	_ "image/png"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

const (
	// Large enough for a photograph at avatar size, small enough that a row per
	// user never becomes a storage question.
	MaxAvatarBytes = 256 << 10
	// A header-only guard against a decompression bomb: a 40-byte PNG can claim
	// to be 60000x60000 and only the dimensions reveal it.
	maxAvatarPixels = 4096
)

// avatarTypes is an allowlist, and SVG is deliberately not on it: SVG is XML
// that can carry script, and this content is served back from the panel's own
// origin, where a script would run with the operator's session.
var avatarTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// SetAvatar validates an uploaded image and stores it. The content type is the
// one sniffed from the bytes, never the one the uploader declared — a caller
// that says "image/png" over a payload of something else gets rejected, and a
// caller that lies the other way cannot smuggle a type past the allowlist.
func (a *Authenticator) SetAvatar(ctx context.Context, userID string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", invalid("that file is empty")
	}
	if len(data) > MaxAvatarBytes {
		return "", invalid(fmt.Sprintf("an avatar is at most %d KB", MaxAvatarBytes>>10))
	}

	sniffed := http.DetectContentType(data)
	if !avatarTypes[sniffed] {
		return "", invalid("an avatar must be a PNG, JPEG or WebP image")
	}

	// WebP has no stdlib decoder; DetectContentType already matched its RIFF
	// container, which is the structural check available without a dependency.
	if sniffed != "image/webp" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", invalid("that file is not a readable image")
		}
		if cfg.Width > maxAvatarPixels || cfg.Height > maxAvatarPixels {
			return "", invalid(fmt.Sprintf("an avatar is at most %dx%d pixels", maxAvatarPixels, maxAvatarPixels))
		}
	}

	sum := sha256.Sum256(data)
	etag := hex.EncodeToString(sum[:16])
	if err := a.store.SetUserAvatar(ctx, userID, sniffed, data, etag); err != nil {
		return "", err
	}
	return etag, nil
}

// Avatar returns a user's photo. Callers must treat a not-found as "no photo",
// not as an error worth showing.
func (a *Authenticator) Avatar(ctx context.Context, userID string) (domain.Avatar, error) {
	return a.store.GetUserAvatar(ctx, userID)
}

// RemoveAvatar drops the photo; the initials take its place again.
func (a *Authenticator) RemoveAvatar(ctx context.Context, userID string) error {
	return a.store.DeleteUserAvatar(ctx, userID)
}
