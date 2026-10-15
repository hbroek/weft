# Local development helpers. Override on the command line, e.g. `make run ADDR=:9090`.
DB     ?= ttc.db
ADDR   ?= :8080
DOMAIN ?= localhost

.PHONY: help build test run users user passwd token clean

help:
	@echo "make build                         build bin/ttc (static, CGO off)"
	@echo "make test                          run all tests"
	@echo "make run                           build and serve on $(ADDR), users at <name>.$(DOMAIN)"
	@echo "make users                         list users in $(DB)"
	@echo "make user NAME=bob PASSWORD=...    create a user; prints the API token once"
	@echo "make passwd NAME=bob PASSWORD=...  set a login password"
	@echo "make token NAME=bob                issue a new API token (the old one stops working)"
	@echo "make clean                         remove bin/ (not the database)"

build:
	CGO_ENABLED=0 go build -o bin/ttc ./cmd/ttc
	CGO_ENABLED=0 go build -o bin/ttc-agent ./cmd/ttc-agent

test:
	go vet ./...
	go test ./...

run: build
	./bin/ttc serve -addr $(ADDR) -domain $(DOMAIN) -db $(DB) -dev $(if $(VERBOSE),-v)

users: build
	./bin/ttc user list -db $(DB)

user: build
	@test -n "$(NAME)" || (echo "usage: make user NAME=bob PASSWORD=at-least-8-chars"; exit 2)
	./bin/ttc user add -db $(DB) $(if $(PASSWORD),-password '$(PASSWORD)') $(NAME)

passwd: build
	@test -n "$(NAME)" -a -n "$(PASSWORD)" || (echo "usage: make passwd NAME=bob PASSWORD=at-least-8-chars"; exit 2)
	./bin/ttc user passwd -db $(DB) -password '$(PASSWORD)' $(NAME)

token: build
	@test -n "$(NAME)" || (echo "usage: make token NAME=bob"; exit 2)
	./bin/ttc user token -db $(DB) $(NAME)

clean:
	rm -rf bin
