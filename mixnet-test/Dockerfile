FROM golang:1.25-alpine AS builder

WORKDIR /app

ENV GOTOOLCHAIN=auto

# Copy go.mod and go.sum from the root
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the entire project to build
COPY . .

# Build the test binary
RUN go build -o mixnet-node ./mixnet-test/main.go

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/mixnet-node .

# Libp2p port
EXPOSE 4001
# Status port
EXPOSE 8080

ENTRYPOINT ["./mixnet-node"]
