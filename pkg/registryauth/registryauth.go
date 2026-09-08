// Package registryauth renders a container-registry credential into the header
// value the Docker engine wants (registries.md; ADR-008 path 3).
//
// It lives in pkg rather than in the agent because the encoding is part of the
// contract between the plane's RegistryAuth message and the daemon, and two
// implementations of one header is how a pull starts failing on half the fleet.
package registryauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Encode returns the value for the `X-Registry-Auth` header the engine's
// /images/create and /images/{name}/push endpoints read: base64url of the JSON
// auth object.
//
// base64 URL encoding, not standard: the daemon accepts either, and the value
// travels in a header where '+' and '/' are needlessly risky.
//
// serverAddress must be the registry host the reference itself names — the
// daemon matches the credential against the reference's registry, so a
// mismatched address authenticates nothing and the pull fails anonymously.
func Encode(serverAddress, username, token string) (string, error) {
	if serverAddress == "" || token == "" {
		return "", nil
	}
	raw, err := json.Marshal(map[string]string{
		"username":      username,
		"password":      token,
		"serveraddress": serverAddress,
	})
	if err != nil {
		// The value never reaches a message: an error naming the credential is
		// the one way a token could end up in a log (ENGINEERING rule 20).
		return "", fmt.Errorf("registryauth: encoding credential for %s: %w", serverAddress, err)
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

// EncodeConfig returns the value for the `X-Registry-Config` header the
// engine's /build endpoint reads. It is a different shape from Encode's: /build
// may pull from several registries (a multi-stage Dockerfile), so it takes a
// MAP from registry host to credential rather than one credential.
//
// Only the one registry the plane sent is in the map. The daemon looks the
// host up by the reference's own prefix, so an unrelated FROM is unaffected —
// it simply finds no entry and pulls anonymously, exactly as it did before.
func EncodeConfig(serverAddress, username, token string) (string, error) {
	if serverAddress == "" || token == "" {
		return "", nil
	}
	raw, err := json.Marshal(map[string]map[string]string{
		serverAddress: {
			"username":      username,
			"password":      token,
			"serveraddress": serverAddress,
		},
	})
	if err != nil {
		return "", fmt.Errorf("registryauth: encoding build credentials for %s: %w", serverAddress, err)
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}
