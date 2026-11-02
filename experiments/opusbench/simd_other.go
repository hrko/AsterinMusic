//go:build !goexperiment.simd || !amd64

package main

func simdFeatures() map[string]bool { return map[string]bool{"experiment": false} }
