//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type clPlatform uintptr
type clDevice uintptr
type clContext uintptr
type clQueue uintptr
type clProgram uintptr
type clKernel uintptr
type clMem uintptr

const (
	clSuccess = 0

	clDeviceTypeAll = 0xffffffff

	clPlatformName = 0x0902
	clDeviceName   = 0x102b

	clProgramBuildLog = 0x1183

	clMemReadWrite = 1 << 0
	clMemWriteOnly = 1 << 1
	clMemReadOnly  = 1 << 2
)

var openCL = struct {
	dll                     *syscall.LazyDLL
	getPlatformIDs          *syscall.LazyProc
	getPlatformInfo         *syscall.LazyProc
	getDeviceIDs            *syscall.LazyProc
	getDeviceInfo           *syscall.LazyProc
	createContext           *syscall.LazyProc
	createCommandQueue      *syscall.LazyProc
	createProgramWithSource *syscall.LazyProc
	buildProgram            *syscall.LazyProc
	getProgramBuildInfo     *syscall.LazyProc
	createKernel            *syscall.LazyProc
	createBuffer            *syscall.LazyProc
	setKernelArg            *syscall.LazyProc
	enqueueWriteBuffer      *syscall.LazyProc
	enqueueReadBuffer       *syscall.LazyProc
	enqueueNDRangeKernel    *syscall.LazyProc
	finish                  *syscall.LazyProc
	releaseMemObject        *syscall.LazyProc
	releaseKernel           *syscall.LazyProc
	releaseProgram          *syscall.LazyProc
	releaseCommandQueue     *syscall.LazyProc
	releaseContext          *syscall.LazyProc
}{
	dll: syscall.NewLazyDLL("OpenCL.dll"),
}

type openCLDeviceInfo struct {
	platform clPlatform
	device   clDevice
	name     string
}

type openCLBackend struct {
	deviceIndex int
	workSize    int
	deviceName  string

	context clContext
	queue   clQueue
	program clProgram
	kernel  clKernel

	headerMem clMem
	targetMem clMem
	foundMem  clMem
	nonceMem  clMem
	hashMem   clMem
}

func listOpenCLDevices() error {
	if err := loadOpenCL(); err != nil {
		return err
	}
	devices, err := enumerateOpenCLDevices()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return fmt.Errorf("no OpenCL devices found")
	}
	for i, dev := range devices {
		fmt.Printf("[%d] %s\n", i, dev.name)
	}
	return nil
}

func newOpenCLBackend(cfg config) (miningBackend, error) {
	if err := loadOpenCL(); err != nil {
		return nil, err
	}
	devices, err := enumerateOpenCLDevices()
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no OpenCL devices found; install the Intel Arc graphics driver")
	}
	if cfg.device < 0 || cfg.device >= len(devices) {
		return nil, fmt.Errorf("OpenCL device %d is out of range; run --list-devices", cfg.device)
	}
	workSize := cfg.workSize
	if workSize <= 0 {
		workSize = defaultOpenCLWorkSize
	}
	b := &openCLBackend{
		deviceIndex: cfg.device,
		workSize:    workSize,
		deviceName:  devices[cfg.device].name,
	}
	if err := b.init(devices[cfg.device]); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *openCLBackend) Name() string {
	return fmt.Sprintf("opencl[%d] %s", b.deviceIndex, b.deviceName)
}

