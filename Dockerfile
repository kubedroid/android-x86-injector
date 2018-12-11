FROM golang:1.11 AS build

WORKDIR /go/src/android-x86-injector

# Download dep
RUN curl -fsSL -o /usr/local/bin/dep https://github.com/golang/dep/releases/download/v0.5.0/dep-linux-amd64 && chmod +x /usr/local/bin/dep

# Restore the dependencies. As long as Gopkg.toml and Gopkg.lock remain stable,
# this step can be cached
COPY src/android-x86-injector/Gopkg.toml src/android-x86-injector/Gopkg.lock ./
RUN dep ensure -vendor-only

# Build the entire project
COPY src/android-x86-injector/*.go ./
RUN go build

FROM ubuntu:bionic

# Copy the executable
COPY --from=build /go/src/android-x86-injector/android-x86-injector /usr/local/bin/

ENTRYPOINT [ "/usr/local/bin/android-x86-injector" ]
