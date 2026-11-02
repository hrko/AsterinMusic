package main

import (
	"syscall"
	"unsafe"
)

// The benchmark is sequential. Reuse storage to avoid counting clock allocations.
var counterValue int64
var counterFrequency int64
var performanceCounter = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceCounter")

func init() {
	f := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")
	ok, _, _ := f.Call(uintptr(unsafe.Pointer(&counterFrequency)))
	if ok == 0 || counterFrequency == 0 {
		panic("QueryPerformanceFrequency failed")
	}
}
func clockUS() float64 {
	ok, _, _ := performanceCounter.Call(uintptr(unsafe.Pointer(&counterValue)))
	if ok == 0 {
		panic("QueryPerformanceCounter failed")
	}
	return float64(counterValue) * 1e6 / float64(counterFrequency)
}