func (b *openCLBackend) Start(ctx context.Context, state *miningState, shares chan<- share, stats *counters) error {
	defer b.Close()
	var seen uint64
	nonce := uint32(time.Now().UnixNano())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		job := state.snapshot()
		if job == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if job.seq != seen {
			seen = job.seq
			nonce = uint32(time.Now().UnixNano())
		}

		foundNonce, foundHash, found, err := b.scan(job, nonce, uint32(b.workSize))
		if err != nil {
			return err
		}
		stats.hashes.Add(uint64(b.workSize))
		if found && state.currentSeq() == seen {
			sh := share{
				jobSeq:   job.seq,
				jobID:    job.id,
				ntimeHex: job.ntimeHex,
				nonceHex: fmt.Sprintf("%08x", foundNonce),
				nonce:    foundNonce,
				hash:     hex.EncodeToString(foundHash[:]),
			}
			select {
			case shares <- sh:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		nonce += uint32(b.workSize)
	}
}

func (b *openCLBackend) Close() {
	if b.headerMem != 0 {
		openCL.releaseMemObject.Call(uintptr(b.headerMem))
		b.headerMem = 0
	}
	if b.targetMem != 0 {
		openCL.releaseMemObject.Call(uintptr(b.targetMem))
		b.targetMem = 0
	}
	if b.foundMem != 0 {
		openCL.releaseMemObject.Call(uintptr(b.foundMem))
		b.foundMem = 0
	}
	if b.nonceMem != 0 {
		openCL.releaseMemObject.Call(uintptr(b.nonceMem))
		b.nonceMem = 0
	}
	if b.hashMem != 0 {
		openCL.releaseMemObject.Call(uintptr(b.hashMem))
		b.hashMem = 0
	}
	if b.kernel != 0 {
		openCL.releaseKernel.Call(uintptr(b.kernel))
		b.kernel = 0
	}
	if b.program != 0 {
		openCL.releaseProgram.Call(uintptr(b.program))
		b.program = 0
	}
	if b.queue != 0 {
		openCL.releaseCommandQueue.Call(uintptr(b.queue))
		b.queue = 0
	}
	if b.context != 0 {
		openCL.releaseContext.Call(uintptr(b.context))
		b.context = 0
	}
}

func (b *openCLBackend) init(dev openCLDeviceInfo) error {
	var errCode int32
	context, _, _ := openCL.createContext.Call(0, 1, uintptr(unsafe.Pointer(&dev.device)), 0, 0, uintptr(unsafe.Pointer(&errCode)))
	if errCode != clSuccess {
		return fmt.Errorf("clCreateContext failed: %d", errCode)
	}
	b.context = clContext(context)

	queue, _, _ := openCL.createCommandQueue.Call(uintptr(b.context), uintptr(dev.device), 0, uintptr(unsafe.Pointer(&errCode)))
	if errCode != clSuccess {
		return fmt.Errorf("clCreateCommandQueue failed: %d", errCode)
	}
	b.queue = clQueue(queue)

	src := append([]byte(openCLKernelSource), 0)
	srcPtr := uintptr(unsafe.Pointer(&src[0]))
	srcLen := uintptr(len(src) - 1)
	program, _, _ := openCL.createProgramWithSource.Call(uintptr(b.context), 1, uintptr(unsafe.Pointer(&srcPtr)), uintptr(unsafe.Pointer(&srcLen)), uintptr(unsafe.Pointer(&errCode)))
	if errCode != clSuccess {
		return fmt.Errorf("clCreateProgramWithSource failed: %d", errCode)
	}
	b.program = clProgram(program)

	build, _, _ := openCL.buildProgram.Call(uintptr(b.program), 1, uintptr(unsafe.Pointer(&dev.device)), 0, 0, 0)
	if int32(build) != clSuccess {
		return fmt.Errorf("clBuildProgram failed: %d\n%s", int32(build), b.buildLog(dev.device))
	}

	name := append([]byte("mine_blake256\x00"), 0)
	kernel, _, _ := openCL.createKernel.Call(uintptr(b.program), uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&errCode)))
	if errCode != clSuccess {
		return fmt.Errorf("clCreateKernel failed: %d", errCode)
	}
	b.kernel = clKernel(kernel)

	var err error
	if b.headerMem, err = b.createBuffer(clMemReadOnly, headerLength); err != nil {
		return err
	}
	if b.targetMem, err = b.createBuffer(clMemReadOnly, 32); err != nil {
		return err
	}
	if b.foundMem, err = b.createBuffer(clMemReadWrite, 4); err != nil {
		return err
	}
	if b.nonceMem, err = b.createBuffer(clMemWriteOnly, 4); err != nil {
		return err
	}
	if b.hashMem, err = b.createBuffer(clMemWriteOnly, 32); err != nil {
		return err
	}
	return nil
}

