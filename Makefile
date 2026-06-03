.PHONY: proto build test vet fmt up down

# Regenerate gRPC/Protobuf stubs. Requires protoc + protoc-gen-go(-grpc) on PATH.
proto:
	protoc \
		--proto_path=proto \
		--proto_path=/usr/include \
		--go_out=. --go_opt=module=github.com/v-shah07/event-ticketing \
		--go-grpc_out=. --go-grpc_opt=module=github.com/v-shah07/event-ticketing \
		proto/analytics.proto

build:
	go build ./...

vet:
	go vet ./...

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*' -not -path './proto/analyticspb/*')

# Integration tests need Postgres + Redis reachable via DATABASE_URL / REDIS_ADDR.
test:
	go test ./... -count=1

up:
	docker compose up -d --build

down:
	docker compose down

# Validate the (never-applied) infrastructure manifests.
infra-validate:
	terraform -chdir=infra/terraform init -backend=false -input=false
	terraform -chdir=infra/terraform validate
	kubeconform -strict -summary infra/k8s/*.yaml
	helm lint infra/helm/event-ticketing
	helm template et infra/helm/event-ticketing | kubeconform -strict -summary
