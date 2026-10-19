FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd/vpnmodo-controller ./cmd/vpnmodo-controller
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vpnmodo-controller ./cmd/vpnmodo-controller

FROM docker:28.5.1-cli
COPY --from=build /out/vpnmodo-controller /usr/local/bin/vpnmodo-controller
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/vpnmodo-controller"]
CMD ["serve"]
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/vpnmodo-controller", "health"]
