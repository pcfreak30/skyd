package dependencies

// NewDependencySkipUnpinRequest skips submitting the unpin request.
func NewDependencySkipUnpinRequest() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("SkipUnpinRequest")
}

// NewDependencyDoNotUploadFanout skips submitting the unpin request.
func NewDependencyDoNotUploadFanout() *DependencyWithDisableAndEnable {
	return newDependencywithDisableAndEnable("DoNotUploadFanout")
}
