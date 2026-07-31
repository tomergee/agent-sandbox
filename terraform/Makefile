default: build

.PHONY: build install lint test testacc docs kind-up kind-down

build:
	go build -o bin/terraform-provider-agentsandbox .

install:
	go install .

lint:
	golangci-lint run

test:
	go test ./... -count=1

testacc:
	TF_ACC=1 go test ./internal/... -v -timeout 30m

docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate

kind-up:
	kind create cluster --name agentsandbox-dev

kind-down:
	kind delete cluster --name agentsandbox-dev
