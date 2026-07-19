# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w \
      -X github.com/hookship/hook-service/internal/buildinfo.Version=${VERSION} \
      -X github.com/hookship/hook-service/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/hookship/hook-service/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/hookshipd ./cmd/hookshipd \
    && CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w \
      -X github.com/hookship/hook-service/internal/buildinfo.Version=${VERSION} \
      -X github.com/hookship/hook-service/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/hookship/hook-service/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/hookshipctl ./cmd/hookshipctl

FROM scratch

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="HookShip delivery service" \
      org.opencontainers.image.description="HookShip outbound webhook delivery data-plane foundation" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/hookship/hook-service" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/hookshipd /hookshipd
COPY --from=build /out/hookshipctl /hookshipctl
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.md /licenses/

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/hookshipd"]
