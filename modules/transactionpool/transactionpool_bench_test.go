package transactionpool

import (
	"testing"

	"gitlab.com/NebulousLabs/Sia/types"
)

// BenchmarkAcceptTransactionSet will see how quickly a transaction set gets
// added to the transaction pool.
func BenchmarkAcceptTransactionSet(b *testing.B) {
	if testing.Short() {
		b.SkipNow()
	}

	// Create a transaction pool.
	tpt, err := createTpoolTester(b.Name())
	if err != nil {
		b.Fatal(err)
	}
	defer tpt.Close()

	// Create the source output for the transaction graph that we will be
	// submitting to the tpool.
	funds := types.SiacoinPrecision.Mul64(25e3)
	outputs := []types.SiacoinOutput{
		{
			UnlockHash: types.UnlockConditions{}.UnlockHash(),
			Value:      funds,
		},
	}
	txns, err := tpt.wallet.SendSiacoinsMulti(outputs)
	if err != nil {
		b.Fatal(err)
	}
	finalTxn := txns[len(txns)-1]
	sourceOutput := finalTxn.SiacoinOutputID(0)

	// Mine the genesis output into a block.
	_, err = tpt.miner.AddBlock()
	if err != nil {
		b.Fatal(err)
	}

	// Create a line of transactions with no special interaction.
	graphLen := 500
	fee := types.SiacoinPrecision
	var edges []types.TransactionGraphEdge
	for i := 0; i < graphLen; i++ {
		funds = funds.Sub(fee)
		edges = append(edges, types.TransactionGraphEdge{
			Dest:   i + 1,
			Fee:    fee,
			Source: i,
			Value:  funds,
		})
	}
	graph, err := types.TransactionGraph(sourceOutput, edges)
	if err != nil {
		b.Fatal(err)
	}

	// Perform the benchmark on adding a line of 50 transactions to the tpool.
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		b.StartTimer()
		err := tpt.tpool.AcceptTransactionSet(graph)
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		tpt.tpool.purge()
	}
}

// BenchmarkAcceptTransactionSetPiecemeal will see how quickly a transaction set
// gets added to the transaction pool when the set is added one transaction at a
// time.
func BenchmarkAcceptTransactionSetPiecemeal(b *testing.B) {
	if testing.Short() {
		b.SkipNow()
	}

	// Create a transaction pool.
	tpt, err := createTpoolTester(b.Name())
	if err != nil {
		b.Fatal(err)
	}
	defer tpt.Close()

	// Create the source output for the transaction graph that we will be
	// submitting to the tpool.
	funds := types.SiacoinPrecision.Mul64(25e3)
	outputs := []types.SiacoinOutput{
		{
			UnlockHash: types.UnlockConditions{}.UnlockHash(),
			Value:      funds,
		},
	}
	txns, err := tpt.wallet.SendSiacoinsMulti(outputs)
	if err != nil {
		b.Fatal(err)
	}
	finalTxn := txns[len(txns)-1]
	sourceOutput := finalTxn.SiacoinOutputID(0)

	// Mine the genesis output into a block.
	_, err = tpt.miner.AddBlock()
	if err != nil {
		b.Fatal(err)
	}

	// Create a line of transactions with no special interaction.
	graphLen := 500
	fee := types.SiacoinPrecision
	var edges []types.TransactionGraphEdge
	for i := 0; i < graphLen; i++ {
		funds = funds.Sub(fee)
		edges = append(edges, types.TransactionGraphEdge{
			Dest:   i + 1,
			Fee:    fee,
			Source: i,
			Value:  funds,
		})
	}
	graph, err := types.TransactionGraph(sourceOutput, edges)
	if err != nil {
		b.Fatal(err)
	}

	// Perform the benchmark on adding a line of 50 transactions to the tpool.
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		b.StartTimer()
		for _, txn := range graph {
			err := tpt.tpool.AcceptTransactionSet([]types.Transaction{txn})
			if err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		tpt.tpool.purge()
	}
}
