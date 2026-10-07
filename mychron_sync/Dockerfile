# Builds on the device that installs the add-on (a Pi 4 takes a minute or two).
FROM golang:1.22-alpine AS build
ARG BUILD_VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.version=${BUILD_VERSION}" \
      -o /out/mychron-sync ./cmd/mychron-sync

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/mychron-sync /usr/local/bin/mychron-sync
ENTRYPOINT ["/usr/local/bin/mychron-sync"]
CMD ["serve"]
