package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/big"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/decred/dcrd/crypto/blake256"
)

const (
	appVersion = "0.3.2"

	headerBitsOffset      = 116
	headerHeightOffset    = 128
	headerTimestampOffset = 136
	headerNonceOffset     = 140
	headerExtraDataOffset = 144
	headerLength          = 180

	defaultPool                     = "stratum.pingancoin.org:3333"
	defaultOpenCLWorkSize           = 1 << 26
	defaultSuggestedShareDifficulty = 100_000_000
)

type config struct {
	poolURL       string
	username      string
	password      string
	backend       string
	device        int
	threads       int
	workSize      int
	suggestDiff   float64
	benchmark     bool
	benchmarkSecs int
	listDevices   bool
	showVersion   bool
	verbose       bool
}

type rpcMessage struct {
	ID     json.RawMessage   `json:"id,omitempty"`
	Method string            `json:"method,omitempty"`
	Params []json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage   `json:"result,omitempty"`
	Error  json.RawMessage   `json:"error,omitempty"`
}

type stratumClient struct {
	conn   net.Conn
	writer *bufio.Writer
	mu     sync.Mutex
	nextID atomic.Int64
}

type miningState struct {
	mu         sync.RWMutex
	job        *miningJob
	difficulty float64
	seq        atomic.Uint64
}

type miningJob struct {
	seq         uint64
	id          string
	bitsHex     string
	ntimeHex    string
	ntime       uint32
	header      []byte
	shareTarget [32]byte
	difficulty  float64
}

type share struct {
	jobSeq   uint64
	jobID    string
	extraHex string
	ntimeHex string
	nonceHex string
	nonce    uint32
	hash     string
}

type counters struct {
	hashes   atomic.Uint64
	accepted atomic.Uint64
	rejected atomic.Uint64
	submits  atomic.Uint64
}

func (j *miningJob) withExtraNonce(extra uint64) (*miningJob, string) {
	extraHex := extraNonceHex(extra)
	if j == nil {
		return nil, extraHex
	}
	clone := *j
	clone.header = append([]byte(nil), j.header...)
	extraBytes, _ := hex.DecodeString(extraHex)
	copy(clone.header[headerExtraDataOffset:headerExtraDataOffset+len(extraBytes)], extraBytes)
	return &clone, extraHex
}

func extraNonceHex(extra uint64) string {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], extra)
	return hex.EncodeToString(buf[:])
}

type miningBackend interface {
	Name() string
	Start(ctx context.Context, state *miningState, shares chan<- share, stats *counters) error
}

