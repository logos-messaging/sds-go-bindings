# Makefile for SDS Go Bindings

# The NIM_SDS_LIB_PATH and NIM_SDS_HEADER_PATH env vars should be defined beforehand
# Therefore, we assume the libsds library and headers are located in the specified paths
export CGO_CFLAGS="-I${NIM_SDS_HEADER_PATH}/"
export CGO_LDFLAGS="-L${NIM_SDS_LIB_PATH}/ -lsds -Wl,-rpath,${NIM_SDS_LIB_PATH}/"

# Expected files
HEADER_FILE := $(NIM_SDS_HEADER_PATH)/libsds.h
LIB_FILES   := $(wildcard $(NIM_SDS_LIB_PATH)/libsds.*)

.PHONY: all clean prepare build-libsds build

# Default target
all: build

# Validate necessary folders and files
check-folders:
	@echo Checking header directory ...
	@if [ -z "$(NIM_SDS_HEADER_PATH)" ]; then \
		echo "ERROR: NIM_SDS_HEADER_PATH not set"; exit 1; \
	fi
	@if [ ! -d "$(NIM_SDS_HEADER_PATH)" ]; then \
		echo "ERROR: Header path does not exist: $(NIM_SDS_HEADER_PATH)"; exit 1; \
	fi

	@echo Checking lib directory ...
	@if [ -z "$(NIM_SDS_LIB_PATH)" ]; then \
		echo "ERROR: NIM_SDS_LIB_PATH not set"; exit 1; \
	fi
	@if [ ! -d "$(NIM_SDS_LIB_PATH)" ]; then \
		echo "ERROR: Library path does not exist: $(NIM_SDS_LIB_PATH)"; exit 1; \
	fi

	@echo Checking for libsds.h ...
	@if [ ! -f "$(HEADER_FILE)" ]; then \
		echo "ERROR: libsds.h not found at: $(HEADER_FILE)"; exit 1; \
	fi

	@echo Checking for libsds library file ...
	@if [ -z "$(LIB_FILES)" ]; then \
		echo "ERROR: No libsds library file found in: $(NIM_SDS_LIB_PATH)"; exit 1; \
	fi

# Build SDS Go Bindings
build: check-folders
	@echo "Building SDS Go Bindings..."
	go build ./...

# Clean up generated files
clean:
	@echo "Cleaning up..."
	@rm -f sds-go-bindings
