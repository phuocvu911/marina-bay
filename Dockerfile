# Builder pinned to the Go version go.mod declares, so a toolchain bump is a
# deliberate edit in both places rather than something the base image does on
# its own.
FROM golang:1.22-alpine AS build

WORKDIR /src

# Dependencies first: this layer is rebuilt only when go.mod/go.sum change,
# not on every source edit.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off is the whole point of choosing modernc.org/sqlite: the driver is
# pure Go, so there is no gcc in the builder, no libc in the runtime, and no
# musl/glibc mismatch to line up between the two stages. -trimpath keeps build
# paths out of the binary; -s -w drops the debug tables.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /marina-bay .

# Runtime: alpine rather than scratch or distroless. The binary is static and
# needs nothing from the base image, but `fly ssh console` drops you into the
# image's shell, and a shell plus wget is exactly what you want on the machine
# when a gateway "cannot reach the server" and you need to tell a DNS problem
# from a TLS one. The cost is a few megabytes.
FROM alpine:3.20

RUN apk add --no-cache su-exec \
    && adduser -D -H -u 10001 marina

# templates/ and static/ are embedded in the binary, so this is the whole app.
COPY --from=build /marina-bay /usr/local/bin/marina-bay
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
