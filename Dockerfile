# syntax=docker/dockerfile:1

# ---- build stage ----
# The build host's TARGETARCH is honored automatically by BuildKit; this image
# builds natively on amd64 and arm64.
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod ./
RUN go mod download

COPY . .

# Static, fully self-contained binary; CGO is not used (pure Go numerics).
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/heateserver ./cmd/heateserver
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go test ./...

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/heateserver /usr/local/bin/heateserver
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/heateserver"]
CMD ["-addr=:8080"]
