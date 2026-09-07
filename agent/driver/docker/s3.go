package docker

// The agent's S3 client is the shared one in pkg/s3 (AWS Signature Version 4
// over net/http, no SDK). It lives there because BOTH planes write to S3 — the
// agent takes database and volume backups, and the control plane archives log
// lines — and two implementations of a signing algorithm is two places for a
// subtle bug that only shows up against one provider.

import "github.com/MaramHarsha/cypherpanel/pkg/s3"

// RealS3Client is the shared client, kept under this name so the driver's own
// wiring and its tests read unchanged.
type RealS3Client = s3.Client

// NewS3Client wires a new S3 client.
func NewS3Client() *RealS3Client { return s3.New() }
