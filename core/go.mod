module github.com/MaramHarsha/cypherpanel/core

go 1.25.12

require (
	github.com/MaramHarsha/cypherpanel/pkg v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/nats-io/nats-server/v2 v2.14.5
	github.com/nats-io/nats.go v1.53.1
	github.com/pressly/goose/v3 v3.27.3
	github.com/robfig/cron/v3 v3.0.1
	golang.org/x/crypto v0.55.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/antithesishq/antithesis-sdk-go v0.7.2-default-no-op // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/nats-io/jwt/v2 v2.8.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/sethvargo/go-retry v0.4.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260720211330-0afa2a65878a // indirect
)

replace github.com/MaramHarsha/cypherpanel/pkg => ../pkg