func main() {
	cfg := parseFlags()
	if cfg.showVersion {
		fmt.Printf("pacminer %s\n", appVersion)
		return
	}
	if cfg.listDevices {
		if err := listOpenCLDevices(); err != nil {
			fmt.Fprintln(os.Stderr, "pacminer:", err)
			os.Exit(1)
		}
		return
	}
	if cfg.threads <= 0 {
		cfg.threads = defaultThreads()
	}
	if cfg.workSize <= 0 {
		cfg.workSize = defaultOpenCLWorkSize
	}
	if cfg.benchmark {
		runBenchmark(cfg)
		return
	}
	if strings.TrimSpace(cfg.username) == "" {
		fmt.Fprintln(os.Stderr, "missing --user. Example: pacminer.exe --user PYourWalletAddress.rig01")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runStratum(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "pacminer:", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.poolURL, "url", defaultPool, "Stratum TCP endpoint")
	flag.StringVar(&cfg.username, "user", "", "pool username, usually PAddress.worker")
	flag.StringVar(&cfg.password, "pass", "x", "pool password")
	flag.StringVar(&cfg.backend, "backend", "cpu", "mining backend: cpu or opencl")
	flag.IntVar(&cfg.device, "device", 0, "OpenCL device index")
	flag.IntVar(&cfg.threads, "threads", 0, "CPU mining threads, default is half of CPU cores")
	flag.IntVar(&cfg.workSize, "worksize", defaultOpenCLWorkSize, "OpenCL nonces per kernel launch")
	flag.Float64Var(&cfg.suggestDiff, "suggest-diff", defaultSuggestedShareDifficulty, "suggested share difficulty; lower values submit more shares")
	flag.BoolVar(&cfg.benchmark, "benchmark", false, "run a local BLAKE-256 benchmark instead of connecting to a pool")
	flag.IntVar(&cfg.benchmarkSecs, "seconds", 10, "benchmark duration in seconds")
	flag.BoolVar(&cfg.listDevices, "list-devices", false, "list OpenCL devices and exit")
	flag.BoolVar(&cfg.showVersion, "version", false, "print version")
	flag.BoolVar(&cfg.verbose, "verbose", false, "print every job and share response")
	flag.Parse()
	cfg.backend = strings.ToLower(strings.TrimSpace(cfg.backend))
	return cfg
}

func runStratum(ctx context.Context, cfg config) error {
	addr := normalizePoolAddress(cfg.poolURL)
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	defer conn.Close()

	client := &stratumClient{conn: conn, writer: bufio.NewWriter(conn)}
	messages := make(chan rpcMessage, 64)
	readErr := make(chan error, 1)
	go readLoop(conn, messages, readErr)

	state := &miningState{difficulty: 1}
	stats := &counters{}
	shares := make(chan share, 1024)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	backend, err := newMiningBackend(cfg)
	if err != nil {
		return err
	}
	backendErr := make(chan error, 1)
	go func() {
		backendErr <- backend.Start(ctx, state, shares, stats)
	}()
	select {
	case err := <-backendErr:
		if err != nil {
			return err
		}
	default:
	}

	fmt.Printf("pacminer %s\n", appVersion)
	fmt.Printf("pool=%s user=%s backend=%s threads=%d\n", addr, cfg.username, backend.Name(), cfg.threads)

	if _, err := client.call("mining.configure", []any{[]string{"minimum-difficulty"}, map[string]any{}}); err != nil {
		return err
	}
	if _, err := client.call("mining.subscribe", []any{fmt.Sprintf("pacminer/%s", appVersion)}); err != nil {
		return err
	}
	if cfg.suggestDiff > 0 {
		if _, err := client.call("mining.suggest_difficulty", []any{cfg.suggestDiff}); err != nil {
			return err
		}
	}
	if _, err := client.call("mining.authorize", []any{cfg.username, cfg.password}); err != nil {
		return err
	}

	pending := make(map[string]share)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastHashes := uint64(0)
	lastTick := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-backendErr:
			cancel()
			if err == nil {
				return ctx.Err()
			}
			return err
		case err := <-readErr:
			cancel()
			return err
		case msg := <-messages:
			handleMessage(msg, cfg.username, state, stats, pending, cfg.verbose)
		case sh := <-shares:
			if sh.jobSeq != state.currentSeq() {
				continue
			}
			id, err := client.call("mining.submit", []any{cfg.username, sh.jobID, sh.extraHex, sh.ntimeHex, sh.nonceHex})
			if err != nil {
				cancel()
				return err
			}
			stats.submits.Add(1)
			pending[id] = sh
		case now := <-ticker.C:
			total := stats.hashes.Load()
			delta := total - lastHashes
			rate := float64(delta) / now.Sub(lastTick).Seconds()
			lastHashes = total
			lastTick = now
			job := state.snapshot()
			jobID := "-"
			diff := state.currentDifficulty()
			if job != nil {
				jobID = job.id
				diff = job.difficulty
			}
			fmt.Printf("hashrate=%s job=%s diff=%.4f accepted=%d rejected=%d submitted=%d total=%d\n",
				formatHashrate(rate),
				jobID,
				diff,
				stats.accepted.Load(),
				stats.rejected.Load(),
				stats.submits.Load(),
				total,
			)
		}
	}
}

func newMiningBackend(cfg config) (miningBackend, error) {
	switch cfg.backend {
	case "", "cpu":
		return cpuBackend{threads: cfg.threads}, nil
	case "opencl", "gpu":
		return newOpenCLBackend(cfg)
	default:
		return nil, fmt.Errorf("unknown backend %q", cfg.backend)
	}
}

type cpuBackend struct {
	threads int
}

func (b cpuBackend) Name() string {
	return "cpu"
}

