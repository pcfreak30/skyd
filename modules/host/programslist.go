package host

import (
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
)

var (
	// pruneProgramsListFrequency is the frequency with which the host prunes
	// the information it keeps in memory about running, or recently executed,
	// MDM programs
	pruneProgramsListFrequency = build.Select(build.Var{
		Standard: 15 * time.Minute,
		Dev:      10 * time.Minute,
		Testing:  30 * time.Second,
	}).(time.Duration)

	// programInfoExpiry is the amount of time the hosts keeps track of
	// information about an MDM program that has recently been executed on the
	// host.
	programInfoExpiry = build.Select(build.Var{
		Standard: 10 * time.Minute,
		Dev:      time.Minute,
		Testing:  10 * time.Second,
	}).(time.Duration)
)

type (
	// programsList keeps track of info on running or recently executed MDM
	// programs. It keeps track of the refund, allowing renters to query what
	// was refunded to them after the execution of an MDM program.
	programsList struct {
		programs map[modules.MDMProgramToken]*programInfo
		tokens   []*tokenEntry
		mu       sync.Mutex
	}

	// programInfo is a helper struct that contains information about a program
	programInfo struct {
		externRefund    types.Currency
		externRefundErr error
		refunded        chan struct{}
	}

	// tokenEntry is a helper struct that keeps track of when the token, and the
	// refund that goes along with it, can be removed from the heap
	tokenEntry struct {
		token  modules.MDMProgramToken
		expiry time.Time
	}
)

// refundComplete returns whether the refund for the program was refunded.
func (pi programInfo) refundComplete() bool {
	select {
	case <-pi.refunded:
		return false
	default:
		return true
	}
}

// managedAddProgramInfo adds information to the list of running programs for a
// program with given token. This list contains information about programs that
// are running or that have ran in the recent history. The list is periodically
// purged for programs with a token that have been in the list for longer than
// the `refundExpiry` time.
func (pl *programsList) managedAddProgramInfo(t modules.MDMProgramToken, pi *programInfo) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	_, exists := pl.programs[t]
	if exists {
		build.Critical("ProgramList already contains an entry for given token")
		return
	}

	pl.programs[t] = pi
	pl.tokens = append(pl.tokens, &tokenEntry{t, time.Now().Add(programInfoExpiry)})
}

// managedProgramInfo returns the information for the program with given token.
// It will also return a boolean indicating whether or not the information was
// found.
func (pl *programsList) managedProgramInfo(t modules.MDMProgramToken) (*programInfo, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	pi, found := pl.programs[t]
	if !found {
		return nil, false
	}
	return pi, true
}

// managedPruneProgramsList prunes the extra information we keep on running or
// recently executed programs, by removing all entries that have an expiry in
// the past. We can only keep this information in memory for a limited amount of
// time as we would otherwise be leaking memory.
func (pl *programsList) managedPruneProgramsList() {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	now := time.Now()
	offset := len(pl.tokens)
	for index, te := range pl.tokens {
		if now.Before(te.expiry) {
			offset = index
			break
		}
		delete(pl.programs, te.token)
	}
	pl.tokens = pl.tokens[offset:]
}

// threadedPruneProgramsList will prune the program information the host keeps
// in memory.
//
// NOTE: threadgroup counter must be inside for loop. If not, calling 'Flush' on
// the threadgroup would deadlock.
func (h *Host) threadedPruneProgramsList() {
	for {
		func() {
			if err := h.tg.Add(); err != nil {
				return
			}
			defer h.tg.Done()
			h.staticPrograms.managedPruneProgramsList()
		}()

		// Block until next cycle.
		select {
		case <-h.tg.StopChan():
			return
		case <-time.After(pruneProgramsListFrequency):
			continue
		}
	}
}
