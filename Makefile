.PHONY: dev build vet fmt test test-unit keygen tidy migrate migrate-dry-run

# Default: everything that runs on the dev toolchain alone — no Docker, no
# network, no database. Covers the route guards, the token forgery cases,
# config validation and the entitlement rules. Run this constantly.
test: vet test-unit

test-unit:
	go test ./internal/... ./pkg/... -count=1

dev:
	go run main.go

build:
	go build -o build/tenantcore main.go

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal pkg main.go

tidy:
	go mod tidy

# Generate the Ed25519 signing keypair.
#
# The private half goes in THIS service's TOKEN_PRIVATE_KEY and nowhere else.
# The public half goes in every product service, or is fetched from
# GET /.well-known/tenantcore. Whoever holds the private key can mint a
# superadmin token that every product will accept.
keygen:
	go run ./cmd/keygen

# Copy the platform half of digitalservice's database into this one: tenants,
# platform users, packages-as-plans and subscriptions, every document keeping
# its own _id.
#
# Preserved ids are what lets the admin console read a tenant from here and
# its showcase content from digitalservice and know they are the same
# customer. Always run the dry run first.
migrate-dry-run:
	go run ./cmd/migrate-from-digitalservice -dry-run

migrate:
	go run ./cmd/migrate-from-digitalservice