func (b cpuBackend) Start(ctx context.Context, state *miningState, shares chan<- share, stats *counters) error {
	threads := b.threads
	if threads <= 0 {
		threads = defaultThreads()
	}
	for i := 0; i < threads; i++ {
		go mineLoop(ctx, i, threads, state, shares, stats)
	}
	<-ctx.Done()
	return ctx.Err()
}

func handleMessage(msg rpcMessage, username string, state *miningState, stats *counters, pending map[string]share, verbose bool) {
	switch msg.Method {
	case "mining.set_difficulty":
		diff, err := parseFloatParam(msg.Params, 0)
		if err != nil {
			fmt.Printf("bad difficulty message: %v\n", err)
			return
		}
		state.setDifficulty(diff)
		if verbose {
			fmt.Printf("pool difficulty set to %.4f\n", diff)
		}
	case "mining.notify":
		job, err := jobFromNotify(msg.Params, state.currentDifficulty(), state.nextSeq())
		if err != nil {
			fmt.Printf("cannot use pool job: %v\n", err)
			return
		}
		state.setJob(job)
		if verbose {
			fmt.Printf("new job %s bits=%s ntime=%s\n", job.id, job.bitsHex, job.ntimeHex)
		}
	default:
		if len(msg.ID) == 0 {
			return
		}
		id := string(msg.ID)
		if sh, ok := pending[id]; ok {
			delete(pending, id)
			if hasRPCError(msg.Error) {
				stats.rejected.Add(1)
				if verbose {
					fmt.Printf("share rejected nonce=%s hash=%s error=%s\n", sh.nonceHex, sh.hash, compactJSON(msg.Error))
				}
				return
			}
			okResult := false
			_ = json.Unmarshal(msg.Result, &okResult)
			if okResult {
				stats.accepted.Add(1)
				if verbose {
					fmt.Printf("share accepted nonce=%s hash=%s worker=%s\n", sh.nonceHex, sh.hash, username)
				}
			} else {
				stats.rejected.Add(1)
				if verbose {
					fmt.Printf("share rejected nonce=%s hash=%s\n", sh.nonceHex, sh.hash)
				}
			}
		} else if hasRPCError(msg.Error) {
			fmt.Printf("pool response error id=%s error=%s\n", id, compactJSON(msg.Error))
		}
	}
}

func readLoop(conn net.Conn, out chan<- rpcMessage, errs chan<- error) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			errs <- fmt.Errorf("decode stratum message: %w", err)
			return
		}
		out <- msg
	}
	if err := scanner.Err(); err != nil {
		errs <- fmt.Errorf("read stratum: %w", err)
		return
	}
	errs <- errors.New("pool closed connection")
}

