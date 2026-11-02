//go:build !cgo

package main

import "errors"

func nativeEncoder() (codec, error) { return codec{}, errors.New("reference requires cgo") }
func referenceDecoder() (func([]byte, []float32) (int, error), func(), error) {
	return nil, nil, errors.New("reference requires cgo")
}
func nativeVersion() string { return "disabled" }