func (b *openCLBackend) scan(job *miningJob, startNonce uint32, count uint32) (uint32, [32]byte, bool, error) {
	if err := b.writeBuffer(b.headerMem, job.header); err != nil {
		return 0, [32]byte{}, false, err
	}
	if err := b.writeBuffer(b.targetMem, job.shareTarget[:]); err != nil {
		return 0, [32]byte{}, false, err
	}
	zero := [4]byte{}
	if err := b.writeBuffer(b.foundMem, zero[:]); err != nil {
		return 0, [32]byte{}, false, err
	}

	args := []struct {
		size uintptr
		ptr  uintptr
	}{
		{unsafe.Sizeof(b.headerMem), uintptr(unsafe.Pointer(&b.headerMem))},
		{unsafe.Sizeof(b.targetMem), uintptr(unsafe.Pointer(&b.targetMem))},
		{unsafe.Sizeof(startNonce), uintptr(unsafe.Pointer(&startNonce))},
		{unsafe.Sizeof(count), uintptr(unsafe.Pointer(&count))},
		{unsafe.Sizeof(b.foundMem), uintptr(unsafe.Pointer(&b.foundMem))},
		{unsafe.Sizeof(b.nonceMem), uintptr(unsafe.Pointer(&b.nonceMem))},
		{unsafe.Sizeof(b.hashMem), uintptr(unsafe.Pointer(&b.hashMem))},
	}
	for i, arg := range args {
		rc, _, _ := openCL.setKernelArg.Call(uintptr(b.kernel), uintptr(i), arg.size, arg.ptr)
		if int32(rc) != clSuccess {
			return 0, [32]byte{}, false, fmt.Errorf("clSetKernelArg(%d) failed: %d", i, int32(rc))
		}
	}

	global := uintptr(count)
	rc, _, _ := openCL.enqueueNDRangeKernel.Call(uintptr(b.queue), uintptr(b.kernel), 1, 0, uintptr(unsafe.Pointer(&global)), 0, 0, 0, 0)
	if int32(rc) != clSuccess {
		return 0, [32]byte{}, false, fmt.Errorf("clEnqueueNDRangeKernel failed: %d", int32(rc))
	}
	if rc, _, _ = openCL.finish.Call(uintptr(b.queue)); int32(rc) != clSuccess {
		return 0, [32]byte{}, false, fmt.Errorf("clFinish failed: %d", int32(rc))
	}

	foundBytes := [4]byte{}
	if err := b.readBuffer(b.foundMem, foundBytes[:]); err != nil {
		return 0, [32]byte{}, false, err
	}
	if binary.LittleEndian.Uint32(foundBytes[:]) == 0 {
		return 0, [32]byte{}, false, nil
	}
	nonceBytes := [4]byte{}
	if err := b.readBuffer(b.nonceMem, nonceBytes[:]); err != nil {
		return 0, [32]byte{}, false, err
	}
	var hash [32]byte
	if err := b.readBuffer(b.hashMem, hash[:]); err != nil {
		return 0, [32]byte{}, false, err
	}
	if bytes.Compare(hash[:], job.shareTarget[:]) > 0 {
		return 0, [32]byte{}, false, nil
	}
	return binary.LittleEndian.Uint32(nonceBytes[:]), hash, true, nil
}

