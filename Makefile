.PHONY: verify test acceptance

verify:
	@test -z "$$(gofmt -l cmd internal)" || { echo "gofmt needed:"; gofmt -l cmd internal; exit 1; }
	go vet ./...
	go vet -tags acceptance ./...
	go build ./...
	$(MAKE) test
	$(MAKE) acceptance

test:
	go test ./...

acceptance:
	go test -tags acceptance -count=1 -timeout 14m -run '^TestAcceptance' -v ./cmd/jig

# Record the database schema of an engine release for the contract
# test: ENGINE is <repo>@<tag>, e.g. sortie-ai/sortie@v1.30.0. The new
# release needs a profile row (internal/gen/profile.go) and its schema
# fixture committed in the same pull request (section 7.8).
schema-fixture:
	@test -n "$(ENGINE)" || { echo "usage: make schema-fixture ENGINE=<repo>@<tag>"; exit 1; }
	go run ./internal/rollup/schemagen -engine "$(ENGINE)"
