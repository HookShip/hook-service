# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# Builds the evaluation demo tools: a local webhook sink and a reference
# protocol-v1 producer client. These are for local demos only, not the delivery
# service itself.
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/local-sink ./examples/local-sink \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/evaluation-client ./examples/evaluation-client

FROM scratch
COPY --from=build /out/local-sink /local-sink
COPY --from=build /out/evaluation-client /evaluation-client
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 9099
ENTRYPOINT ["/local-sink"]
