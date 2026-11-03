# Builds the cisid server and agent images:
#
#   docker build --target server -t cisid-server .
#   docker build --target agent  -t cisid-agent .

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cisid-server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cisid-agent ./cmd/agent

# The server needs no privileges: it runs as an unprivileged user.
FROM alpine:3.22 AS server
RUN adduser -D -H -u 10001 cisid
COPY --from=build /out/cisid-server /usr/local/bin/cisid-server
USER 10001
EXPOSE 8080
ENTRYPOINT ["cisid-server"]

# The agent drives the host's Docker daemon through its socket, which
# requires root (or the socket's group) inside the container.
FROM alpine:3.22 AS agent
COPY --from=build /out/cisid-agent /usr/local/bin/cisid-agent
ENTRYPOINT ["cisid-agent"]
