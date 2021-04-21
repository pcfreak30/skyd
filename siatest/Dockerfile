# Start with the sia-ci docker image
FROM nebulouslabs/sia-ci:latest 

# Install minimum tooling to debug in container
RUN apt-get -y install sudo vim && apt-get clean

# We want this to run on linux/amd64
ENV GOOS linux
ENV GOARCH amd64

# Define the GOCACHE folder
ENV GOCACHE /tmp

# Set the working Directory
WORKDIR /root

# Copy the code files into the docker container
ADD . /root/skyd

# Make dependencies 
RUN cd skyd && make dependencies

# Prep for running test
RUN --mount=target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/.cache/go-build  
