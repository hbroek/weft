# Local development helpers. Override on the command line, e.g. `make run ADDR=:9090`.
DATA   ?= weft
ADDR   ?= :8080
DOMAIN ?= localhost
HNDATA ?= hackernews
HNPORT ?= 8090

.PHONY: help all build test run users user passwd token follow hackernews run-hn clean

help:
	@echo "make build                         build bin/weft (static, CGO off)"
	@echo "make test                          run all tests"
	@echo "make run                           build and serve on $(ADDR), users at <name>.$(DOMAIN)"
	@echo "make users                         list users in $(DATA)"
	@echo "make user NAME=bob PASSWORD=...    create a user; prints the API token once"
	@echo "make passwd NAME=bob PASSWORD=...  set a login password"
	@echo "make token NAME=bob                issue a new API token (the old one stops working)"
	@echo "make follow NAME=dana TO='alice henry'  make a user follow others (bare names or feed URLs)"
	@echo "make all                            vet, run the tests, then build all binaries"
	@echo "make hackernews                     copy the Hacker News front page (stories, comments, authors, points) into a new $(HNDATA)"
	@echo "make run-hn                        serve $(HNDATA) on :$(HNPORT); log in as me / hackernews"
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
	./bin/weft serve -addr $(ADDR) -domain $(DOMAIN) -data $(DATA) -dev -poll 30s $(if $(VERBOSE),-v)

users: build
	./bin/weft user list -data $(DATA)

user: build
	@test -n "$(NAME)" || (echo "usage: make user NAME=bob PASSWORD=at-least-8-chars"; exit 2)
	./bin/weft user add -data $(DATA) $(if $(PASSWORD),-password '$(PASSWORD)') $(NAME)

passwd: build
	@test -n "$(NAME)" -a -n "$(PASSWORD)" || (echo "usage: make passwd NAME=bob PASSWORD=at-least-8-chars"; exit 2)
	./bin/weft user passwd -data $(DATA) -password '$(PASSWORD)' $(NAME)

token: build
	@test -n "$(NAME)" || (echo "usage: make token NAME=bob"; exit 2)
	./bin/weft user token -data $(DATA) $(NAME)

follow: build
	@test -n "$(NAME)" -a -n "$(TO)" || (echo "usage: make follow NAME=dana TO='alice henry'  (servers must be running)"; exit 2)
	./bin/weft user follow -data $(DATA) -domain $(DOMAIN) $(NAME) $(TO)

hackernews: build
	./bin/hn-import -data $(HNDATA) -port $(HNPORT) -domain $(DOMAIN)

run-hn: build
	./bin/weft serve -addr :$(HNPORT) -domain $(DOMAIN) -data $(HNDATA) -dev -poll 30s $(if $(VERBOSE),-v)

clean:
	rm -rf bin
