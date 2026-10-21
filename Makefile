# Local development helpers. Override on the command line, e.g. `make run ADDR=:9090`.
DB     ?= weft.db
ADDR   ?= :8080
DOMAIN ?= localhost
HNDB   ?= hackernews.db
HNPORT ?= 8090

.PHONY: help all build test run users user passwd token follow hackernews run-hn clean

help:
	@echo "make build                         build bin/weft (static, CGO off)"
	@echo "make test                          run all tests"
	@echo "make run                           build and serve on $(ADDR), users at <name>.$(DOMAIN)"
	@echo "make users                         list users in $(DB)"
	@echo "make user NAME=bob PASSWORD=...    create a user; prints the API token once"
	@echo "make passwd NAME=bob PASSWORD=...  set a login password"
	@echo "make token NAME=bob                issue a new API token (the old one stops working)"
	@echo "make follow NAME=dana TO='alice henry'  make a user follow others (bare names or feed URLs)"
	@echo "make all                            vet, run the tests, then build all binaries"
	@echo "make hackernews                     copy the Hacker News front page (stories, comments, authors, points) into a new $(HNDB)"
	@echo "make run-hn                        serve $(HNDB) on :$(HNPORT); log in as me / hackernews"
	@echo "make clean                         remove bin/ (not the database)"

all: test build

build:
	CGO_ENABLED=0 go build -o bin/weft ./cmd/weft
	CGO_ENABLED=0 go build -o bin/weft-agent ./cmd/weft-agent
	CGO_ENABLED=0 go build -o bin/hn-import ./cmd/hn-import

test:
	go vet ./...
	go test ./...

run: build
	./bin/weft serve -addr $(ADDR) -domain $(DOMAIN) -db $(DB) -dev -poll 30s $(if $(VERBOSE),-v)

users: build
	./bin/weft user list -db $(DB)

user: build
	@test -n "$(NAME)" || (echo "usage: make user NAME=bob PASSWORD=at-least-8-chars"; exit 2)
	./bin/weft user add -db $(DB) $(if $(PASSWORD),-password '$(PASSWORD)') $(NAME)

passwd: build
	@test -n "$(NAME)" -a -n "$(PASSWORD)" || (echo "usage: make passwd NAME=bob PASSWORD=at-least-8-chars"; exit 2)
	./bin/weft user passwd -db $(DB) -password '$(PASSWORD)' $(NAME)

token: build
	@test -n "$(NAME)" || (echo "usage: make token NAME=bob"; exit 2)
	./bin/weft user token -db $(DB) $(NAME)

follow: build
	@test -n "$(NAME)" -a -n "$(TO)" || (echo "usage: make follow NAME=dana TO='alice henry'  (servers must be running)"; exit 2)
	./bin/weft user follow -db $(DB) -domain $(DOMAIN) $(NAME) $(TO)

hackernews: build
	./bin/hn-import -db $(HNDB) -port $(HNPORT) -domain $(DOMAIN)

run-hn: build
	./bin/weft serve -addr :$(HNPORT) -domain $(DOMAIN) -db $(HNDB) -dev -poll 30s $(if $(VERBOSE),-v)

clean:
	rm -rf bin
