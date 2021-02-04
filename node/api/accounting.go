package api

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// accountingHandlerGet handles the API call for /accounting
func (api *API) accountingHandlerGet(w http.ResponseWriter, _ *http.Request, _ httprouter.Params) {
	ai, err := api.accounting.Accounting()
	if err != nil {
		WriteError(w, Error{"unable to get current accounting information" + err.Error()}, http.StatusInternalServerError)
		return
	}

	// Return the Accounting information
	WriteJSON(w, ai)
	return
}
