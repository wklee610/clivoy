# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
WORKDIR /src
COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" \
    -o /out/clivoy ./cmd/clivoy

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/wklee610/clivoy"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/clivoy /usr/local/bin/clivoy
USER 65532:65532
WORKDIR /home/nonroot
EXPOSE 9610
ENTRYPOINT ["/usr/local/bin/clivoy"]
CMD ["serve", "--config", "/etc/clivoy/config.json"]
