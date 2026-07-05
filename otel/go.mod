module github.com/deepteams/gage/otel

go 1.26.3

require (
	github.com/deepteams/gage v0.0.0
	go.opentelemetry.io/otel v1.36.0
	go.opentelemetry.io/otel/sdk v1.36.0
	go.opentelemetry.io/otel/trace v1.36.0
)

require (
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel/metric v1.36.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// In-repo development builds against the sibling checkout; once the
// repository is tagged, consumers resolve github.com/deepteams/gage by
// version and this replace only affects builds inside this repository.
replace github.com/deepteams/gage => ../