func (c *stratumClient) call(method string, params []any) (string, error) {
	id := c.nextID.Add(1)
	req := map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := json.NewEncoder(c.writer).Encode(req); err != nil {
		return "", err
	}
	if err := c.writer.Flush(); err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

func mineLoop(ctx context.Context, workerID int, workers int, state *miningState, shares chan<- share, stats *counters) {
	var seen uint64
	nonce := uint32(time.Now().UnixNano()) + uint32(workerID*0x9e3779b9)
	step := uint32(workers)
	if step == 0 {
		step = 1
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job := state.snapshot()
		if job == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if job.seq != seen {
			seen = job.seq
			nonce = uint32(time.Now().UnixNano()) + uint32(workerID)
		}

		extra := uint64(time.Now().UnixNano()) + uint64(workerID)
		workJob, extraHex := job.withExtraNonce(extra)
		header := make([]byte, len(workJob.header))
		copy(header, workJob.header)
		for i := 0; i < 32768; i++ {
			if state.currentSeq() != seen {
				break
			}
			binary.LittleEndian.PutUint32(header[headerNonceOffset:headerNonceOffset+4], nonce)
			hash := blake256.Sum256(header)
			stats.hashes.Add(1)
			if bytes.Compare(hash[:], job.shareTarget[:]) <= 0 {
				sh := share{
					jobSeq:   job.seq,
					jobID:    job.id,
					extraHex: extraHex,
					ntimeHex: job.ntimeHex,
					nonceHex: fmt.Sprintf("%08x", nonce),
					nonce:    nonce,
					hash:     hex.EncodeToString(hash[:]),
				}
				select {
				case shares <- sh:
				case <-ctx.Done():
					return
				}
			}
			prev := nonce
			nonce += step
			if nonce < prev {
				extra += uint64(step)
				workJob, extraHex = job.withExtraNonce(extra)
				copy(header, workJob.header)
			}
		}
	}
}

func jobFromNotify(params []json.RawMessage, difficulty float64, seq uint64) (*miningJob, error) {
	if len(params) < 7 {
		return nil, fmt.Errorf("pool notify is missing PAC headerhex extension; update pacpool before mining")
	}
	jobID, err := parseStringParam(params, 0)
	if err != nil {
		return nil, err
	}
	bitsHex, err := parseStringParam(params, 3)
	if err != nil {
		return nil, err
	}
	ntimeHex, err := parseStringParam(params, 4)
	if err != nil {
		return nil, err
	}
	headerHex, err := parseStringParam(params, 6)
	if err != nil {
		return nil, err
	}
	header, err := hex.DecodeString(strings.TrimSpace(headerHex))
	if err != nil {
		return nil, fmt.Errorf("bad headerhex: %w", err)
	}
	if len(header) != headerLength {
		return nil, fmt.Errorf("bad header length %d, want %d", len(header), headerLength)
	}
	bits, err := strconv.ParseUint(bitsHex, 16, 32)
	if err != nil {
		return nil, fmt.Errorf("bad bits %q: %w", bitsHex, err)
	}
	ntime, err := strconv.ParseUint(ntimeHex, 16, 32)
	if err != nil {
		return nil, fmt.Errorf("bad ntime %q: %w", ntimeHex, err)
	}
	ntime32 := uint32(ntime)
	if difficulty <= 0 || math.IsNaN(difficulty) || math.IsInf(difficulty, 0) {
		difficulty = 1
	}

	binary.LittleEndian.PutUint32(header[headerTimestampOffset:headerTimestampOffset+4], ntime32)
	binary.LittleEndian.PutUint32(header[headerBitsOffset:headerBitsOffset+4], uint32(bits))
	binary.LittleEndian.PutUint32(header[headerNonceOffset:headerNonceOffset+4], 0)

	return &miningJob{
		seq:         seq,
		id:          jobID,
		bitsHex:     bitsHex,
		ntimeHex:    ntimeHex,
		ntime:       ntime32,
		header:      header,
		shareTarget: targetBytes(difficultyToTarget(difficulty)),
		difficulty:  difficulty,
	}, nil
}

func (s *miningState) nextSeq() uint64 {
	return s.seq.Load() + 1
}

func (s *miningState) setJob(job *miningJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job = job
	s.difficulty = job.difficulty
	s.seq.Store(job.seq)
}

func (s *miningState) setDifficulty(difficulty float64) {
	if difficulty <= 0 || math.IsNaN(difficulty) || math.IsInf(difficulty, 0) {
		difficulty = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.difficulty = difficulty
	if s.job != nil {
		clone := *s.job
		clone.seq = s.seq.Add(1)
		clone.difficulty = difficulty
		clone.shareTarget = targetBytes(difficultyToTarget(difficulty))
		s.job = &clone
	}
}

func (s *miningState) snapshot() *miningJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.job
}

func (s *miningState) currentSeq() uint64 {
	return s.seq.Load()
}

func (s *miningState) currentDifficulty() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.difficulty <= 0 {
		return 1
	}
	return s.difficulty
}

func parseStringParam(params []json.RawMessage, index int) (string, error) {
	if len(params) <= index {
		return "", fmt.Errorf("missing param %d", index)
	}
	var s string
	if err := json.Unmarshal(params[index], &s); err != nil {
		return "", fmt.Errorf("param %d: %w", index, err)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("param %d is empty", index)
	}
	return strings.TrimSpace(s), nil
}

func parseFloatParam(params []json.RawMessage, index int) (float64, error) {
	if len(params) <= index {
		return 0, fmt.Errorf("missing param %d", index)
	}
	var f float64
	if err := json.Unmarshal(params[index], &f); err != nil {
		return 0, fmt.Errorf("param %d: %w", index, err)
	}
	return f, nil
}

