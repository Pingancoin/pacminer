//go:build !windows

package main

import "fmt"

func listOpenCLDevices() error {
	return fmt.Errorf("OpenCL backend is only enabled in the Windows build")
}

func newOpenCLBackend(config) (miningBackend, error) {
	return nil, fmt.Errorf("OpenCL backend is only enabled in the Windows build")
}

func runOpenCLBenchmark(config) bool {
	return false
}
