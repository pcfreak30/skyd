# Start with the sia-ci docker image
FROM nebulouslabs/sia-ci:latest 

# Install minimum tooling to debug in container
RUN apt-get -y install sudo vim ranger && apt-get clean

# We want this to run on linux/amd64
ENV GOOS linux
ENV GOARCH amd64

# Define the GOCACHE folder
ENV GOCACHE /tmp

# Set the working Directory
WORKDIR /skyd

# Copy the go files to cache the dependency
COPY go.* .

# Copy the code files into the docker container
ADD . .

# Make dependencies and copy to cache
RUN make dependencies
COPY . .

# Prep for running test
RUN --mount=target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/.cache/go-build  
