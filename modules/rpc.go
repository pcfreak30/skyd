package modules

import (
	"io"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/siamux"
)

type (
	// RPCPriceTable contains a list of costs associated to RPCs. It is uniquely
	// identified by its uuid, and is given out by the host which guarantees the
	// listed costs up until the expiry timestamp.
	RPCPriceTable struct {
		UUID   types.ShortSpecifier
		Costs  map[types.Specifier]types.Currency
		Expiry int64
	}

	// rpcResponse is a helper type for encoding and decoding RPC response
	// messages.
	rpcResponse struct {
		err  *RPCError
		data interface{}
	}
)

var (
	// RPCUpdatePriceTable specifier
	RPCUpdatePriceTable = types.NewSpecifier("UpdatePriceTable")

	// RPCFundEphemeralAccount specifier
	RPCFundEphemeralAccount = types.NewSpecifier("FundEphemeralAcc")

	// RPCExecuteMDMProgram specifier
	RPCExecuteMDMProgram = types.NewSpecifier("ExecMDMProgram")
)

type (
	// RPCUpdatePriceTableResponse contains a JSON encoded price table.
	RPCUpdatePriceTableResponse struct {
		PriceTableJSON []byte
	}

	// RPCFundEphemeralAccountRequest specifies the ephemeral account id.
	RPCFundEphemeralAccountRequest struct {
		AccountID string
	}

	// RPCFundEphemeralAccountResponse contains the signature. This signature
	// is a signed receipt, and can be used as proof of payment.
	RPCFundEphemeralAccountResponse struct {
		Receipt   Receipt
		Signature []byte
	}

	// RPCExecuteProgramRequest contains the filecontract ID on which to execute
	// the program.
	RPCExecuteProgramRequest struct {
		FileContractID types.FileContractID
	}
)

// NewRPCPriceTable returns an empty RPC price table
func NewRPCPriceTable(expiry int64) RPCPriceTable {
	pt := RPCPriceTable{
		Expiry: expiry,
		Costs:  make(map[types.Specifier]types.Currency),
	}
	fastrand.Read(pt.UUID[:])
	return pt
}

// Clone returns a deep copy of the rpc price table with an updated expiry, the
// host will call this function on its price table every time it hands out a
// price table to the renter.
func (pt *RPCPriceTable) Clone(expiry int64) *RPCPriceTable {
	cloned := NewRPCPriceTable(expiry)
	for k, v := range pt.Costs {
		cloned.Costs[k] = v
	}
	fastrand.Read(cloned.UUID[:])
	return &cloned
}

// RPCRead tries to read the given object from the stream.
func RPCRead(stream siamux.Stream, obj interface{}) error {
	return encoding.ReadObject(stream, &rpcResponse{nil, obj}, uint64(RPCMinLen))
}

// RPCWrite writes the given object to the stream.
func RPCWrite(stream siamux.Stream, obj interface{}) error {
	return encoding.WriteObject(stream, &rpcResponse{nil, obj})
}

// RPCWriteAll writes the given objects to the stream.
func RPCWriteAll(stream siamux.Stream, objs ...interface{}) error {
	for _, obj := range objs {
		err := encoding.WriteObject(stream, &rpcResponse{nil, obj})
		if err != nil {
			return err
		}
	}
	return nil
}

// RPCWriteError writes the given error to the stream.
func RPCWriteError(stream siamux.Stream, err error) error {
	re, ok := err.(*RPCError)
	if err != nil && !ok {
		re = &RPCError{Description: err.Error()}
	}
	return encoding.WriteObject(stream, rpcResponse{re, nil})
}

// MarshalSia implements the encoding.SiaMarshaler interface.
func (resp *rpcResponse) MarshalSia(w io.Writer) error {
	if resp.data == nil {
		resp.data = struct{}{}
	}
	return encoding.NewEncoder(w).EncodeAll(resp.err, resp.data)
}

// UnmarshalSia implements the encoding.SiaUnmarshaler interface.
func (resp *rpcResponse) UnmarshalSia(r io.Reader) error {
	// NOTE: no allocation limit is required because this method is always
	// called via encoding.Unmarshal, which already imposes an allocation limit.
	d := encoding.NewDecoder(r, 0)
	if err := d.Decode(&resp.err); err != nil {
		return err
	} else if resp.err != nil {
		return resp.err
	}
	return d.Decode(resp.data)
}
