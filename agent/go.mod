module github.com/MaramHarsha/cypherpanel/agent

go 1.25.12

require (
	github.com/MaramHarsha/cypherpanel/pkg v0.0.0
	github.com/nats-io/nats.go v1.53.1
	github.com/robfig/cron/v3 v3.0.1
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/klauspost/compress v1.19.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260715232425-e75dac1f907d // indirect
)

replace github.com/MaramHarsha/cypherpanel/pkg => ../pkg
