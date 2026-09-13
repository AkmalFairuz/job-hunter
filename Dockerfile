# syntax=docker/dockerfile:1

FROM golang:1.25.8-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/jobbot ./cmd/jobbot
COPY finder ./finder
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/jobbot ./cmd/jobbot

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=nonroot:nonroot /out/jobbot /jobbot
COPY --chown=nonroot:nonroot LICENSE /licenses/job-hunter/LICENSE

ENTRYPOINT ["/jobbot"]
