package renter

import (
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules"

	"gitlab.com/NebulousLabs/errors"
)

type (
	// workerPriceTable contains a price table and some information related to
	// retrieveing the next update.
	workerPriceTable struct {
		// The actual price table.
		staticPriceTable modules.RPCPriceTable

		// The next time that the worker should try to update the price table.
		staticUpdateTime time.Time

		// The number of consecutive failures that the worker has experienced in
		// trying to fetch the price table. This number is used to inform
		// staticUpdateTime, a larger number of consecutive failures will result in
		// greater backoff on fetching the price table.
		staticConsecutiveFailures uint64

		// staticRecentErr specifies the most recent error that the price table
		// update has failed with.
		staticRecentErr error
	}
)

// staticNeedsUpdate is a helper function that determines whether the price
// table should be updated.
func (wpt *workerPriceTable) staticNeedsUpdate() bool {
	return time.Now().After(wpt.staticUpdateTime)
}

// newPriceTable will initialize a price table for the worker.
func (w *worker) newPriceTable() {
	if w.staticPriceTable() != nil {
		w.renter.log.Critical("creating a new price table when a new price table already exists")
	}
	w.staticSetPriceTable(new(workerPriceTable))
}

// staticPriceTable will return the most recent price table for the worker's
// host.
func (w *worker) staticPriceTable() *workerPriceTable {
	ptr := atomic.LoadPointer(&w.atomicPriceTable)
	return (*workerPriceTable)(ptr)
}

// staticSetPriceTable will set the price table in the worker to be equal to the
// provided price table.
func (w *worker) staticSetPriceTable(pt *workerPriceTable) {
	atomic.StorePointer(&w.atomicPriceTable, unsafe.Pointer(pt))
}

// staticValid will return true if the latest price table that we have is still
// valid for the host.
//
// The price table is default invalid, because the zero time / empty time is
// before the current time, and the price table expiry defaults to the zero
// time.
func (wpt *workerPriceTable) staticValid() bool {
	return wpt.staticPriceTable.Expiry > time.Now().Unix()
}

// managedUpdatePriceTable performs the UpdatePriceTableRPC on the host.
func (w *worker) staticUpdatePriceTable() {
	// Create a goroutine to wake the worker when the time has come to check
	// the price table again.
	defer func() {
		go func() {
			updateTime := w.staticPriceTable().staticUpdateTime
			sleepDuration := updateTime.Sub(time.Now())
			time.Sleep(sleepDuration)
			w.staticWake()
		}()
	}()

	// Check the host version.
	//
	// TODO: After the protocol is stable, switch to '<=' instead of '!='.
	cache := w.staticCache()
	currentPT := w.staticPriceTable()
	if build.VersionCmp(cache.staticHostVersion, minAsyncVersion) != 0 {
		// Host is not a supported version, set the price table to be invalid.
		// This will potentially overwrite a valid price table, but the host no
		// longer speaks a valid protocol so we should invalidate the price
		// table.
		pt := &workerPriceTable{
			staticUpdateTime:          cooldownUntil(currentPT.staticConsecutiveFailures),
			staticConsecutiveFailures: currentPT.staticConsecutiveFailures + 1,
			staticRecentErr:           errors.New("host version is not compatible, unable to fetch price table"),
		}
		w.staticSetPriceTable(pt)
		println("pt update failed because version cmp")
		return
	}

	// All remaining errors represent short term issues with the host, so the
	// price table should be updated to represent the failure, but should retain
	// the existing price table, which will allow the renter to continue
	// performing tasks even though it's having trouble getting a new price
	// table.
	var err error
	defer func() {
		if err != nil {
			println(" <><><><><><><><><> price table update failed: ", err.Error())
			// Because of race conditions, can't modify the existing price
			// table, need to make a new one.
			pt := &workerPriceTable{
				staticPriceTable:          currentPT.staticPriceTable,
				staticUpdateTime:          cooldownUntil(currentPT.staticConsecutiveFailures),
				staticConsecutiveFailures: currentPT.staticConsecutiveFailures + 1,
				staticRecentErr:           err,
			}
			w.staticSetPriceTable(pt)
		}
	}()

	// Get a stream.
	stream, err := w.staticNewStream()
	if err != nil {
		println("stream failed: ", err.Error())
		return
	}
	defer func() {
		// An error closing the stream is not sufficient reason to reject the
		// price table that the host gave us. Because there is a defer checking
		// for the value of 'err', we use a different variable name here.
		streamCloseErr := stream.Close()
		if streamCloseErr != nil {
			w.renter.log.Println("ERROR: failed to close stream", streamCloseErr)
			println("stream close failed: ", streamCloseErr.Error())
		}
	}()

	// write the specifier
	err = modules.RPCWrite(stream, modules.RPCUpdatePriceTable)
	if err != nil {
		println("rpc write failed: ", err.Error())
		return
	}

	// receive the price table
	var uptr modules.RPCUpdatePriceTableResponse
	err = modules.RPCRead(stream, &uptr)
	if err != nil {
		println("rpc read failed: ", err.Error())
		return
	}

	// decode the JSON
	var pt modules.RPCPriceTable
	err = json.Unmarshal(uptr.PriceTableJSON, &pt)
	if err != nil {
		println("json unmarshal failed: ", err.Error())
		return
	}

	// TODO: Check for gouging before paying.

	// provide payment
	err = w.renter.hostContractor.ProvidePayment(stream, w.staticHostPubKey, modules.RPCUpdatePriceTable, pt.UpdatePriceTableCost, w.staticAccount.staticID, cache.staticBlockHeight)
	if err != nil {
		println("provide payment failed: ", err.Error())
		return
	}

	// expect stream to be closed (the host only sees a PT as valid if it
	// successfully managed to process payment, not awaiting the close allows
	// for a race condition where we consider it valid but the host does not
	//
	// We use a different error name here because this error shouldn't
	// invalidate the price table.
	//
	// TODO: Why is this here?
	bogusReadErr := modules.RPCRead(stream, struct{}{})
	if bogusReadErr == nil || !strings.Contains(bogusReadErr.Error(), io.ErrClosedPipe.Error()) {
		println("unexpected err on rpc read: ", bogusReadErr.Error())
		w.renter.log.Println("ERROR: expected io.ErrClosedPipe, instead received err:", bogusReadErr)
	}

	// Update the price table.
	//
	// TODO: Do something smarter for the update time than 5 minutes.
	wpt := &workerPriceTable{
		staticPriceTable:          pt,
		staticUpdateTime:          time.Now().Add(5 * time.Minute),
		staticConsecutiveFailures: 0,
		staticRecentErr:           currentPT.staticRecentErr,
	}
	println("setting the price table???")
	w.staticSetPriceTable(wpt)
}
