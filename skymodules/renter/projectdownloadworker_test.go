package renter

import "testing"

func TestChimeraWorker_addWorker(t *testing.T) {
	iw := &individualWorker{staticResolveChance: .1}
	iw2 := &individualWorker{staticResolveChance: .2}
	iw3 := &individualWorker{staticResolveChance: .5}
	iw4 := &individualWorker{staticResolveChance: .7}

	cw := NewChimeraWorker()
	r1 := cw.addWorker(iw)
	r2 := cw.addWorker(iw2)
	r3 := cw.addWorker(iw3)
	r4 := cw.addWorker(iw4)

	t.Log(r1)
	t.Log(r2)
	t.Log(r3)
	t.Log(r4)

}