func (b *openCLBackend) createBuffer(flags uintptr, size int) (clMem, error) {
	var errCode int32
	mem, _, _ := openCL.createBuffer.Call(uintptr(b.context), flags, uintptr(size), 0, uintptr(unsafe.Pointer(&errCode)))
	if errCode != clSuccess {
		return 0, fmt.Errorf("clCreateBuffer(%d) failed: %d", size, errCode)
	}
	return clMem(mem), nil
}

func (b *openCLBackend) writeBuffer(mem clMem, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	rc, _, _ := openCL.enqueueWriteBuffer.Call(uintptr(b.queue), uintptr(mem), 1, 0, uintptr(len(data)), uintptr(unsafe.Pointer(&data[0])), 0, 0, 0)
	if int32(rc) != clSuccess {
		return fmt.Errorf("clEnqueueWriteBuffer failed: %d", int32(rc))
	}
	return nil
}

func (b *openCLBackend) readBuffer(mem clMem, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	rc, _, _ := openCL.enqueueReadBuffer.Call(uintptr(b.queue), uintptr(mem), 1, 0, uintptr(len(data)), uintptr(unsafe.Pointer(&data[0])), 0, 0, 0)
	if int32(rc) != clSuccess {
		return fmt.Errorf("clEnqueueReadBuffer failed: %d", int32(rc))
	}
	return nil
}

