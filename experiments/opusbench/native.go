//go:build cgo

package main

/*
#cgo pkg-config: opus
#include <opus.h>
static int setup(OpusEncoder *e) { return opus_encoder_ctl(e, OPUS_SET_BITRATE(96000)); }
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func nativeEncoder() (codec, error) {
	var code C.int
	e := C.opus_encoder_create(48000, 2, C.OPUS_APPLICATION_AUDIO, &code)
	if e == nil {
		return codec{}, fmt.Errorf("create: %d", code)
	}
	if code = C.setup(e); code != 0 {
		C.opus_encoder_destroy(e)
		return codec{}, fmt.Errorf("setup: %d", code)
	}
	return codec{encode: func(p []float32, b []byte) (int, error) {
		n := int(C.opus_encode_float(e, (*C.float)(unsafe.Pointer(&p[0])), 960, (*C.uchar)(unsafe.Pointer(&b[0])), C.opus_int32(len(b))))
		if n < 0 {
			return 0, fmt.Errorf("encode: %d", n)
		}
		return n, nil
	}, close: func() { C.opus_encoder_destroy(e) }}, nil
}
func referenceDecoder() (func([]byte, []float32) (int, error), func(), error) {
	var code C.int
	d := C.opus_decoder_create(48000, 2, &code)
	if d == nil {
		return nil, nil, fmt.Errorf("decoder: %d", code)
	}
	return func(b []byte, p []float32) (int, error) {
		n := int(C.opus_decode_float(d, (*C.uchar)(unsafe.Pointer(&b[0])), C.opus_int32(len(b)), (*C.float)(unsafe.Pointer(&p[0])), 960, 0))
		if n < 0 {
			return n, fmt.Errorf("decode: %d", n)
		}
		return n, nil
	}, func() { C.opus_decoder_destroy(d) }, nil
}
func nativeVersion() string { return C.GoString(C.opus_get_version_string()) }
