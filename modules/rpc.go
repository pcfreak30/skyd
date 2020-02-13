package modules

import (
	"errors"
	"io"

	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/NebulousLabs/siamux"
)

// RPCPriceTable contains the cost of executing a RPC on a host. Each host can
// set its own prices for the individual MDM instructions and RPC costs.
type RPCPriceTable struct {
	// UUID is a specifier that uniquely identifies this price table
	UUID types.ShortSpecifier

	// Expiry is a unix timestamp that specifies the time until which the
	// MDMCostTable is valid.
	Expiry int64 `json:"expiry"`

	// UpdatePriceTableCost refers to the cost of fetching a new price table
	// from the host.
	UpdatePriceTableCost types.Currency `json:"updatepricetablecost"`

	// FundEphemeralAccountCost refers to the cost of funding an ephemeral
	// account on the host.
	FundEphemeralAccountCost types.Currency `json:"fundephemeralaccountcost"`

	// MDM related costs
	//
	// InitBaseCost is the amount of cost that is incurred when an MDM program
	// starts to run. This doesn't include the memory used by the program data.
	// The total cost to initialize a program is calculated as
	// InitCost = InitiBaseCost + MemoryTimeCost * Time
	InitBaseCost types.Currency `json:"initbasecost"`

	// MemoryTimeCost is the amount of cost per byte per time that is incurred
	// by the memory consumption of the program.
	MemoryTimeCost types.Currency `json:"memorytimecost"`

	// Cost values specific to the Read instruction.
	ReadBaseCost   types.Currency `json:"readbasecost"`
	ReadLengthCost types.Currency `json:"readlengthcost"`
}

var (
	// ErrPriceTableExpired is returned when the RPC price is expired.
	ErrPriceTableExpired = errors.New("RPC price table was expired")

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

	// rpcResponse is a helper type for encoding and decoding RPC response
	// messages.
	rpcResponse struct {
		err  *RPCError
		data interface{}
	}
)

// NewRPCPriceTable returns an empty RPC price table
func NewRPCPriceTable(expiry int64) RPCPriceTable {
	pt := RPCPriceTable{
		Expiry: expiry,
	}
	fastrand.Read(pt.UUID[:])
	return pt
}

// Clone returns a deep copy of the rpc price table with an updated expiry, the
// host will call this function on its price table every time it hands out a
// price table to the renter.
func (pt *RPCPriceTable) Clone(expiry int64) (*RPCPriceTable, error) {
	// clone the pricetable
	var cloned RPCPriceTable
	bytes := encoding.Marshal(*pt)
	err := encoding.Unmarshal(bytes, &cloned)
	if err != nil {
		return nil, err
	}

	// update expiry and set a new UUID
	cloned.Expiry = expiry
	fastrand.Read(cloned.UUID[:])

	return &cloned, nil
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
	return encoding.WriteObject(stream, &rpcResponse{re, nil})
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
