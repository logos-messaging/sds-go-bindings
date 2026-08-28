# Nimble resolves nim-sds and owns the library build; Go resolves the Go side;
# this Makefile links them together.

NIMBLE ?= nimble
GO ?= go

LIB_DIR ?= $(CURDIR)/build
LIB_EXT ?= $(if $(filter Darwin,$(shell uname -s)),dylib,so)
LIB := $(LIB_DIR)/libsds.$(LIB_EXT)

export CGO_CFLAGS  = -I$(LIB_DIR)
export CGO_LDFLAGS = -L$(LIB_DIR) -lsds -Wl,-rpath,$(LIB_DIR)

.PHONY: deps libsds build test lint clean

deps: nimble.paths ##@build Resolve the Nim dependencies

nimble.paths:
	$(NIMBLE) setup --localdeps -y

# nimble.paths is the resolution nimble actually settled on: it names the
# nim-sds package directory, and it carries the --path set the compile needs,
# since the package sits outside this tree where Nim finds no ancestor
# config.nims.
$(LIB): | nimble.paths
	SDS_PKG_DIR="$$(grep -om1 '/[^"]*/sds-[0-9][^"/]*' $(CURDIR)/nimble.paths)" \
		LIBSDS_OUT="$(LIB_DIR)" \
		NIM_PARAMS="$$NIM_PARAMS $$(tr '\n' ' ' < $(CURDIR)/nimble.paths)" \
		$(NIMBLE) libsds
	@test -f $@ || (echo "ERROR: $@ was not produced" && exit 1)

libsds: $(LIB) ##@build Build libsds from the Nimble dependency

build: $(LIB) ##@build Build the Go packages
	$(GO) build ./...

test: $(LIB) ##@test Run the Go tests; TEST=<name> to select one
	@if [ -z "$(TEST)" ]; then \
		$(GO) test -race ./...; \
	else \
		$(GO) test ./... -count=1 -run $(TEST) -v; \
	fi

lint: $(LIB) ##@test Vet and build the lint stubs
	$(GO) vet ./...
	$(GO) build -tags lint ./...

clean:
	@rm -rf $(LIB_DIR) nimble.paths nimbledeps
