# Build go binaries
FROM --platform=$BUILDPLATFORM golang:1.26.4 AS builder
ARG TARGETARCH

WORKDIR /workspace

# Download dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy the code source
COPY api/ ./api/
COPY cmd/ ./cmd/
COPY controllers/ ./controllers/
COPY extensions/ ./extensions/
COPY internal/ ./internal/
COPY plan/ ./plan/

# Build the daemon binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -o /lifecycle-daemon ./cmd/sandbox-lifecycle-daemon/main.go

# Minimal distroless image
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /lifecycle-daemon /lifecycle-daemon

ENTRYPOINT ["/lifecycle-daemon"]
