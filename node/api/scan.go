package api

import (
	"math/big"

	"errors"

	"go.sia.tech/siad/types"
)

// scanAmount scans a types.Currency from a string.
func scanAmount(amount string) (types.Currency, bool) {
	// use SetString manually to ensure that amount does not contain
	// multiple values, which would confuse fmt.Scan
	i, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return types.Currency{}, ok
	}
	return types.NewCurrency(i), true
}

// scanBool converts "true" and "false" strings to their respective
// boolean value and returns an error if conversion is not possible.
func scanBool(param string) (bool, error) {
	if param == "true" {
		return true, nil
	} else if param == "false" || len(param) == 0 {
		return false, nil
	}
	return false, errors.New("could not decode boolean: value was not true or false")
}
