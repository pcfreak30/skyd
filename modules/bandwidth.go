package modules

// HasSectorJobExpectedBandwidth returns the expected bandwidth consumption of a
// has sector job. This helper function enables getting the expected
// bandwidth without having to instantiate a job.
func HasSectorJobExpectedBandwidth() (ul, dl uint64) {
	ul = 20e3
	dl = 20e3
	return
}

// ReadSectorJobExpectedBandwidth returns the expected bandwidth consumption of
// a read sector job. This helper function takes a length parameter and is used
// to get the expected bandwidth without having to instantiate a job.
func ReadSectorJobExpectedBandwidth(length uint64) (ul, dl uint64) {
	ul = 1 << 15                              // 32 KiB
	dl = uint64(float64(length)*1.01) + 1<<14 // (readSize * 1.01 + 16 KiB)
	return
}
