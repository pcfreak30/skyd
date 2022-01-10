# These variables get inserted into ./build/commit.go
BUILD_TIME=$(shell date)
GIT_REVISION=$(shell git rev-parse --short HEAD)
GIT_DIRTY=$(shell git diff-index --quiet HEAD -- || echo "✗-")

ldflags= \
-X "gitlab.com/SkynetLabs/skyd/build.BinaryName=skyd" \
-X "gitlab.com/SkynetLabs/skyd/build.NodeVersion=1.6.0" \
-X "gitlab.com/SkynetLabs/skyd/build.GitRevision=${GIT_DIRTY}${GIT_REVISION}" \
-X "gitlab.com/SkynetLabs/skyd/build.BuildTime=${BUILD_TIME}"

racevars= history_size=3 halt_on_error=1 atexit_sleep_ms=2000

# all will build and install release binaries
all: release

# count says how many times to run the tests.
count = 1

# cpkg determines which package is the target when running 'make fullcover'.
# 'make fullcover' can only provide full coverage statistics on a single package
# at a time, unfortunately.
cpkg = ./skymodules/renter

# lockcheckpkgs are the packages that are checked for locking violations.
lockcheckpkgs = \
	./benchmark \
	./build \
	./cmd/skyc \
	./cmd/skyd \
	./cmd/skynet-benchmark \
	./compatibility \
	./fixtures \
	./node \
	./node/api \
	./node/api/client \
	./node/api/server \
	./profile \
	./siatest \
	./siatest/accounting \
	./siatest/daemon \
	./siatest/dependencies \
	./siatest/renter \
	./siatest/renter/contractor \
	./siatest/renter/hostdb \
	./siatest/renterhost \
	./siatest/skynet \
	./skykey \
	./skymodules \
	./skymodules/accounting \
	./skymodules/renter/filesystem \
	./skymodules/renter/filesystem/siadir \
	./skymodules/renter/filesystem/siafile \
	./skymodules/renter/hostdb \
	./skymodules/renter/hostdb/hosttree \
	./skymodules/renter/skynetblocklist \
	./skymodules/renter/skynetportals \

# pkgs changes which packages the makefile calls operate on. run changes which
# tests are run during testing.
pkgs = \
	$(lockcheckpkgs) \
	./skymodules/renter \
	./skymodules/renter/contractor \
	./skymodules/renter/proto

# mongohost is the hostname that mongodb uses to announce the node with within
# in the cluster.
mongohost = localhost

# mongouri is the uri that skyd will use to connect to in testing. If not
# specified it will default to localhost.
mongouri = mongodb://$(mongohost):27017

# release-pkgs determine which packages are built for release and distribution
# when running a 'make release' command.
release-pkgs = ./cmd/skyc ./cmd/skyd


# run determines which tests run when running any variation of 'make test'.
run = .

# util-pkgs determine the set of packages that are built when running
# 'make utils'
util-pkgs = ./cmd/skynet-benchmark

# dependencies list all packages needed to run make commands used to build, test
# and lint siac/siad locally and in CI systems.
dependencies:
	go get -d ./...
	./install-dependencies.sh

# fmt calls go fmt on all packages.
fmt:
	gofmt -s -l -w $(pkgs)

# vet calls go vet on all packages.
# NOTE: go vet requires packages to be built in order to obtain type info.
vet:
	go vet $(pkgs)

# markdown-spellcheck runs codespell on all markdown files that are not
# vendored.
markdown-spellcheck:
	git ls-files "*.md" :\!:"vendor/**" | xargs codespell

# lint runs golangci-lint (which includes golint, a spellcheck of the codebase,
# and other linters), the custom analyzers, and also a markdown spellchecker.
lint: markdown-spellcheck
	golangci-lint run -c .golangci.yml ./...
	analyze -lockcheck=false -- $(pkgs)
	analyze -lockcheck -- $(lockcheckpkgs)

