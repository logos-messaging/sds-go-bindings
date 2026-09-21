# Nimble resolves nim-sds and owns the library build; Go resolves the Go side;
# this Makefile links them together.

NIMBLE ?= nimble
GO ?= go

LIB_DIR ?= $(CURDIR)/build

LIB_EXT ?= $(if $(filter Windows_NT,$(OS)),dll,$(if $(filter Darwin,$(shell uname -s)),dylib,so))

LIB := $(LIB_DIR)/libsds.$(LIB_EXT)

# Windows has no -rpath; the loader uses PATH.
ifneq ($(LIB_EXT),dll)
    LIB_RPATH := -Wl,-rpath,$(LIB_DIR)
endif

export CGO_CFLAGS  = -I$(LIB_DIR)
export CGO_LDFLAGS = -L$(LIB_DIR) -lsds $(LIB_RPATH)

.PHONY: deps libsds build test lint print-cgo clean

# Always re-resolves: a restored nimbledeps is only an accelerator.
deps: ##@build Resolve the Nim dependencies
	$(NIMBLE) setup --localdeps -y

nimble.paths:
	$(NIMBLE) setup --localdeps -y

# nim-sds installs outside this tree, so the compile needs nimble.paths.
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
