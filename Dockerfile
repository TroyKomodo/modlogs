FROM golang:1.16.3-alpine3.13 AS build_base

RUN apk add --no-cache git

# Set the Current Working Directory inside the container
WORKDIR /tmp/app

# We want to populate the module cache based on the go.{mod,sum} files.
COPY go.mod .
COPY go.sum .

RUN go mod download
# RUN go get -u github.com/gobuffalo/packr/v2/packr2

COPY . .

# Build the Go app
# RUN packr2
RUN go build

# Start fresh from a smaller image
FROM alpine:3.9 
RUN apk add ca-certificates

WORKDIR /app

COPY --from=build_base /tmp/app/modlogs /app/modlogs
COPY config.yaml /app/config.yaml

# Run the binary program produced by `go install`
CMD ["/app/modlogs"]