# spellcheck checks for misspelled words in comments or strings.
spellcheck: markdown-spellcheck
	golangci-lint run -c .golangci.yml -E misspell

# staticcheck runs the staticcheck tool
# NOTE: this is not yet enabled in the CI system.
staticcheck:
	staticcheck $(pkgs)

start-mongo:
# Remove existing container.
	-docker stop mongo-test

# Start primary node.
	docker run \
      --rm \
      --detach \
      --name mongo-test \
      -p 127.0.0.1:27017:27017 \
      -e MONGODB_ADVERTISED_HOSTNAME=localhost \
      -e MONGODB_REPLICA_SET_MODE=primary \
      -e MONGODB_PRIMARY_HOST=localhost \
      -e MONGODB_ROOT_PASSWORD=pwd \
      -e MONGODB_REPLICA_SET_KEY=testkey \
      bitnami/mongodb:4.4.1

stop-mongo:
	-docker stop mongo-test

# debug builds and installs debug binaries. This will also install the utils.
debug:
	go install -tags='debug profile netgo' -ldflags='$(ldflags)' $(pkgs)
debug-race:
	GORACE='$(racevars)' go install -race -tags='debug profile netgo' -ldflags='$(ldflags)' $(pkgs)

# dev builds and installs developer binaries. This will also install the utils.
dev:
	go install -tags='dev debug profile netgo' -ldflags='$(ldflags)' $(pkgs)
dev-race:
	GORACE='$(racevars)' go install -race -tags='dev debug profile netgo' -ldflags='$(ldflags)' $(pkgs)

# release builds and installs release binaries.
release:
	go install -tags='netgo' -ldflags='$(ldflags)'  -gcflags="all=-N -l"  $(release-pkgs)
release-race:
	GORACE='$(racevars)' go install -race -tags='netgo' -ldflags='-s -w $(ldflags)' $(release-pkgs)
release-util:
	go install -tags='netgo' -ldflags='-s -w $(ldflags)' $(release-pkgs) $(util-pkgs)

# clean removes all directories that get automatically created during
# development.
clean:
ifneq ("$(OS)","Windows_NT")
# Linux
	rm -rf cover doc/whitepaper.aux doc/whitepaper.log doc/whitepaper.pdf docker fullcover release
else
# Windows
	- DEL /F /Q cover doc\whitepaper.aux doc\whitepaper.log doc\whitepaper.pdf docker fullcover release
endif

test:
	go test -short -tags='debug testing netgo' -timeout=5s $(pkgs) -run=$(run) -count=$(count)
test-v:
	GORACE='$(racevars)' go test -race -v -short -tags='debug testing netgo' -timeout=15s $(pkgs) -run=$(run) -count=$(count)
test-long: clean
	@mkdir -p cover
	GORACE='$(racevars)' MONGODB_URI=$(mongouri) go test -race --coverprofile='./cover/cover.out' -v -failfast -tags='testing debug netgo' -timeout=3600s $(pkgs) -run=$(run) -count=$(count)

# Use on Linux (and MacOS)
test-vlong: clean fmt vet lint
	@mkdir -p cover
	export MONGODB_URI=$(mongouri)
	GORACE='$(racevars)' MONGODB_URI=$(mongouri) go test --coverprofile='./cover/cover.out' -v -race -tags='testing debug vlong netgo' -timeout=20000s $(pkgs) -run=$(run) -count=$(count)

# Use on Windows without fmt, vet, lint
test-vlong-windows: clean
	MD cover
	SET GORACE='$(racevars)'
	go test --coverprofile='./cover/cover.out' -v -race -tags='testing debug vlong netgo' -timeout=20000s $(pkgs) -run=$(run) -count=$(count)

