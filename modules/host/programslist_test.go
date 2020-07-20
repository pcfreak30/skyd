package host

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestProgramsListPrune is a unit test verifying the functionality of
// managedPruneProgramsList
func TestProgramsListPrune(t *testing.T) {
	t.Parallel()

	token1 := modules.NewMDMProgramToken()
	token2 := modules.NewMDMProgramToken()
	token3 := modules.NewMDMProgramToken()

	pl := programsList{
		programs: make(map[modules.MDMProgramToken]*programInfo, 0),
		tokens:   make([]*tokenEntry, 0),
	}

	// mock a state
	pl.mu.Lock()
	pl.tokens = append(
		pl.tokens,
		&tokenEntry{token1, time.Now().Add(10 * time.Millisecond)},
		&tokenEntry{token2, time.Now().Add(100 * time.Millisecond)},
		&tokenEntry{token3, time.Now().Add(1000 * time.Millisecond)},
	)
	pl.programs[token1] = &programInfo{}
	pl.programs[token2] = &programInfo{}
	pl.programs[token3] = &programInfo{}
	pl.mu.Unlock()

	// prune immediately - expect no changes
	pl.managedPruneProgramsList()
	pl.mu.Lock()
	progrLen := len(pl.programs)
	tokLen := len(pl.tokens)
	pl.mu.Unlock()
	if progrLen != 3 || tokLen != 3 {
		t.Fatal("Unexpected number of tokens pruned")
	}

	// sleep 50ms - expect 1 token to be pruned
	time.Sleep(50 * time.Millisecond)
	pl.managedPruneProgramsList()
	pl.mu.Lock()
	progrLen = len(pl.programs)
	tokLen = len(pl.tokens)
	_, tok1Found := pl.programs[token1]
	pl.mu.Unlock()
	if progrLen != 2 || tokLen != 2 || tok1Found {
		t.Fatal("Unexpected number of tokens pruned")
	}

	// sleep 500ms - expect 1 more token to be pruned
	time.Sleep(500 * time.Millisecond)
	pl.managedPruneProgramsList()
	pl.mu.Lock()
	progrLen = len(pl.programs)
	tokLen = len(pl.tokens)
	_, tok2Found := pl.programs[token2]
	pl.mu.Unlock()
	if progrLen != 1 || tokLen != 1 || tok2Found {
		t.Fatal("Unexpected number of tokens pruned")
	}

	// sleep 1000ms - expect all tokens to be pruned
	time.Sleep(1000 * time.Millisecond)
	pl.managedPruneProgramsList()
	pl.mu.Lock()
	progrLen = len(pl.programs)
	tokLen = len(pl.tokens)
	_, tok3Found := pl.programs[token3]
	pl.mu.Unlock()
	if progrLen != 0 || tokLen != 0 || tok3Found {
		t.Fatal("Unexpected number of tokens pruned")
	}
}

// TestProgramsListNewProgramInfo is a unit test verifying the functionality of
// adding information for a program to the programs list
func TestProgramsListNewProgramInfo(t *testing.T) {
	t.Parallel()

	pl := programsList{
		programs: make(map[modules.MDMProgramToken]*programInfo, 0),
		tokens:   make([]*tokenEntry, 0),
	}

	// register information on a program
	token := modules.NewMDMProgramToken()
	pl.managedNewProgramInfo(token, make(chan struct{}))

	// verify it's found
	_, found := pl.managedProgramInfo(token)
	if !found {
		t.Fatal("Expected program info to be found")
	}

	// verify the program token is part of the list of tokens, this assures the
	// program info will get pruned eventually
	pl.mu.Lock()
	tokLen := len(pl.tokens)
	progLen := len(pl.programs)
	pl.mu.Unlock()
	if tokLen != 1 || progLen != 1 {
		t.Fatal("Token not listed in the token list")
	}

	// verify the build.Critical when we try and register a refund for the same
	// token twice
	defer func() {
		if r := recover(); r != nil {
			if !strings.Contains(fmt.Sprintf("%v", r), "already contains an entry for given token") {
				t.Error("Expected build.Critical")
				t.Log(r)
			}
		}
	}()
	pl.managedNewProgramInfo(token, make(chan struct{}))
}

// TestProgramsListSetRefund unit tests the functionality of the SetRefund
// function on the program info
func TestProgramsListSetRefund(t *testing.T) {
	t.Parallel()

	pl := programsList{
		programs: make(map[modules.MDMProgramToken]*programInfo, 0),
		tokens:   make([]*tokenEntry, 0),
	}

	// case error nil
	token := modules.NewMDMProgramToken()
	pi := pl.managedNewProgramInfo(token, make(chan struct{}))
	refund := types.NewCurrency64(fastrand.Uint64n(100))
	pi.SetRefund(refund, nil)
	if !pi.externRefund.Equals(refund) {
		t.Fatal("Unexpected extern refund")
	}
	if pi.externRefundErr != nil {
		t.Fatal("Unexpected extern err")
	}

	token = modules.NewMDMProgramToken()
	pi = pl.managedNewProgramInfo(token, make(chan struct{}))
	refund = types.NewCurrency64(fastrand.Uint64n(100))
	refundErr := errors.New("some error")
	pi.SetRefund(refund, refundErr)
	if !pi.externRefund.IsZero() {
		t.Fatal("Unexpected extern refund")
	}
	if pi.externRefundErr != refundErr {
		t.Fatal("Unexpected extern err")
	}
}
