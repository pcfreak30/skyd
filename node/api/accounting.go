package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/skynetlabs/skyd/skymodules/accounting"
)

// accountingHandlerGet handles the API call for /accounting
func (api *API) accountingHandlerGet(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	// Check for range
	var err error

	// Check for Start time
	startStr := req.Form.Get("start")
	var start int64
	if startStr != "" {
		start, err = strconv.ParseInt(startStr, 10, 64)
		if err != nil {
			WriteError(w, Error{"unable to parse start time" + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Check for End time
	endStr := req.Form.Get("end")
	var end int64 = accounting.DefaultEndRangeTime
	if endStr != "" {
		end, err = strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			WriteError(w, Error{"unable to parse end time" + err.Error()}, http.StatusBadRequest)
			return
		}
	}

	// Sanity check range
	if end < start {
		errStr := fmt.Sprintf("cannot provided end time that is before start time: start %v > end %v", start, end)
		WriteError(w, Error{errStr}, http.StatusBadRequest)
		return
	}
	if end < 0 || start < 0 {
		WriteError(w, Error{"cannot provided negative start or end time"}, http.StatusBadRequest)
		return
	}

	// Request the range of accounting information
	ais, err := api.accounting.Accounting(start, end)
	if err != nil {
		WriteError(w, Error{"unable to get accounting information" + err.Error()}, http.StatusInternalServerError)
		return
	}

	// Return the Accounting information
	WriteJSON(w, ais)
	return
}