# docker-ci launches a docker container to run tests in the same
# environment as the online CI.
#
# Output from the docker ci can be found in the created docker folder. An
# output.txt file contains the stdout and stderr output and the test artifacts
# can be found in docker/SiaTesting
#
# The `|| true` after some of the docker commands is to allow the script to
# continue if the command errors. This is useful since some tests don't create
# test files in /SiaTesting so the copy command would fail and exit before the
# final commands to stop and remove the container.
docker-ci: clean
	@mkdir docker
	@docker build . -f siatest/Dockerfile -t skytest-ci
	@docker run --cpus="1" --name test -di skytest-ci
	@docker exec test make docker-test-long pkgs=$(pkgs) run=$(run) count=$(count) 2>&1 | tee docker/output.txt
	@docker cp test:/tmp/SiaTesting ./docker/SiaTesting || true
	@docker stop test || true && docker rm test || true
# docker-test-long allows for running long tests faster in the docker container
docker-test-long:
	GORACE='$(racevars)' go test -race -v -failfast -tags='testing debug netgo' -timeout=3600s $(pkgs) -run=$(run) -count=$(count)

test-cpu:
	go test -v -tags='testing debug netgo' -timeout=500s -cpuprofile cpu.prof $(pkgs) -run=$(run) -count=$(count)
test-mem:
	go test -v -tags='testing debug netgo' -timeout=500s -memprofile mem.prof $(pkgs) -run=$(run) -count=$(count)
bench: clean fmt
	go test -tags='debug testing netgo' -timeout=500s -run=XXX -bench=$(run) $(pkgs) -count=$(count)
cover: clean
	@mkdir -p cover
	@for package in $(pkgs); do                                                                                                                                 \
		mkdir -p `dirname cover/$$package`                                                                                                                      \
		&& go test -tags='testing debug netgo' -timeout=500s -covermode=atomic -coverprofile=cover/$$package.out ./$$package -run=$(run) || true 				\
		&& go tool cover -html=cover/$$package.out -o=cover/$$package.html ;                                                                                    \
	done

# fullcover is a command that will give the full coverage statistics for a
# package. Unlike the 'cover' command, full cover will include the testing
# coverage that is provided by all tests in all packages on the target package.
# Only one package can be targeted at a time. Use 'cpkg' as the variable for the
# target package, 'pkgs' as the variable for the packages running the tests.
#
# NOTE: this command has to run the full test suite to get output for a single
# package. Ideally we could get the output for all packages when running the
# full test suite.
#
# NOTE: This command will not skip testing packages that do not run code in the
# target package at all. For example, none of the tests in the 'sync' package
# will provide any coverage to the renter package. The command will not detect
# this and will run all of the sync package tests anyway.
fullcover: clean
	@mkdir -p fullcover
	@mkdir -p fullcover/tests
	@echo "mode: atomic" >> fullcover/fullcover.out
	@for package in $(pkgs); do                                                                                                                                                             \
		mkdir -p `dirname fullcover/tests/$$package`                                                                                                                                        \
		&& go test -tags='testing debug netgo' -timeout=500s -covermode=atomic -coverprofile=fullcover/tests/$$package.out -coverpkg $(cpkg) ./$$package -run=$(run) || true 				\
		&& go tool cover -html=fullcover/tests/$$package.out -o=fullcover/tests/$$package.html                                                                                              \
		&& tail -n +2 fullcover/tests/$$package.out >> fullcover/fullcover.out ;                                                                                                            \
	done
	@go tool cover -html=fullcover/fullcover.out -o fullcover/fullcover.html
	@printf 'Full coverage on $(cpkg):'
	@go tool cover -func fullcover/fullcover.out | tail -n -1 | awk '{$$1=""; $$2=""; sub(" ", " "); print}'

# whitepaper builds the whitepaper from whitepaper.tex. pdflatex has to be
# called twice because references will not update correctly the first time.
whitepaper:
	@pdflatex -output-directory=doc whitepaper.tex > /dev/null
	pdflatex -output-directory=doc whitepaper.tex

.PHONY: all fmt install release clean test test-v test-long cover whitepaper docker-ci docker-test-long

