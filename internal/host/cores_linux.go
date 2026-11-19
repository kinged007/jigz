//go:build linux

package host

import "runtime"

// cores is the number of logical CPUs the percentage is measured against.
func cores() int { return runtime.NumCPU() }
