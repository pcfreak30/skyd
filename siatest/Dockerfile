# Start with the sia-ci docker image
FROM nebulouslabs/sia-ci:latest 

# Install minimum tooling to debug in container
RUN apt-get -y install sudo vim ranger && apt-get clean

# Define the GOCACHE folder
ENV GOCACHE /tmp

# Set the working Directory
WORKDIR /skyd

# Copy the go files from the base image
COPY go.* .
# Download go modules to cache dependencies
RUN go mod download
COPY . .

# Copy in the skyd repo
ADD . .

# Prep for running test
RUN --mount=target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/.cache/go-build  
