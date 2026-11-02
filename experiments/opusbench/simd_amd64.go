//go:build goexperiment.simd && amd64

package main

import "simd/archsimd"

func simdFeatures() map[string]bool {
	return map[string]bool{"experiment": true, "avx": archsimd.X86.AVX(), "avx2": archsimd.X86.AVX2()}
}
