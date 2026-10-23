FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /peershare .

# One image for routers and peers: routers need iptables, peers need ip to
# route through their router and curl to read the mounted share.
FROM alpine:3.22
RUN apk add --no-cache iptables iproute2 curl
COPY --from=build /peershare /usr/local/bin/peershare
