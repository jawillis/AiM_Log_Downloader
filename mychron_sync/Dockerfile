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

# Optional viewer. Fetched at build time at a pinned commit and never copied into
# this repository. If GitHub can't be reached the add-on still builds, without it.
# To follow a newer Lapline, change LAPLINE_REF (a commit, tag or branch) and Rebuild.
FROM alpine:3.20 AS lapline
RUN apk add --no-cache git
ARG LAPLINE_REPO=https://github.com/jawillis/Lapline.git
ARG LAPLINE_REF=39e1a345196412620be9a7a93cbcb9252f05e53d
RUN mkdir /lapline \
 && ( git init -q /src && cd /src \
      && git fetch -q --depth 1 "$LAPLINE_REPO" "$LAPLINE_REF" \
      && git archive FETCH_HEAD | tar -x -C /lapline ) \
    || echo "WARNING: could not fetch Lapline; building without the viewer"

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/mychron-sync /usr/local/bin/mychron-sync
COPY --from=lapline /lapline /opt/lapline
ENTRYPOINT ["/usr/local/bin/mychron-sync"]
CMD ["serve"]