func hasRPCError(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(encoded)
}

func compactToBig(compact uint32) *big.Int {
	mantissa := compact & 0x007fffff
	isNegative := compact&0x00800000 != 0
	exponent := uint(compact >> 24)

	var bn *big.Int
	if exponent <= 3 {
		mantissa >>= 8 * (3 - exponent)
		bn = big.NewInt(int64(mantissa))
	} else {
		bn = big.NewInt(int64(mantissa))
		bn.Lsh(bn, 8*(exponent-3))
	}
	if isNegative {
		bn.Neg(bn)
	}
	return bn
}

func difficultyToTarget(difficulty float64) *big.Int {
	if difficulty <= 0 || math.IsNaN(difficulty) || math.IsInf(difficulty, 0) {
		difficulty = 1
	}
	base := compactToBig(0x207fffff)
	diffRat := new(big.Rat).SetFloat64(difficulty)
	if diffRat == nil || diffRat.Sign() <= 0 {
		diffRat = big.NewRat(1, 1)
	}
	scaled := new(big.Rat).SetInt(base)
	scaled.Quo(scaled, diffRat)
	target := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	if target.Sign() <= 0 {
		return big.NewInt(1)
	}
	if target.Cmp(base) > 0 {
		return base
	}
	return target
}

func targetBytes(target *big.Int) [32]byte {
	var out [32]byte
	if target == nil || target.Sign() <= 0 {
		out[31] = 1
		return out
	}
	src := target.Bytes()
	if len(src) > len(out) {
		src = src[len(src)-len(out):]
	}
	copy(out[len(out)-len(src):], src)
	return out
}

func normalizePoolAddress(raw string) string {
	addr := strings.TrimSpace(raw)
	addr = strings.TrimPrefix(addr, "stratum+tcp://")
	addr = strings.TrimPrefix(addr, "tcp://")
	addr = strings.TrimPrefix(addr, "stratum://")
	if !strings.Contains(addr, ":") {
		addr += ":3333"
	}
	return addr
}

func defaultThreads() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		return 1
	}
	return n
}

func formatHashrate(rate float64) string {
	switch {
	case rate >= 1_000_000_000:
		return fmt.Sprintf("%.2f GH/s", rate/1_000_000_000)
	case rate >= 1_000_000:
		return fmt.Sprintf("%.2f MH/s", rate/1_000_000)
	case rate >= 1_000:
		return fmt.Sprintf("%.2f kH/s", rate/1_000)
	default:
		return fmt.Sprintf("%.0f H/s", rate)
	}
}

func runBenchmark(cfg config) {
	if cfg.backend == "opencl" || cfg.backend == "gpu" {
		if runOpenCLBenchmark(cfg) {
			return
		}
	}
	if cfg.benchmarkSecs <= 0 {
		cfg.benchmarkSecs = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.benchmarkSecs)*time.Second)
	defer cancel()

	var hashes atomic.Uint64
	var wg sync.WaitGroup
	for i := 0; i < cfg.threads; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			header := make([]byte, headerLength)
			binary.LittleEndian.PutUint32(header[headerTimestampOffset:headerTimestampOffset+4], uint32(time.Now().Unix()))
			binary.LittleEndian.PutUint32(header[headerBitsOffset:headerBitsOffset+4], 0x207fffff)
			binary.LittleEndian.PutUint32(header[headerHeightOffset:headerHeightOffset+4], 1)
			nonce := uint32(time.Now().UnixNano()) + uint32(worker)
			step := uint32(cfg.threads)
			if step == 0 {
				step = 1
			}
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				for i := 0; i < 32768; i++ {
					binary.LittleEndian.PutUint32(header[headerNonceOffset:headerNonceOffset+4], nonce)
					_ = blake256.Sum256(header)
					hashes.Add(1)
					nonce += step
				}
			}
		}(i)
	}
	start := time.Now()
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	rate := float64(hashes.Load()) / elapsed
	fmt.Printf("benchmark threads=%d hashes=%d elapsed=%.2fs rate=%s\n", cfg.threads, hashes.Load(), elapsed, formatHashrate(rate))
}
