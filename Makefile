EXECUTABLE := gorestapicmd
GITVERSION ?= 1.10.0-blksrv
GOPATH ?= ${HOME}/go
PACKAGENAME := $(shell go list -m -f '{{.Path}}')
TOOLS := ${GOPATH}/bin/swag
SWAGGERSOURCE = $(wildcard gorestapi/*.go) \
	$(wildcard gorestapi/mainrpc/*.go)

.PHONY: default
default: ${EXECUTABLE}

tools: ${TOOLS}

${GOPATH}/bin/swag:
	go install github.com/swaggo/swag/cmd/swag@latest

.PHONY: swagger
swagger: tools ${SWAGGERSOURCE}
	swag init --dir . --generalInfo gorestapi/swagger.go --exclude embed --output embed/public_html/v1/api-docs
	rm embed/public_html/v1/api-docs/docs.go
	
embed/public_html/v1/api-docs/swagger.json: tools ${SWAGGERSOURCE}
	swag init --dir . --generalInfo gorestapi/swagger.go --exclude embed --output embed/public_html/v1/api-docs
	rm embed/public_html/v1/api-docs/docs.go

.PHONY: ${EXECUTABLE}
${EXECUTABLE}: tools embed/public_html/v1/api-docs/swagger.json
	# Compiling...
	go mod tidy
	CGO_ENABLED=0 go build -ldflags "-X ${PACKAGENAME}/conf.Executable=${EXECUTABLE} -X ${PACKAGENAME}/conf.GitVersion=${GITVERSION}" -o ${EXECUTABLE}


.PHONY: test
test:
	go test -cover ./...

# Runs the SeaweedFS integration tests against a live S3 gateway, e.g.
#   make test-integration SEAWEED_TEST_ENDPOINT=localhost:8333 SEAWEED_TEST_ACCESS_KEY=... SEAWEED_TEST_SECRET_KEY=...
.PHONY: test-integration
test-integration:
	go test -count=1 -run Integration ./store/seaweed/

.PHONY: deps
deps:
	# Fetching dependancies...
	go get -d -v # Adding -u here will break CI

.PHONY: lint
lint:
	docker run --rm -v ${PWD}:/app -w /app golangci/golangci-lint:v1.27.0 golangci-lint run -v --timeout 5m

.PHONY: hadolint
hadolint:
	docker run -it --rm -v ${PWD}/Dockerfile:/Dockerfile hadolint/hadolint:latest hadolint --ignore DL3018 Dockerfile

.PHONY: relocate
relocate:
	@test ${TARGET} || ( echo ">> TARGET is not set. Use: make relocate TARGET=<target>"; exit 1 )
	$(eval ESCAPED_PACKAGENAME := $(shell echo "${PACKAGENAME}" | sed -e 's/[\/&]/\\&/g'))
	$(eval ESCAPED_TARGET := $(shell echo "${TARGET}" | sed -e 's/[\/&]/\\&/g'))
	# Renaming package ${PACKAGENAME} to ${TARGET}
	@grep -rlI '${PACKAGENAME}' * | xargs -i@ sed -i 's/${ESCAPED_PACKAGENAME}/${ESCAPED_TARGET}/g' @
	# Complete... 
	# NOTE: This does not update the git config nor will it update any imports of the root directory of this project.

