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
		Standard: time.Minute,
		Dev:      time.Minute,
		Testing:  30 * time.Second,
	}).(time.Duration)

	// programInfoExpiry is the amount of time the hosts keeps track of
	// information about an MDM program that has recently been executed on the
	// host.
	programInfoExpiry = build.Select(build.Var{
		Standard: 10 * time.Minute,
		Dev:      10 * time.Minute,
		Testing:  time.Minute,
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
		// The `externRefund` and `externRefundErr` fields are not allowed to be
		// accessed by any external threads until the `refunded` channel has
		// been closed. Once that is the case, both fields can be treated like
		// statics.
		externRefund    types.Currency // refund amount
		externRefundErr error          // refund error
		refunded        chan struct{}
	}

	// tokenEntry is a helper struct that keeps track of an expiry time which
	// indicates when the info for the program that corresponds with the token
	// can be pruned.
	tokenEntry struct {
		token  modules.MDMProgramToken
		expiry time.Time
	}
)

// managedNewProgramInfo creates a new program info object for a program with
// the given token. It will add it to the list of program infos we track and
// return a reference to it. This list contains information about programs that
// are running or that have ran in the recent history. The list is periodically
// purged for programs with a token that have been in the list for longer than
// the `refundExpiry` time.
func (pl *programsList) managedNewProgramInfo(t modules.MDMProgramToken, refundedChan chan struct{}) *programInfo {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	_, exists := pl.programs[t]
	if exists {
		build.Critical("ProgramList already contains an entry for given token")
		return nil
	}

	pi := &programInfo{refunded: refundedChan}
	pl.programs[t] = pi
	pl.tokens = append(pl.tokens, &tokenEntry{t, time.Now().Add(programInfoExpiry)})
	return pi
}

// SetRefund sets the refund and error on the programInfo and closes the
// `refunded` channel. This signals that the `externRefund` and
// `externRefundErr` fields are available and can be accessed freely.
func (pi *programInfo) SetRefund(refund types.Currency, err error) {
	select {
	case <-pi.refunded:
		build.Critical("SetRefund is being called twice")
	default:
		pi.externRefundErr = err
		if err == nil {
			pi.externRefund = refund
		}
		close(pi.refunded)
	}
}

// managedProgramInfo returns the information for the program with given token.
// It will also return a boolean indicating whether or not the information was
// found.
func (pl *programsList) managedProgramInfo(t modules.MDMProgramToken) (*programInfo, bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	pi, found := pl.programs[t]
	return pi, found
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
		if err := h.tg.Add(); err != nil {
			return
		}
		h.staticPrograms.managedPruneProgramsList()
		h.tg.Done()

		// Block until next cycle.
		select {
		case <-h.tg.StopChan():
			return
		case <-time.After(pruneProgramsListFrequency):
			continue
		}
	}
}
