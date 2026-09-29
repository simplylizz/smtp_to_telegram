# Run the Go toolchain on the build host and cross-compile for the target
# platform, instead of compiling under QEMU emulation in multi-arch builds.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

WORKDIR /app

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .

ARG ST_VERSION TARGETOS TARGETARCH TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} go build \
        -ldflags "-s -w \
            -X main.Version=${ST_VERSION:-UNKNOWN_RELEASE}" \
        -tags urfave_cli_no_docs \
        -o smtp_to_telegram

FROM alpine

RUN apk add --no-cache ca-certificates mailcap

COPY --from=builder /app/smtp_to_telegram /smtp_to_telegram

USER daemon

ENV ST_SMTP_LISTEN="0.0.0.0:2525"
EXPOSE 2525

ENTRYPOINT ["/smtp_to_telegram"]
