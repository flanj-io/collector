module github.com/vinifera-io/collector/processor/viniferaredaction

go 1.26.0

require (
	github.com/vinifera-io/collector v0.0.0
	go.opentelemetry.io/collector/component v1.65.0
	go.opentelemetry.io/collector/consumer v1.65.0
	go.opentelemetry.io/collector/pdata v1.65.0
	go.opentelemetry.io/collector/processor v1.65.0
	go.opentelemetry.io/collector/processor/processorhelper v0.159.0
)

require (
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/nyaruka/phonenumbers v1.8.1 // indirect
	go.opentelemetry.io/collector/featuregate v1.65.0 // indirect
	go.opentelemetry.io/collector/internal/componentalias v0.159.0 // indirect
	go.opentelemetry.io/collector/pipeline v1.65.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Local source wiring — the ocb-generated distribution module re-declares this
// (Go ignores replaces in non-main modules); it exists here for standalone
// `go build`/`go vet` of this component during development.
replace github.com/vinifera-io/collector => ../../
