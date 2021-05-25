package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/persist"
)

// There's a global lock on cpu and memory profiling, because I'm not sure what
// happens if multiple threads call each at the same time. This lock might be
// unnecessary.
var (
	cpuActive   bool
	cpuLock     sync.Mutex
	memActive   bool
	memLock     sync.Mutex
	traceActive bool
	traceLock   sync.Mutex

	// profileChan is used to determine if the profile has been started or
	// stopped.
	profileChan chan struct{}
	chanLock    sync.Mutex
)

var (
	// ErrInvalidProfileFlags is the error returned when there is an error parsing
	// the profile flags
	ErrInvalidProfileFlags = errors.New("Unable to parse --profile flags, unrecognized or duplicate flags")
)

// ProcessProfileFlags checks that the flags given for profiling are valid.
func ProcessProfileFlags(profile string) (string, error) {
	// Check for input
	if profile == "" {
		return "", errors.New("no profile flags provided")
	}

	// Convert to lowercase
	profile = strings.ToLower(profile)

	// Valid profile flags are spaces, c, m, and t
	validProfiles := " cmt"

	// Check profile flags
	invalidProfiles := profile
	for _, p := range validProfiles {
		invalidProfiles = strings.Replace(invalidProfiles, string(p), "", 1)
	}
	if len(invalidProfiles) > 0 {
		return "", errors.AddContext(ErrInvalidProfileFlags, invalidProfiles)
	}
	return profile, nil
}

// StartCPUProfile starts cpu profiling. An error will be returned if a cpu
// profiler is already running.
func StartCPUProfile(profileDir, identifier string) error {
	// Lock the cpu profile lock so that only one profiler is running at a
	// time.
	cpuLock.Lock()
	if cpuActive {
		cpuLock.Unlock()
		return errors.New("cannot start cpu profiler, a profiler is already running")
	}
	cpuActive = true
	cpuLock.Unlock()

	// Start profiling into the profile dir, using the identifier. The timestamp
	// of the start time of the profiling will be included in the filename.
	cpuProfileFile, err := os.Create(filepath.Join(profileDir, "cpu-profile-"+identifier+"-"+time.Now().Format(time.RFC3339Nano)+".prof"))
	if err != nil {
		return err
	}
	pprof.StartCPUProfile(cpuProfileFile)
	return nil
}

// StopCPUProfile stops cpu profiling.
func StopCPUProfile() {
	cpuLock.Lock()
	if cpuActive {
		pprof.StopCPUProfile()
		cpuActive = false
	}
	cpuLock.Unlock()
}

// SaveMemProfile saves the current memory structure of the program. An error
// will be returned if memory profiling is already in progress. Unlike for cpu
// profiling, there is no 'stopMemProfile' call - everything happens at once.
func SaveMemProfile(profileDir, identifier string) error {
	memLock.Lock()
	if memActive {
		memLock.Unlock()
		return errors.New("cannot start memory profiler, a memory profiler is already running")
	}
	memActive = true
	memLock.Unlock()

	// Save the memory profile.
	memFile, err := os.Create(filepath.Join(profileDir, "mem-profile-"+identifier+"-"+time.Now().Format(time.RFC3339Nano)+".prof"))
	if err != nil {
		return err
	}
	pprof.WriteHeapProfile(memFile)

	memLock.Lock()
	memActive = false
	memLock.Unlock()
	return nil
}

// StartTrace starts trace. An error will be returned if a trace
// is already running.
func StartTrace(traceDir, identifier string) error {
	// Lock the trace lock so that only one profiler is running at a
	// time.
	traceLock.Lock()
	if traceActive {
		traceLock.Unlock()
		return errors.New("cannot start trace, it is already running")
	}
	traceActive = true
	traceLock.Unlock()

	// Start trace into the trace dir, using the identifier. The timestamp
	// of the start time of the trace will be included in the filename.
	traceFile, err := os.Create(filepath.Join(traceDir, "trace-"+identifier+"-"+time.Now().Format(time.RFC3339Nano)+".trace"))
	if err != nil {
		return err
	}
	return trace.Start(traceFile)
}

