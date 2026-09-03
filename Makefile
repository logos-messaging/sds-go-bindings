# Nimble resolves nim-sds and owns the library build; Go resolves the Go side;
# this Makefile links them together.

NIMBLE ?= nimble
GO ?= go

LIB_DIR ?= $(CURDIR)/build

ifeq ($(OS),Windows_NT)
    LIB_EXT ?= dll
else ifeq ($(shell uname -s),Darwin)
    LIB_EXT ?= dylib
else
    LIB_EXT ?= so
endif

LIB := $(LIB_DIR)/libsds.$(LIB_EXT)

# Windows resolves the DLL through PATH, and its linker has no -rpath.
ifneq ($(LIB_EXT),dll)
    LIB_RPATH := -Wl,-rpath,$(LIB_DIR)
endif

export CGO_CFLAGS  = -I$(LIB_DIR)
export CGO_LDFLAGS = -L$(LIB_DIR) -lsds $(LIB_RPATH)

.PHONY: deps libsds build test lint print-cgo clean

deps: nimble.paths ##@build Resolve the Nim dependencies

nimble.paths:
	$(NIMBLE) setup --localdeps -y

# nimble.paths is the resolution nimble actually settled on: it names the
# nim-sds package directory, and it carries the --path set the compile needs,
# since the package sits outside this tree where Nim finds no ancestor
# config.nims.
$(LIB): | nimble.paths
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

print-cgo: ##@build Print the cgo flags, for a caller that runs Go itself
	@echo 'CGO_CFLAGS=$(CGO_CFLAGS)'
	@echo 'CGO_LDFLAGS=$(CGO_LDFLAGS)'

clean:
	@rm -rf $(LIB_DIR) nimble.paths nimbledeps
