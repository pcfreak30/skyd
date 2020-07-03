package host

import (
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
)

// TestRefundsListPrune is a unit test verifying the functionality of
// managedPruneRefundsList
func TestRefundsListPrune(t *testing.T) {
	t.Parallel()

	oneCurrency := types.NewCurrency64(1)
	token1 := modules.NewMDMProgramToken()
	token2 := modules.NewMDMProgramToken()
	token3 := modules.NewMDMProgramToken()

	rl := refundsList{
		refunds: make(map[modules.MDMProgramToken]types.Currency, 0),
		tokens:  make([]*tokenEntry, 0),
	}

	// mock a state
	rl.mu.Lock()
	rl.tokens = append(
		rl.tokens,
		&tokenEntry{token1, time.Now().Add(10 * time.Millisecond)},
		&tokenEntry{token2, time.Now().Add(100 * time.Millisecond)},
		&tokenEntry{token3, time.Now().Add(1000 * time.Millisecond)},
	)
	rl.refunds[token1] = oneCurrency
	rl.refunds[token2] = oneCurrency
	rl.refunds[token3] = oneCurrency
	rl.mu.Unlock()

	// prune immediately - expect no changes
	rl.managedPruneRefundsList()
	rl.mu.Lock()
	refLen := len(rl.refunds)
	tokLen := len(rl.tokens)
	rl.mu.Unlock()
	if refLen != 3 || tokLen != 3 {
		t.Fatal("Unexpected number of tokens pruned")
	}

	// sleep 50ms - expect 1 token to be pruned
	time.Sleep(50 * time.Millisecond)
	rl.managedPruneRefundsList()
	rl.mu.Lock()
	refLen = len(rl.refunds)
	tokLen = len(rl.tokens)
	_, tok1Found := rl.refunds[token1]
	rl.mu.Unlock()
	if refLen != 2 || tokLen != 2 || tok1Found {
		t.Fatal("Unexpected number of tokens pruned")
	}

	// sleep 500ms - expect 1 more token to be pruned
	time.Sleep(500 * time.Millisecond)
	rl.managedPruneRefundsList()
	rl.mu.Lock()
	refLen = len(rl.refunds)
	tokLen = len(rl.tokens)
	_, tok2Found := rl.refunds[token2]
	rl.mu.Unlock()
	if refLen != 1 || tokLen != 1 || tok2Found {
		t.Fatal("Unexpected number of tokens pruned")
	}

	// sleep 1000ms - expect all tokens to be pruned
	time.Sleep(1000 * time.Millisecond)
	rl.managedPruneRefundsList()
	rl.mu.Lock()
	refLen = len(rl.refunds)
	tokLen = len(rl.tokens)
	_, tok3Found := rl.refunds[token3]
	rl.mu.Unlock()
	if refLen != 0 || tokLen != 0 || tok3Found {
		t.Fatal("Unexpected number of tokens pruned")
	}
}

// TestRefundsListRegister is a unit test verifying the functionality of
// managedRegisterRefund
func TestRefundsListRegister(t *testing.T) {
	t.Parallel()

	rl := refundsList{
		refunds: make(map[modules.MDMProgramToken]types.Currency, 0),
		tokens:  make([]*tokenEntry, 0),
	}

	// register a refund
	token := modules.NewMDMProgramToken()
	refund := types.NewCurrency64(fastrand.Uint64n(1000))
	rl.managedRegisterRefund(token, refund)

	// verify it's found
	regRefund, found := rl.managedRefund(token)
	if !found {
		t.Fatal("Expected refund to be found")
	}
	if !regRefund.Equals(refund) {
		t.Fatal("Expected refund to be equal the registered refund")
	}

	// verify it's both in the token heap as in the refunds list - assuring it's
	// going to get pruned eventually
	rl.mu.Lock()
	tokLen := len(rl.tokens)
	refLen := len(rl.refunds)
	rl.mu.Unlock()
	if tokLen != 1 || refLen != 1 {
		t.Fatal("Token not listed in the token list")
	}
}
