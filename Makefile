SERVICES := expenses

.PHONY: test vet fmt run-expenses

test:
	@for s in $(SERVICES); do (cd services/$$s && go test -race ./...) || exit 1; done

vet:
	@for s in $(SERVICES); do (cd services/$$s && go vet ./...) || exit 1; done

fmt:
	gofmt -w services

run-expenses:
	cd services/expenses && go run ./cmd/expenses
