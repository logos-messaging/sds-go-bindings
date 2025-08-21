//go:build ignore
// +build ignore

package sds

// This file contains the go:generate directive for building SDS native code.

//go:generate sh -c "cd sds && make build"
