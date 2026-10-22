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
	go test -tags acceptance -count=1 -timeout 5m -run '^TestAcceptance' -v ./cmd/sortie-loop
