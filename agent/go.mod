module github.com/MaramHarsha/cypherpanel/agent

go 1.25.12

require (
	github.com/MaramHarsha/cypherpanel/pkg v0.0.0
	github.com/nats-io/nats-server/v2 v2.14.3
	github.com/nats-io/nats.go v1.52.0
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/antithesishq/antithesis-sdk-go v0.7.2 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/klauspost/compress v1.19.0 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/nats-io/jwt/v2 v2.8.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260715232425-e75dac1f907d // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/MaramHarsha/cypherpanel/pkg => ../pkg