// StopTrace stops trace.
func StopTrace() {
	traceLock.Lock()
	if traceActive {
		trace.Stop()
		traceActive = false
	}
	traceLock.Unlock()
}

// startContinuousLog creates dir and saves inexpensive logs periodically.
// It also runs the restart function periodically.
func startContinuousLog(dir string, sleepCap time.Duration, restart func()) {
	// Create the folder for all of the profiling results.
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Continuously log statistics about the running Sia application.
	go func() {
		// Create the logger.
		log, err := persist.NewFileLogger(filepath.Join(dir, "continuousStats.log"))
		if err != nil {
			fmt.Println("Stats logging failed:", err)
			return
		}
		// Collect statistics in an infinite loop.
		sleepTime := time.Second * 10
		for {
			// Check if the profile was stopped
			if isProfileStopped() {
				return
			}

			// Profile is still running, trigger the reset func
			restart()

			// Sleep for an exponential amount of time each iteration, this
			// keeps the size of the log small while still providing lots of
			// information.
			time.Sleep(sleepTime)
			sleepTime = time.Duration(1.2 * float64(sleepTime))
			if sleepCap != 0*time.Second && sleepTime > sleepCap {
				sleepTime = sleepCap
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			log.Printf("\n\tGoroutines: %v\n\tAlloc: %v\n\tTotalAlloc: %v\n\tHeapAlloc: %v\n\tHeapSys: %v\n", runtime.NumGoroutine(), m.Alloc, m.TotalAlloc, m.HeapAlloc, m.HeapSys)
		}
	}()
}

// StartContinuousProfile will continuously print statistics about the cpu
// usage, memory usage, and runtime stats of the program, and run an execution
// logger. Select one (recommended) or more functionalities by passing the
// corresponding flag(s)
func StartContinuousProfile(profileDir string, profileCPU bool, profileMem bool, profileTrace bool) {
	// Initialize the profile chan
	initProfileChan()

	sleepCap := 0 * time.Second // Unlimited.
	if profileTrace && sleepCap == 0 {
		sleepCap = 10 * time.Minute
	}
	startContinuousLog(profileDir, sleepCap, func() {
		if profileCPU {
			StopCPUProfile()
			StartCPUProfile(profileDir, "continuousProfileCPU")
		}
		if profileMem {
			SaveMemProfile(profileDir, "continuousProfileMem")
		}
		if profileTrace {
			StopTrace()
			StartTrace(profileDir, "continuousProfileTrace")
		}
	})
}

// StopContinuousProfile stops the profiling of the node
func StopContinuousProfile() {
	closeProfileChan()
	StopCPUProfile()
	StopTrace()
}

// closeProfileChan closes the profileChan
func closeProfileChan() {
	chanLock.Lock()
	defer chanLock.Unlock()

	// If the log chan is nil, initialize and then close the channel
	if profileChan == nil {
		profileChan = make(chan struct{})
		close(profileChan)
		return
	}

	// Close the channel if it is open
	select {
	case <-profileChan:
	default:
		close(profileChan)
	}
}

// initProfileChan initializes the profileChan
func initProfileChan() {
	chanLock.Lock()
	defer chanLock.Unlock()

	// Initialize the channel if it is still nil
	if profileChan == nil {
		profileChan = make(chan struct{})
		return
	}

	// Re-initialize the channel if it was closed
	select {
	case <-profileChan:
		profileChan = make(chan struct{})
	default:
	}
}

// isProfileStopped returns whether or not the profile has been stopped, which
// is indicated by whether or not the profileChan has been closed.
func isProfileStopped() bool {
	chanLock.Lock()
	defer chanLock.Unlock()
	select {
	case <-profileChan:
		return true
	default:
		return false
	}
}
