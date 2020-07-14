package host

import (
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/siamux"
)

// managedRPCProgramRefund handles the RPC which returns the refund (if any) for
// a certain program token. The host keeps these refunds in memory for
// 'refundExpiry' time, allowing the renter to query these and verify its
// account balance.
func (h *Host) managedRPCProgramRefund(stream siamux.Stream) error {
	// Read the price table
	pt, err := h.staticReadPriceTableID(stream)
	if err != nil {
		return errors.AddContext(err, "Failed to read price table")
	}

	// Process payment.
	pd, err := h.ProcessPayment(stream)
	if err != nil {
		return errors.AddContext(err, "failed to process payment")
	}

	// Check payment.
	if pd.Amount().Cmp(pt.ProgramRefundCost) < 0 {
		return modules.ErrInsufficientPaymentForRPC
	}

	// Refund excessive payment.
	refund := pd.Amount().Sub(pt.ProgramRefundCost)
	err = h.staticAccountManager.callRefund(pd.AccountID(), refund)
	if err != nil {
		return errors.AddContext(err, "failed to refund client")
	}

	// Read request
	var prr modules.ProgramRefundRequest
	err = modules.RPCRead(stream, &prr)
	if err != nil {
		return errors.AddContext(err, "Failed to read ProgramRefundRequest")
	}

	// Get the program info and whether or not it was found, which indicates if
	// the host still had the information in memory.
	programInfo, found := h.staticPrograms.managedProgramInfo(prr.ProgramToken)
	if found && programInfo.running() {
		return errors.AddContext(err, "Failed to return the refund, the program is still running")
	}

	// Send response.
	err = modules.RPCWrite(stream, modules.ProgramRefundResponse{
		Refund: programInfo.refund,
		Found:  found,
	})
	return errors.AddContext(err, "Failed to send ProgramRefundResponse")
}
