FROM golang:1.26-alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -o /out/ingest ./cmd/ingest

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /out/ingest ./ingest
COPY config/config.example.yaml ./config/config.example.yaml
RUN mkdir -p /data/state /data/ingest /data/mock-cards

# Runs as root: the container writes into bind-mounted host directories
# (dev/data, dev/cards, and later NAS shares) whose ownership we don't
# control, and this is a two-person appliance, not a multi-tenant service -
# see RUNBOOK_MOY_MEDIA_INGEST.md's "avoid unnecessary security layers".
EXPOSE 8080
ENTRYPOINT ["./ingest"]