func (b *openCLBackend) buildLog(device clDevice) string {
	var size uintptr
	openCL.getProgramBuildInfo.Call(uintptr(b.program), uintptr(device), clProgramBuildLog, 0, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	openCL.getProgramBuildInfo.Call(uintptr(b.program), uintptr(device), clProgramBuildLog, size, uintptr(unsafe.Pointer(&buf[0])), 0)
	return strings.TrimRight(string(buf), "\x00")
}

func loadOpenCL() error {
	if err := openCL.dll.Load(); err != nil {
		return fmt.Errorf("OpenCL.dll not found; install the Intel Arc graphics driver: %w", err)
	}
	openCL.getPlatformIDs = openCL.dll.NewProc("clGetPlatformIDs")
	openCL.getPlatformInfo = openCL.dll.NewProc("clGetPlatformInfo")
	openCL.getDeviceIDs = openCL.dll.NewProc("clGetDeviceIDs")
	openCL.getDeviceInfo = openCL.dll.NewProc("clGetDeviceInfo")
	openCL.createContext = openCL.dll.NewProc("clCreateContext")
	openCL.createCommandQueue = openCL.dll.NewProc("clCreateCommandQueue")
	openCL.createProgramWithSource = openCL.dll.NewProc("clCreateProgramWithSource")
	openCL.buildProgram = openCL.dll.NewProc("clBuildProgram")
	openCL.getProgramBuildInfo = openCL.dll.NewProc("clGetProgramBuildInfo")
	openCL.createKernel = openCL.dll.NewProc("clCreateKernel")
	openCL.createBuffer = openCL.dll.NewProc("clCreateBuffer")
	openCL.setKernelArg = openCL.dll.NewProc("clSetKernelArg")
	openCL.enqueueWriteBuffer = openCL.dll.NewProc("clEnqueueWriteBuffer")
	openCL.enqueueReadBuffer = openCL.dll.NewProc("clEnqueueReadBuffer")
	openCL.enqueueNDRangeKernel = openCL.dll.NewProc("clEnqueueNDRangeKernel")
	openCL.finish = openCL.dll.NewProc("clFinish")
	openCL.releaseMemObject = openCL.dll.NewProc("clReleaseMemObject")
	openCL.releaseKernel = openCL.dll.NewProc("clReleaseKernel")
	openCL.releaseProgram = openCL.dll.NewProc("clReleaseProgram")
	openCL.releaseCommandQueue = openCL.dll.NewProc("clReleaseCommandQueue")
	openCL.releaseContext = openCL.dll.NewProc("clReleaseContext")
	return nil
}

func enumerateOpenCLDevices() ([]openCLDeviceInfo, error) {
	var platformCount uint32
	rc, _, _ := openCL.getPlatformIDs.Call(0, 0, uintptr(unsafe.Pointer(&platformCount)))
	if int32(rc) != clSuccess {
		return nil, fmt.Errorf("clGetPlatformIDs count failed: %d", int32(rc))
	}
	platforms := make([]clPlatform, platformCount)
	if platformCount > 0 {
		rc, _, _ = openCL.getPlatformIDs.Call(uintptr(platformCount), uintptr(unsafe.Pointer(&platforms[0])), 0)
		if int32(rc) != clSuccess {
			return nil, fmt.Errorf("clGetPlatformIDs failed: %d", int32(rc))
		}
	}
	var out []openCLDeviceInfo
	for _, platform := range platforms {
		var deviceCount uint32
		rc, _, _ = openCL.getDeviceIDs.Call(uintptr(platform), clDeviceTypeAll, 0, 0, uintptr(unsafe.Pointer(&deviceCount)))
		if int32(rc) != clSuccess || deviceCount == 0 {
			continue
		}
		devices := make([]clDevice, deviceCount)
		rc, _, _ = openCL.getDeviceIDs.Call(uintptr(platform), clDeviceTypeAll, uintptr(deviceCount), uintptr(unsafe.Pointer(&devices[0])), 0)
		if int32(rc) != clSuccess {
			continue
		}
		platformName := clString(openCL.getPlatformInfo, uintptr(platform), clPlatformName)
		for _, device := range devices {
			name := clString(openCL.getDeviceInfo, uintptr(device), clDeviceName)
			if platformName != "" {
				name = platformName + " / " + name
			}
			out = append(out, openCLDeviceInfo{platform: platform, device: device, name: name})
		}
	}
	return out, nil
}

func clString(proc *syscall.LazyProc, object uintptr, param uintptr) string {
	var size uintptr
	proc.Call(object, param, 0, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	proc.Call(object, param, size, uintptr(unsafe.Pointer(&buf[0])), 0)
	return strings.TrimRight(string(buf), "\x00")
}

func runOpenCLBenchmark(cfg config) bool {
	if cfg.workSize <= 0 {
		cfg.workSize = defaultOpenCLWorkSize
	}
	if cfg.benchmarkSecs <= 0 {
		cfg.benchmarkSecs = 10
	}
	backend, err := newOpenCLBackend(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pacminer:", err)
		return true
	}
	b := backend.(*openCLBackend)
	defer b.Close()

	header := make([]byte, headerLength)
	binary.LittleEndian.PutUint32(header[headerTimestampOffset:headerTimestampOffset+4], uint32(time.Now().Unix()))
	binary.LittleEndian.PutUint32(header[headerBitsOffset:headerBitsOffset+4], 0x207fffff)
	binary.LittleEndian.PutUint32(header[headerHeightOffset:headerHeightOffset+4], 1)
	job := &miningJob{
		seq:         1,
		id:          "benchmark",
		ntimeHex:    fmt.Sprintf("%08x", uint32(time.Now().Unix())),
		header:      header,
		shareTarget: [32]byte{},
		difficulty:  1,
	}

	deadline := time.Now().Add(time.Duration(cfg.benchmarkSecs) * time.Second)
	start := time.Now()
	nonce := uint32(time.Now().UnixNano())
	var hashes uint64
	for time.Now().Before(deadline) {
		if _, _, _, err := b.scan(job, nonce, uint32(cfg.workSize)); err != nil {
			fmt.Fprintln(os.Stderr, "pacminer:", err)
			return true
		}
		hashes += uint64(cfg.workSize)
		nonce += uint32(cfg.workSize)
	}
	elapsed := time.Since(start).Seconds()
	fmt.Printf("benchmark backend=%s worksize=%d hashes=%d elapsed=%.2fs rate=%s\n",
		b.Name(), cfg.workSize, hashes, elapsed, formatHashrate(float64(hashes)/elapsed))
	return true
}
